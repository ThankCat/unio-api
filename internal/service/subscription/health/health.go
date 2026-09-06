// Package health 消费池型渠道成功传输后的账号观测（第五节：用量反馈与自动暂停）。
//
// 三件事，全部 best-effort（失败只记日志，绝不影响客户交付）：
//  1. 把 x-codex-* 用量窗口快照写回 subscription_accounts.usage_snapshot（供运维水位展示与调度过滤）；
//  2. 更新 last_success_at（池内 LRU 选号的排序依据）;
//  3. 水位达生效阈值时把账号提前移出调度（PauseAccountUsage，到期时刻 = 窗口重置时刻），
//     低于阈值或重置时刻提前（付费即时重置）时按覆盖语义写入新到期，自动恢复。
//
// 阈值按「账号 → 池型渠道 → 全局 setting」三层继承（core/accountusage）。调度侧的拦截按快照 + 生效阈值
// 实时判定，这里写的 Redis usage_pause 标记只是给管理端看的展示缓存；Reconciler 在任一层阈值变更后
// 按同一规则重算这些标记。
package health

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/core/accountusage"
	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/logging"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

// Queries 是账号观测落库所需的最小查询集。
type Queries interface {
	UpdateAccountUsageSnapshot(ctx context.Context, arg sqlc.UpdateAccountUsageSnapshotParams) error
	TouchAccountLastSuccess(ctx context.Context, arg sqlc.TouchAccountLastSuccessParams) error
	// GetAccountUsagePausePolicy 取账号与渠道两层阈值覆写，供观测时解析生效阈值。
	GetAccountUsagePausePolicy(ctx context.Context, id int64) (sqlc.GetAccountUsagePausePolicyRow, error)
}

// RuntimeStore 是账号运行态写入所需的最小 Redis 能力。
type RuntimeStore interface {
	PauseAccountUsage(ctx context.Context, accountID, durationMs int64, window breakerstore.AccountUsageWindow) (int64, error)
	ResumeAccountUsage(ctx context.Context, accountID int64) error
}

// Recorder 实现 lifecycle.AccountHealthSink。
type Recorder struct {
	queries   Queries
	runtime   RuntimeStore
	logger    *zap.Logger
	threshold int32
	// thresholdFn 优先于静态 threshold：接 appsettings 后全局阈值可热更新，
	// 每次用量观测现读（观测频率 = 请求频率，读的是内存缓存的设置快照，无额外 IO）。
	thresholdFn func(ctx context.Context) int32
	now         func() time.Time
}

// NewRecorder 创建账号观测记录器。thresholdPercent 不在 1~100 时使用代码默认 90。
func NewRecorder(queries Queries, runtime RuntimeStore, logger *zap.Logger, thresholdPercent int32) *Recorder {
	if logger == nil {
		logger = zap.NewNop()
	}
	if !accountusage.ValidThreshold(thresholdPercent) {
		thresholdPercent = accountusage.DefaultThresholdPercent
	}
	return &Recorder{
		queries: queries, runtime: runtime, logger: logger,
		threshold: thresholdPercent, now: time.Now,
	}
}

// WithThresholdProvider 注入全局阈值热读取（appsettings）；fn 返回值不在 1~100 时回落静态默认。
func (r *Recorder) WithThresholdProvider(fn func(ctx context.Context) int32) *Recorder {
	r.thresholdFn = fn
	return r
}

// globalThreshold 取本次观测生效的全局阈值。
func (r *Recorder) globalThreshold(ctx context.Context) int32 {
	if r.thresholdFn != nil {
		if v := r.thresholdFn(ctx); accountusage.ValidThreshold(v) {
			return v
		}
	}
	return r.threshold
}

// thresholdFor 解析账号的生效阈值（账号 → 渠道 → 全局）。两层覆写读不到时退回全局并记 warn：
// 观测是 best-effort，不能因为一次读库失败就放弃暂停评估。
func (r *Recorder) thresholdFor(ctx context.Context, accountID int64) accountusage.Threshold {
	global := r.globalThreshold(ctx)
	if r.queries == nil {
		return accountusage.ResolveThreshold(nil, nil, global)
	}
	policy, err := r.queries.GetAccountUsagePausePolicy(ctx, accountID)
	if err != nil {
		r.warn(ctx, accountID, "load usage pause policy failed", err)
		return accountusage.ResolveThreshold(nil, nil, global)
	}
	return accountusage.ResolveThreshold(
		thresholdOverride(policy.AccountThresholdPercent),
		thresholdOverride(policy.ChannelThresholdPercent),
		global,
	)
}

// thresholdOverride 把可空的阈值列还原为 *int32（NULL → nil，表示继承上一层）。
func thresholdOverride(v pgtype.Int4) *int32 {
	if !v.Valid {
		return nil
	}
	percent := v.Int32
	return &percent
}

