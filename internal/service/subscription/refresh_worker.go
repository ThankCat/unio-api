package subscription

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/platform/logging"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

// 后台令牌保活（第六节）：定时扫描将过期账号逐个刷新。
// 每账号的分布式锁与失败处置（退避→临时不可调度→确认吊销才禁用）都在 Outbound.RefreshAccount /
// refreshWithLock 里，worker 只负责「找到该刷的号、按限速逐个刷」。

// RefreshWorkerOptions 是保活扫描的运行参数。
type RefreshWorkerOptions struct {
	// Interval 是扫描周期；默认 5 分钟。
	Interval time.Duration
	// RefreshWithin 是「将过期」的判定窗口；默认 1 小时（与沙箱 token.py 同阈值）。
	RefreshWithin time.Duration
	// PageLimit 是单轮扫描上限；默认 50。
	PageLimit int32
	// PerAccountDelay 是逐账号刷新之间的间隔（每平台限速）；默认 2 秒。
	PerAccountDelay time.Duration
}

func (o RefreshWorkerOptions) withDefaults() RefreshWorkerOptions {
	if o.Interval <= 0 {
		o.Interval = 5 * time.Minute
	}
	if o.RefreshWithin <= 0 {
		o.RefreshWithin = time.Hour
	}
	if o.PageLimit <= 0 {
		o.PageLimit = 50
	}
	if o.PerAccountDelay <= 0 {
		o.PerAccountDelay = 2 * time.Second
	}
	return o
}

// RefreshWorkerQueries 是保活扫描所需的最小查询集。
type RefreshWorkerQueries interface {
	ListAccountsNeedingTokenRefresh(ctx context.Context, arg sqlc.ListAccountsNeedingTokenRefreshParams) ([]sqlc.ListAccountsNeedingTokenRefreshRow, error)
}

// RefreshWorker 周期性保活账号令牌（实现 workers.Unit 形态：RunOnce 由 runner 调度）。
type RefreshWorker struct {
	queries  RefreshWorkerQueries
	outbound *Outbound
	logger   *zap.Logger
	options  RefreshWorkerOptions
	now      func() time.Time

	nextSweep time.Time
}

// NewRefreshWorker 创建保活 worker。
func NewRefreshWorker(queries RefreshWorkerQueries, outbound *Outbound, logger *zap.Logger, options RefreshWorkerOptions) *RefreshWorker {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &RefreshWorker{
		queries: queries, outbound: outbound, logger: logger,
		options: options.withDefaults(), now: time.Now,
	}
}

// Name 实现 workers.Unit。
func (w *RefreshWorker) Name() string { return "subscription_token_refresh" }

// RunOnce 到达周期即扫描一轮将过期账号并逐个刷新；未到周期直接返回。
func (w *RefreshWorker) RunOnce(ctx context.Context) (bool, error) {
	if w.now().Before(w.nextSweep) {
		return false, nil
	}
	w.nextSweep = w.now().Add(w.options.Interval)

	rows, err := w.queries.ListAccountsNeedingTokenRefresh(ctx, sqlc.ListAccountsNeedingTokenRefreshParams{
		WithinSeconds: int64(w.options.RefreshWithin / time.Second),
		PageLimit:     w.options.PageLimit,
	})
	if err != nil {
		return false, err
	}
	if len(rows) == 0 {
		return false, nil
	}
	logging.Info(w.logger, "subscription", "refresh", "refresh sweep started",
		zap.Int("accounts", len(rows)))
	for index, row := range rows {
		if ctx.Err() != nil {
			return true, nil
		}
		// refreshWithLock 内部完成：分布式锁、明确拒绝→禁用、网络失败→临时不可调度、成功→写回+清隔离。
		if _, err := w.outbound.refreshWithLock(ctx, row.ID, textOrEmpty(row.ProxyUrl)); err != nil {
			logging.Warn(w.logger, "subscription", "refresh", "background refresh failed",
				zap.Int64("account_id", row.ID), zap.String("error_message", err.Error()))
		}
		if index+1 < len(rows) {
			select {
			case <-ctx.Done():
				return true, nil
			case <-time.After(w.options.PerAccountDelay):
			}
		}
	}
	return true, nil
}
