package health

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/core/accountusage"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/logging"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

// ReconcileQueries 是阈值变更后重算运行态所需的查询。
type ReconcileQueries interface {
	AdminListUsagePauseReconcileAccounts(ctx context.Context, arg sqlc.AdminListUsagePauseReconcileAccountsParams) ([]sqlc.AdminListUsagePauseReconcileAccountsRow, error)
}

// ReconcileScope 限定一次重算覆盖的账号：零值 = 全部启用中的池内账号。
type ReconcileScope struct {
	ChannelID int64
	AccountID int64
}

// ReconcileResult 是一次重算的统计，回给管理端展示「保存后刷新了什么」。
type ReconcileResult struct {
	// Scanned 是纳入重算的账号数（启用中且属于池型渠道）。
	Scanned int `json:"scanned"`
	// Paused / Resumed 是按当前快照与生效阈值分别写成暂停 / 恢复的账号数（覆盖语义，含状态未变的）。
	Paused  int `json:"paused"`
	Resumed int `json:"resumed"`
	// Skipped 是没有用量快照、无从判定而未动 Redis 的账号数。
	Skipped int `json:"skipped"`
	// Failed 是 Redis 写入失败的账号数；单个失败不中断其余账号。
	Failed int `json:"failed"`
}

// Reconciler 在任一层阈值变更后，按账号最近快照与新的生效阈值重写 Redis usage_pause 标记（展示缓存）。
//
// 调度侧拦截本身按快照实时判定、不读这个标记，所以重算失败不会「拦不住」；它保证管理端看到的
// 「暂停中」与调度实际行为一致，并让 Retry-After 之类的聚合展示不再指向一个已经作废的旧阈值结论。
type Reconciler struct {
	queries     ReconcileQueries
	runtime     RuntimeStore
	logger      *zap.Logger
	thresholdFn func(ctx context.Context) int32
	now         func() time.Time
}

// NewReconciler 创建重算器。thresholdFn 提供全局阈值（与 Recorder 同一来源）。
func NewReconciler(queries ReconcileQueries, runtime RuntimeStore, thresholdFn func(ctx context.Context) int32, logger *zap.Logger) *Reconciler {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Reconciler{queries: queries, runtime: runtime, logger: logger, thresholdFn: thresholdFn, now: time.Now}
}

// ReconcileAll 重算全部启用中的池内账号（全局阈值变更）。
func (r *Reconciler) ReconcileAll(ctx context.Context) (ReconcileResult, error) {
	return r.Reconcile(ctx, ReconcileScope{})
}

// ReconcileChannel 重算某个池型渠道下的账号（渠道阈值变更）。
func (r *Reconciler) ReconcileChannel(ctx context.Context, channelID int64) (ReconcileResult, error) {
	return r.Reconcile(ctx, ReconcileScope{ChannelID: channelID})
}

// ReconcileAccount 重算单个账号（账号阈值变更）。
func (r *Reconciler) ReconcileAccount(ctx context.Context, accountID int64) (ReconcileResult, error) {
	return r.Reconcile(ctx, ReconcileScope{AccountID: accountID})
}

// Reconcile 按范围读出账号快照与两层阈值覆写，逐个重写运行态标记。
// 列表读不出来才返回 error；单个账号的 Redis 写失败只计入 Failed。
func (r *Reconciler) Reconcile(ctx context.Context, scope ReconcileScope) (ReconcileResult, error) {
	if r == nil || r.queries == nil || r.runtime == nil {
		return ReconcileResult{}, nil
	}
	rows, err := r.queries.AdminListUsagePauseReconcileAccounts(ctx, sqlc.AdminListUsagePauseReconcileAccountsParams{
		ChannelID: optionalInt8(scope.ChannelID),
		AccountID: optionalInt8(scope.AccountID),
	})
	if err != nil {
		// 列表读失败这里也记一笔：部分调用方（渠道更新）没有自己的日志器，只能靠这里留痕。
		logging.Warn(r.logger, "runtime", "account", "reconcile usage pause: list accounts failed",
			zap.Int64("channel_id", scope.ChannelID),
			zap.Int64("account_id", scope.AccountID),
			zap.String("error_message", err.Error()),
		)
		return ReconcileResult{}, err
	}
	now := r.now()
	global := r.globalThreshold(ctx)
	result := ReconcileResult{Scanned: len(rows)}
	for _, row := range rows {
		snapshot, ok := accountusage.ParseSnapshot(row.UsageSnapshot)
		if !ok {
			result.Skipped++
			continue
		}
		threshold := accountusage.ResolveThreshold(
			thresholdOverride(row.UsagePauseThresholdPercent),
			thresholdOverride(row.ChannelUsagePauseThresholdPercent),
			global,
		)
		decision := accountusage.Evaluate(snapshot, threshold.Percent, now)
		durationMs := decision.RemainingMs(now)
		if !decision.Paused || durationMs <= 0 {
			if resumeErr := r.runtime.ResumeAccountUsage(ctx, row.ID); resumeErr != nil {
				result.Failed++
				r.warn(row.ID, "reconcile resume usage pause failed", resumeErr)
				continue
			}
			result.Resumed++
			continue
		}
		if _, pauseErr := r.runtime.PauseAccountUsage(ctx, row.ID, durationMs, breakerstore.AccountUsageWindow(decision.Window)); pauseErr != nil {
			result.Failed++
			r.warn(row.ID, "reconcile pause usage failed", pauseErr)
			continue
		}
		result.Paused++
	}
	logging.Info(r.logger, "runtime", "account", "usage pause reconciled",
		zap.Int64("channel_id", scope.ChannelID),
		zap.Int64("account_id", scope.AccountID),
		zap.Int32("global_threshold_percent", global),
		zap.Int("scanned", result.Scanned),
		zap.Int("paused", result.Paused),
		zap.Int("resumed", result.Resumed),
		zap.Int("skipped", result.Skipped),
		zap.Int("failed", result.Failed),
	)
	return result, nil
}

func (r *Reconciler) globalThreshold(ctx context.Context) int32 {
	if r.thresholdFn != nil {
		if v := r.thresholdFn(ctx); accountusage.ValidThreshold(v) {
			return v
		}
	}
	return accountusage.DefaultThresholdPercent
}

func (r *Reconciler) warn(accountID int64, message string, err error) {
	logging.Warn(r.logger, "runtime", "account", message,
		zap.Int64("account_id", accountID),
		zap.String("error_message", err.Error()),
	)
}

func optionalInt8(v int64) pgtype.Int8 {
	if v <= 0 {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: v, Valid: true}
}
