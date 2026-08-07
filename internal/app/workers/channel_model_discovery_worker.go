package workers

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/service/appsettings"
)

// ChannelModelDiscoveryExecutor 提供模型发现 worker 的任务调度与单任务执行能力。
type ChannelModelDiscoveryExecutor interface {
	EnqueueDueDiscoveries(ctx context.Context) (int, error)
	ExecuteNextDiscovery(ctx context.Context) (bool, error)
}

// ChannelModelDiscoveryWorker 周期创建到期任务，并优先消费手工/setup/定时发现队列。
type ChannelModelDiscoveryWorker struct {
	executor  ChannelModelDiscoveryExecutor
	settings  *appsettings.SettingsStore
	logger    *zap.Logger
	now       func() time.Time
	nextSweep time.Time
}

func NewChannelModelDiscoveryWorker(
	executor ChannelModelDiscoveryExecutor,
	settings *appsettings.SettingsStore,
	logger *zap.Logger,
) *ChannelModelDiscoveryWorker {
	if executor == nil {
		panic("workers: channel model discovery executor is required")
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &ChannelModelDiscoveryWorker{executor: executor, settings: settings, logger: logger, now: time.Now}
}

func (w *ChannelModelDiscoveryWorker) Name() string { return "channel_model_discovery" }

func (w *ChannelModelDiscoveryWorker) RunOnce(ctx context.Context) (bool, error) {
	settings := appsettings.AdminBackendChannelModelDiscovery(ctx, w.settings)
	if settings.Enabled && !w.now().Before(w.nextSweep) {
		enqueued, err := w.executor.EnqueueDueDiscoveries(ctx)
		if err != nil {
			return false, err
		}
		w.nextSweep = w.now().Add(time.Minute)
		if enqueued > 0 {
			w.logger.Info("scheduled channel model discoveries", zap.Int("count", enqueued))
		}
	}
	return w.executor.ExecuteNextDiscovery(ctx)
}

var _ Unit = (*ChannelModelDiscoveryWorker)(nil)