// RecordAccountSuccess 消费一次成功传输的账号观测。usage 为 nil 时只更新 LRU 时间。
func (r *Recorder) RecordAccountSuccess(ctx context.Context, accountID int64, usage *adapter.AccountUsageFacts) {
	if r == nil || accountID <= 0 {
		return
	}
	now := r.now()
	if r.queries != nil {
		if err := r.queries.TouchAccountLastSuccess(ctx, sqlc.TouchAccountLastSuccessParams{
			ID: accountID, LastSuccessAt: pgtype.Timestamptz{Time: now, Valid: true},
		}); err != nil {
			r.warn(ctx, accountID, "touch last success failed", err)
		}
	}
	if usage == nil {
		return
	}
	snapshot := snapshotFromFacts(usage, now)
	r.persistUsageSnapshot(ctx, accountID, snapshot)
	r.applyUsagePause(ctx, accountID, snapshot, now)
}

// RecordAccountUsageObservation 只回写用量观测（快照落库 + 阈值暂停评估），不 touch LRU——
// 观测来自失败响应（429 的 x-codex 头照样携带全量水位），不代表一次成功服务。
func (r *Recorder) RecordAccountUsageObservation(ctx context.Context, accountID int64, usage *adapter.AccountUsageFacts) {
	if r == nil || accountID <= 0 || usage == nil {
		return
	}
	now := r.now()
	snapshot := snapshotFromFacts(usage, now)
	r.persistUsageSnapshot(ctx, accountID, snapshot)
	r.applyUsagePause(ctx, accountID, snapshot, now)
}

// snapshotFromFacts 把上游观测转成持久化快照。重置时刻统一落成绝对时刻：上游只给相对秒数时按观测时刻换算，
// 否则调度侧按快照实时判定时拿不到窗口何时重置，会把高水位误判为「已重置」。
func snapshotFromFacts(usage *adapter.AccountUsageFacts, now time.Time) accountusage.Snapshot {
	snapshot := accountusage.Snapshot{PlanType: usage.PlanType, CapturedAt: now.UTC()}
	if usage.Primary.Present {
		snapshot.Primary = windowFromFacts(usage.Primary, now)
	}
	if usage.Secondary.Present {
		snapshot.Secondary = windowFromFacts(usage.Secondary, now)
	}
	return snapshot
}

func windowFromFacts(facts adapter.AccountUsageWindowFacts, now time.Time) *accountusage.Window {
	resetAt := facts.ResetAtUnix
	if resetAt <= 0 && facts.ResetAfterSeconds > 0 {
		resetAt = now.Unix() + facts.ResetAfterSeconds
	}
	return &accountusage.Window{
		UsedPercent:   facts.UsedPercent,
		WindowMinutes: facts.WindowMinutes,
		ResetAt:       resetAt,
	}
}

// persistUsageSnapshot 把用量观测写进 subscription_accounts.usage_snapshot（成功/失败观测共用）。
func (r *Recorder) persistUsageSnapshot(ctx context.Context, accountID int64, snapshot accountusage.Snapshot) {
	if r.queries == nil {
		return
	}
	raw, err := json.Marshal(snapshot)
	if err == nil {
		err = r.queries.UpdateAccountUsageSnapshot(ctx, sqlc.UpdateAccountUsageSnapshotParams{
			ID: accountID, UsageSnapshot: raw,
		})
	}
	if err != nil {
		r.warn(ctx, accountID, "update usage snapshot failed", err)
	}
}

// applyUsagePause 按生效阈值决定暂停或恢复。两个易漏边界（官方核对表）：
//   - 付费即时重置会让 reset_at 提前：暂停用覆盖语义（PauseAccountUsage），新观测直接改写到期时刻；
//   - 任一窗口可能缺失：缺失视为不限，不得按 0% 或 100% 臆断。
func (r *Recorder) applyUsagePause(ctx context.Context, accountID int64, snapshot accountusage.Snapshot, now time.Time) {
	if r.runtime == nil {
		return
	}
	threshold := r.thresholdFor(ctx, accountID)
	decision := accountusage.Evaluate(snapshot, threshold.Percent, now)
	if !decision.Paused {
		// 快照回落阈值之下（或窗口已重置）：显式恢复，不等旧暂停自然到期——
		// 付费即时重置的账号应当立即回到调度。
		if err := r.runtime.ResumeAccountUsage(ctx, accountID); err != nil {
			r.warn(ctx, accountID, "resume usage pause failed", err)
		}
		return
	}
	durationMs := decision.RemainingMs(now)
	if durationMs <= 0 {
		return
	}
	if _, err := r.runtime.PauseAccountUsage(ctx, accountID, durationMs, breakerstore.AccountUsageWindow(decision.Window)); err != nil {
		r.warn(ctx, accountID, "pause usage failed", err)
		return
	}
	logging.Warn(r.logger, "runtime", "account", "account usage paused",
		zap.Int64("account_id", accountID),
		zap.String("window", string(decision.Window)),
		zap.Float64("used_percent", decision.UsedPercent),
		zap.Int32("threshold_percent", threshold.Percent),
		zap.String("threshold_source", string(threshold.Source)),
		zap.Int64("duration_ms", durationMs),
	)
}

func (r *Recorder) warn(_ context.Context, accountID int64, message string, err error) {
	logging.Warn(r.logger, "runtime", "account", message,
		zap.Int64("account_id", accountID),
		zap.String("error_message", err.Error()),
	)
}
