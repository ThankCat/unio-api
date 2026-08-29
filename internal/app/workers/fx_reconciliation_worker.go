package workers

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/admin/adminmessage"
)

// fxReconciliationInterval：每日对账（PLAN §9.2）。窗口取前一自然日（UTC），
// 但 I4（余额=分录累计）是全量时点校验，不受窗口限制。
const fxReconciliationInterval = 24 * time.Hour

// FxReconciliationStore 定义对账所需查询，由 *sqlc.Queries 满足。
type FxReconciliationStore interface {
	ReconcileCostLedgerMismatch(ctx context.Context, arg sqlc.ReconcileCostLedgerMismatchParams) ([]sqlc.ReconcileCostLedgerMismatchRow, error)
	ReconcileFxCompleteness(ctx context.Context, arg sqlc.ReconcileFxCompletenessParams) ([]int64, error)
	ReconcileFxConversion(ctx context.Context, arg sqlc.ReconcileFxConversionParams) ([]sqlc.ReconcileFxConversionRow, error)
	ReconcileProviderBalance(ctx context.Context) ([]sqlc.ReconcileProviderBalanceRow, error)
}

// FxReconciliationWorker 每日跑账务不变量 I1–I4（§9.1）：任何一项非 0 即写 critical 站内告警。
// 结果不落独立对账表（MVP 以消息中心 + 结构化日志为记录，运行表后续按需补）。
type FxReconciliationWorker struct {
	store    FxReconciliationStore
	messages FxMessagePublisher
	logger   *zap.Logger
	now      func() time.Time

	nextRunAt time.Time
}

// NewFxReconciliationWorker 创建每日对账 worker。
func NewFxReconciliationWorker(store FxReconciliationStore, messages FxMessagePublisher, logger *zap.Logger) *FxReconciliationWorker {
	if store == nil {
		panic("workers: fx reconciliation store is required")
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &FxReconciliationWorker{store: store, messages: messages, logger: logger, now: time.Now}
}

// Name 返回 worker 名称。
func (w *FxReconciliationWorker) Name() string { return "fx_reconciliation" }

// RunOnce 到期时跑一轮全部不变量。
func (w *FxReconciliationWorker) RunOnce(ctx context.Context) (bool, error) {
	now := w.now()
	if now.Before(w.nextRunAt) {
		return false, nil
	}
	w.nextRunAt = now.Add(fxReconciliationInterval)

	// 窗口 = 前一自然日（UTC）；首轮启动时同样只看昨天，历史存量由人工一次性核对。
	dayStart := now.UTC().Truncate(24 * time.Hour)
	from := pgtype.Timestamptz{Time: dayStart.Add(-24 * time.Hour), Valid: true}
	to := pgtype.Timestamptz{Time: dayStart, Valid: true}

	failures := 0

	if rows, err := w.store.ReconcileCostLedgerMismatch(ctx, sqlc.ReconcileCostLedgerMismatchParams{FromTime: from, ToTime: to}); err != nil {
		return true, failure.Wrap(failure.CodeAdminStoreFailed, err, failure.WithMessage("reconcile I1 cost ledger mismatch"))
	} else if len(rows) > 0 {
		failures++
		w.alert(ctx, "I1", fmt.Sprintf("发现 %d 笔请求的 provider 扣款与成本快照不一致（示例 request_record_id=%d）", len(rows), rows[0].RequestRecordID))
	}

	if ids, err := w.store.ReconcileFxCompleteness(ctx, sqlc.ReconcileFxCompletenessParams{FromTime: from, ToTime: to}); err != nil {
		return true, failure.Wrap(failure.CodeAdminStoreFailed, err, failure.WithMessage("reconcile I2 fx completeness"))
	} else if len(ids) > 0 {
		failures++
		w.alert(ctx, "I2", fmt.Sprintf("发现 %d 条成本快照的钉汇率三列形态非法（示例 id=%d），检查 ck_cost_snapshots_fx 约束是否被移除", len(ids), ids[0]))
	}

	if rows, err := w.store.ReconcileFxConversion(ctx, sqlc.ReconcileFxConversionParams{FromTime: from, ToTime: to}); err != nil {
		return true, failure.Wrap(failure.CodeAdminStoreFailed, err, failure.WithMessage("reconcile I3 fx conversion"))
	} else if len(rows) > 0 {
		failures++
		w.alert(ctx, "I3", fmt.Sprintf("发现 %d 条成本快照的 USD 归一列与 原币÷钉住汇率 不一致（示例 id=%d）", len(rows), rows[0].ID))
	}

	if rows, err := w.store.ReconcileProviderBalance(ctx); err != nil {
		return true, failure.Wrap(failure.CodeAdminStoreFailed, err, failure.WithMessage("reconcile I4 provider balance"))
	} else if len(rows) > 0 {
		failures++
		w.alert(ctx, "I4", fmt.Sprintf("发现 %d 个 provider 余额与账本累计不一致（示例 provider_id=%d %s）", len(rows), rows[0].ProviderID, rows[0].Currency))
	}

	if failures == 0 {
		w.logger.Info("fx reconciliation clean", zap.Time("window_from", from.Time), zap.Time("window_to", to.Time))
	}
	return true, nil
}

func (w *FxReconciliationWorker) alert(ctx context.Context, check, detail string) {
	w.logger.Error("fx reconciliation violation",
		zap.String("check", check), zap.String("detail", detail),
		zap.String("alert", "fx_reconciliation_violation"))
	if w.messages == nil {
		return
	}
	if _, err := w.messages.Publish(ctx, adminmessage.PublishParams{
		Severity:  adminmessage.SeverityCritical,
		Topic:     "reconciliation",
		Title:     fmt.Sprintf("每日对账失败：不变量 %s 不成立", check),
		Body:      detail + "。账务两条独立表示对不上，请当日排查；已落库分录不回滚，修正一律走 adjustment 冲正分录。",
		Source:    w.Name(),
		DedupeKey: "reconciliation-" + check,
	}); err != nil {
		w.logger.Error("fx reconciliation: publish admin message failed", failure.LogFields(err)...)
	}
}
