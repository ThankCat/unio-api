package workers

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/admin/adminmessage"
)

// fxMarginRecheckInterval：毛利是时变判定（D6）——守卫只在配置写入时硬校验，
// 汇率漂移后的存量违规靠本 worker 周期复查告警（不自动改配置、不硬卡）。
const fxMarginRecheckInterval = time.Hour

// marginViolationsQuery 直接读守卫共用的违规视图（M5）：与触发器口径永不分叉。
// 视图被 DO/EXECUTE 包裹创建（sqlc 无法解析其 DDL），故此处用原生 SQL 而非 sqlc 生成代码。
// 数值收敛到可读位数（告警正文给人看，精确值在配置表里）；sale 在 Fast 缺售价分项时可为 NULL。
const marginViolationsQuery = `
SELECT channel_id, model_id, component,
       COALESCE(round(sale, 6)::text, '—'), COALESCE(round(cost, 6)::text, '—'),
       sale_currency, cost_currency,
       COALESCE(round(fx_rate, 4)::text, ''), COALESCE(fx_rate_date::text, '')
FROM margin_violations_current
LIMIT 50`

// FxMarginRecheckWorker 周期 SELECT margin_violations_current，把每条存量违规写进
// admin 消息中心（dedupe 防轰炸：同一违规在上一条未读被处理前不重复告警）。
type FxMarginRecheckWorker struct {
	db       sqlc.DBTX
	messages FxMessagePublisher
	logger   *zap.Logger
	now      func() time.Time

	nextRunAt time.Time
}

// NewFxMarginRecheckWorker 创建毛利复查 worker。
func NewFxMarginRecheckWorker(db sqlc.DBTX, messages FxMessagePublisher, logger *zap.Logger) *FxMarginRecheckWorker {
	if db == nil {
		panic("workers: fx margin recheck db is required")
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &FxMarginRecheckWorker{db: db, messages: messages, logger: logger, now: time.Now}
}

// Name 返回 worker 名称。
func (w *FxMarginRecheckWorker) Name() string { return "fx_margin_recheck" }

// RunOnce 到期时复查一轮存量毛利违规。
func (w *FxMarginRecheckWorker) RunOnce(ctx context.Context) (bool, error) {
	now := w.now()
	if now.Before(w.nextRunAt) {
		return false, nil
	}
	w.nextRunAt = now.Add(fxMarginRecheckInterval)

	rows, err := w.db.Query(ctx, marginViolationsQuery)
	if err != nil {
		return false, failure.Wrap(failure.CodeAdminStoreFailed, err, failure.WithMessage("query margin violations view"))
	}
	defer rows.Close()

	violations := 0
	for rows.Next() {
		var channelID, modelID int64
		var component, sale, cost, saleCurrency, costCurrency, fxRate, fxRateDate string
		if err := rows.Scan(&channelID, &modelID, &component, &sale, &cost, &saleCurrency, &costCurrency, &fxRate, &fxRateDate); err != nil {
			return true, failure.Wrap(failure.CodeAdminStoreFailed, err, failure.WithMessage("scan margin violation row"))
		}
		violations++
		rateNote := "同币种，无换汇"
		if costCurrency != saleCurrency {
			if fxRate == "" {
				rateNote = fmt.Sprintf("跨币种（%s→%s）且当前无可用汇率", saleCurrency, costCurrency)
			} else {
				rateNote = fmt.Sprintf("按 %s 汇率 %s 折算（1 %s = %s %s）", fxRateDate, fxRate, saleCurrency, fxRate, costCurrency)
			}
		}
		w.logger.Error("margin recheck violation",
			zap.Int64("channel_id", channelID), zap.Int64("model_id", modelID),
			zap.String("component", component), zap.String("sale", sale), zap.String("cost", cost),
			zap.String("sale_currency", saleCurrency), zap.String("cost_currency", costCurrency),
			zap.String("fx_rate", fxRate), zap.String("fx_rate_date", fxRateDate),
			zap.String("alert", "margin_recheck_violation"))
		if w.messages != nil {
			_, pubErr := w.messages.Publish(ctx, adminmessage.PublishParams{
				Severity: adminmessage.SeverityCritical,
				Topic:    "margin",
				Title:    fmt.Sprintf("存量配置出现负毛利：渠道 %d × 模型 %d（%s）", channelID, modelID, component),
				Body: fmt.Sprintf(
					"售价 %s %s < 成本 %s %s；%s。汇率变动可能已把该线路推入亏本区间，请调价或停用渠道（系统不自动处理）。",
					sale, saleCurrency, cost, costCurrency, rateNote,
				),
				Source:    w.Name(),
				DedupeKey: fmt.Sprintf("margin-violation-%d-%d-%s", channelID, modelID, component),
			})
			if pubErr != nil {
				w.logger.Error("margin recheck: publish admin message failed", failure.LogFields(pubErr)...)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return true, failure.Wrap(failure.CodeAdminStoreFailed, err, failure.WithMessage("iterate margin violation rows"))
	}
	if violations == 0 {
		w.logger.Debug("margin recheck clean")
	}
	return true, nil
}
