package workers

import (
	"context"
	"log/slog"
	"time"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/appsettings"
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
// 一个渠道——避免单次 RunOnce 因慢探测长时间阻塞 runner 上的其它 worker（结算补偿等）。
// 进程内游标，重启后自然从下一轮重新开始（检测是幂等遥测，无需持久进度）。
//
// 开关 / 间隔 / 日志保留均取自运行时配置 admin_backend.channel_test（系统设置 → 运营判定），
// 每轮现读，约 3s 内热生效；settings 为 nil 时走注册表默认。
type ChannelTestWorker struct {
	store    ChannelTestStore
	tester   ChannelCredentialTester
	settings *appsettings.SettingsStore
	logger   *slog.Logger
	now      func() time.Time

	nextCycleAt time.Time
	queue       []int64
}

// NewChannelTestWorker 创建渠道自动检测 worker。settings 可为 nil（单测走默认）。
func NewChannelTestWorker(
	store ChannelTestStore,
	tester ChannelCredentialTester,
	settings *appsettings.SettingsStore,
	logger *slog.Logger,
) *ChannelTestWorker {
	if store == nil {
		panic("workers: channel test store is required")
	}
	if tester == nil {
		panic("workers: channel credential tester is required")
	}
	if logger == nil {
		logger = slog.Default()
	}

	return &ChannelTestWorker{
		store:    store,
		tester:   tester,
		settings: settings,
		logger:   logger,
		now:      time.Now,
	}
}

// Name 返回 worker 名称。
func (w *ChannelTestWorker) Name() string {
	return "channel_test"
}

// RunOnce 处理当前巡检轮的一个渠道；关闭或队列空且未到下一轮时空转。
func (w *ChannelTestWorker) RunOnce(ctx context.Context) (bool, error) {
	cfg := appsettings.AdminBackendChannelTest(ctx, w.settings)
	if !cfg.Enabled {
		// 关闭：丢弃进行中队列，并把 nextCycleAt 置为现在——重新开启后立刻起一轮。
		w.queue = nil
		w.nextCycleAt = w.now()
		return false, nil
	}

	if len(w.queue) == 0 {
		now := w.now()
		if now.Before(w.nextCycleAt) {
			return false, nil
		}

		channels, err := w.store.ListChannelsForCredentialTest(ctx)
		if err != nil {
			return false, err
		}
		w.nextCycleAt = now.Add(cfg.Interval)
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
		Keep:      int32(cfg.LogRetentionPerChannel),
	}); err != nil {
		w.logger.Warn("prune channel test logs failed", "worker", w.Name(), "channel_id", channelID, "error", err.Error())
	}

	return true, nil
}
