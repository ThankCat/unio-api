package workers

import (
	"context"
	"log/slog"
	"time"

	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
)

// ChannelTestStore 定义渠道自动检测 worker 所需的存储能力。
type ChannelTestStore interface {
	// ListChannelsForCredentialTest 返回所有启用渠道（失效的排在前面优先复检）。
	ListChannelsForCredentialTest(ctx context.Context) ([]sqlc.Channel, error)
	// DeleteChannelTestLogsBeyondPerChannel 每渠道保留最近 keep 条检测日志，删更旧的（R1）。
	DeleteChannelTestLogsBeyondPerChannel(ctx context.Context, arg sqlc.DeleteChannelTestLogsBeyondPerChannelParams) (int64, error)
}

// ChannelCredentialTester 对单个渠道执行一次自动检测（source=worker）。
// 内部完成探测 + 翻 credential_valid + 写检测日志；worker 只关心执行是否出错。
type ChannelCredentialTester interface {
	TestChannel(ctx context.Context, channelID int64) error
}

// ChannelTestWorker 周期性对所有启用渠道跑合成检测，据此翻 credential_valid（凭据失效自动摘除、
// 检测通过自动恢复），并按保留策略清理每渠道的检测日志。
//
// 调度：以「轮」为单位，每 interval 起一轮；一轮内把所有渠道排进队列，之后每次 RunOnce 只处理队首
// 一个渠道——避免单次 RunOnce 因慢探测（最长 ~15s）长时间阻塞 runner 上的其它 worker（结算补偿等）。
// 进程内游标，重启后自然从下一轮重新开始（检测是幂等遥测，无需持久进度）。
type ChannelTestWorker struct {
	store     ChannelTestStore
	tester    ChannelCredentialTester
	logger    *slog.Logger
	interval  time.Duration
	retention int32
	now       func() time.Time

	nextCycleAt time.Time
	queue       []int64
}

// NewChannelTestWorker 创建渠道自动检测 worker。interval<=0 兜底 30m；retention<=0 兜底 200。
func NewChannelTestWorker(store ChannelTestStore, tester ChannelCredentialTester, logger *slog.Logger, interval time.Duration, retention int) *ChannelTestWorker {
	if store == nil {
		panic("workers: channel test store is required")
	}
	if tester == nil {
		panic("workers: channel credential tester is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	if interval <= 0 {
		interval = 30 * time.Minute
	}
	if retention <= 0 {
		retention = 200
	}

	return &ChannelTestWorker{
		store:     store,
		tester:    tester,
		logger:    logger,
		interval:  interval,
		retention: int32(retention),
		now:       time.Now,
	}
}

// Name 返回 worker 名称。
func (w *ChannelTestWorker) Name() string {
	return "channel_test"
}

// RunOnce 处理当前巡检轮的一个渠道；队列空且未到下一轮时空转。
func (w *ChannelTestWorker) RunOnce(ctx context.Context) (bool, error) {
	if len(w.queue) == 0 {
		now := w.now()
		if now.Before(w.nextCycleAt) {
			return false, nil
		}

		channels, err := w.store.ListChannelsForCredentialTest(ctx)
		if err != nil {
			return false, err
		}
		w.nextCycleAt = now.Add(w.interval)
		if len(channels) == 0 {
			return false, nil
		}

		w.queue = make([]int64, 0, len(channels))
		for _, ch := range channels {
			w.queue = append(w.queue, ch.ID)
		}
	}

	channelID := w.queue[0]
	w.queue = w.queue[1:]

	if err := w.tester.TestChannel(ctx, channelID); err != nil {
		args := append([]any{"worker", w.Name(), "channel_id", channelID}, failure.LogArgs(err)...)
		w.logger.Warn("channel auto-test execution failed", args...)
	}

	if _, err := w.store.DeleteChannelTestLogsBeyondPerChannel(ctx, sqlc.DeleteChannelTestLogsBeyondPerChannelParams{
		ChannelID: channelID,
		Keep:      w.retention,
	}); err != nil {
		w.logger.Warn("prune channel test logs failed", "worker", w.Name(), "channel_id", channelID, "error", err.Error())
	}

	return true, nil
}
