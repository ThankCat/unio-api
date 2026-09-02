// Package health 消费池型渠道成功传输后的账号观测（第五节：用量反馈与自动暂停）。
//
// 三件事，全部 best-effort（失败只记日志，绝不影响客户交付）：
//  1. 把 x-codex-* 用量窗口快照写回 subscription_accounts.usage_snapshot（供运维水位展示与调度过滤）；
//  2. 更新 last_success_at（池内 LRU 选号的排序依据）;
//  3. 水位达阈值时把账号提前移出调度（PauseAccountUsage，到期时刻 = 窗口重置时刻），
//     低于阈值或重置时刻提前（付费即时重置）时按覆盖语义写入新到期，自动恢复。
package health

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/logging"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

// DefaultUsagePauseThresholdPercent 是用量自动暂停的默认水位阈值。
const DefaultUsagePauseThresholdPercent = 90.0

// Queries 是账号观测落库所需的最小查询集。
type Queries interface {
	UpdateAccountUsageSnapshot(ctx context.Context, arg sqlc.UpdateAccountUsageSnapshotParams) error
	TouchAccountLastSuccess(ctx context.Context, arg sqlc.TouchAccountLastSuccessParams) error
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
	threshold float64
	now       func() time.Time
}

// NewRecorder 创建账号观测记录器。thresholdPercent <= 0 时使用默认 90。
func NewRecorder(queries Queries, runtime RuntimeStore, logger *zap.Logger, thresholdPercent float64) *Recorder {
	if logger == nil {
		logger = zap.NewNop()
	}
	if thresholdPercent <= 0 {
		thresholdPercent = DefaultUsagePauseThresholdPercent
	}
	return &Recorder{
		queries: queries, runtime: runtime, logger: logger,
		threshold: thresholdPercent, now: time.Now,
	}
}

// usageSnapshotDoc 是 usage_snapshot 列的持久化形态（迁移 000069 注释约定的 schema）。
type usageSnapshotDoc struct {
	Primary    *usageWindowDoc `json:"primary,omitempty"`
	Secondary  *usageWindowDoc `json:"secondary,omitempty"`
	PlanType   string          `json:"plan_type,omitempty"`
	CapturedAt time.Time       `json:"captured_at"`
}

type usageWindowDoc struct {
	UsedPercent   float64 `json:"used_percent"`
	WindowMinutes int64   `json:"window_minutes,omitempty"`
	ResetAt       int64   `json:"reset_at,omitempty"`
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

	if r.queries != nil {
		doc := usageSnapshotDoc{PlanType: usage.PlanType, CapturedAt: now.UTC()}
		if usage.Primary.Present {
			doc.Primary = &usageWindowDoc{
				UsedPercent: usage.Primary.UsedPercent, WindowMinutes: usage.Primary.WindowMinutes,
				ResetAt: usage.Primary.ResetAtUnix,
			}
		}
		if usage.Secondary.Present {
			doc.Secondary = &usageWindowDoc{
				UsedPercent: usage.Secondary.UsedPercent, WindowMinutes: usage.Secondary.WindowMinutes,
				ResetAt: usage.Secondary.ResetAtUnix,
			}
		}
		raw, err := json.Marshal(doc)
		if err == nil {
			err = r.queries.UpdateAccountUsageSnapshot(ctx, sqlc.UpdateAccountUsageSnapshotParams{
				ID: accountID, UsageSnapshot: raw,
			})
		}
		if err != nil {
			r.warn(ctx, accountID, "update usage snapshot failed", err)
		}
	}

	r.applyUsagePause(ctx, accountID, usage, now)
}

// applyUsagePause 按阈值决定暂停或恢复。两个易漏边界（官方核对表）：
//   - 付费即时重置会让 reset_at 提前：暂停用覆盖语义（PauseAccountUsage），新观测直接改写到期时刻；
//   - 任一窗口可能缺失：缺失视为不限，不得按 0% 或 100% 臆断。
func (r *Recorder) applyUsagePause(ctx context.Context, accountID int64, usage *adapter.AccountUsageFacts, now time.Time) {
	if r.runtime == nil {
		return
	}
	window, over := r.exceededWindow(usage, now)
	if !over {
		// 快照回落阈值之下（或窗口已重置）：显式恢复，不等旧暂停自然到期——
		// 付费即时重置的账号应当立即回到调度。
		if err := r.runtime.ResumeAccountUsage(ctx, accountID); err != nil {
			r.warn(ctx, accountID, "resume usage pause failed", err)
		}
		return
	}
	durationMs := window.durationMs(now)
	if durationMs <= 0 {
		return
	}
	if _, err := r.runtime.PauseAccountUsage(ctx, accountID, durationMs, window.name); err != nil {
		r.warn(ctx, accountID, "pause usage failed", err)
		return
	}
	logging.Warn(r.logger, "runtime", "account", "account usage paused",
		zap.Int64("account_id", accountID),
		zap.String("window", string(window.name)),
		zap.Float64("used_percent", window.usedPercent),
		zap.Int64("duration_ms", durationMs),
	)
}

type exceeded struct {
	name        breakerstore.AccountUsageWindow
	usedPercent float64
	resetAtUnix int64
}

func (e exceeded) durationMs(now time.Time) int64 {
	if e.resetAtUnix <= 0 {
		return 0
	}
	return time.Unix(e.resetAtUnix, 0).Sub(now).Milliseconds()
}

// exceededWindow 返回触顶的窗口（primary 优先：它先重置，暂停代价最小）。
func (r *Recorder) exceededWindow(usage *adapter.AccountUsageFacts, now time.Time) (exceeded, bool) {
	windows := []struct {
		name  breakerstore.AccountUsageWindow
		facts adapter.AccountUsageWindowFacts
	}{
		{breakerstore.AccountUsageWindowPrimary, usage.Primary},
		{breakerstore.AccountUsageWindowSecondary, usage.Secondary},
	}
	for _, w := range windows {
		if !w.facts.Present || w.facts.UsedPercent < r.threshold {
			continue
		}
		resetAt := w.facts.ResetAtUnix
		if resetAt <= 0 && w.facts.ResetAfterSeconds > 0 {
			resetAt = now.Unix() + w.facts.ResetAfterSeconds
		}
		if resetAt <= now.Unix() {
			// 窗口标称已重置却仍报高水位：观测自相矛盾，不暂停（宁多发一个请求，不锁死账号）。
			continue
		}
		return exceeded{name: w.name, usedPercent: w.facts.UsedPercent, resetAtUnix: resetAt}, true
	}
	return exceeded{}, false
}

func (r *Recorder) warn(_ context.Context, accountID int64, message string, err error) {
	logging.Warn(r.logger, "runtime", "account", message,
		zap.Int64("account_id", accountID),
		zap.String("error_message", err.Error()),
	)
}
