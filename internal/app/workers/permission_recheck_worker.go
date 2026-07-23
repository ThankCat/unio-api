package workers

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/service/admin/channeltest"
	"github.com/ThankCat/unio-gateway/internal/service/appsettings"
)

const (
	permissionRecheckLeasePadding      = 30 * time.Second
	permissionRecheckInitialBackoff    = 30 * time.Second
	permissionRecheckMaximumBackoff    = 15 * time.Minute
	permissionRecheckCompletionTimeout = 2 * time.Second
)

// PermissionRecheckStore 是多 worker 共享的 Redis 领取/收口契约。
type PermissionRecheckStore interface {
	ClaimPermissionRecheck(context.Context, string, time.Duration) (*breakerstore.PermissionRecheckTask, error)
	CompletePermissionRecheck(context.Context, breakerstore.PermissionRecheckTask, breakerstore.PermissionRecheckOutcome, time.Duration) (breakerstore.PermissionRecheckDisposition, error)
}

// ChannelPermissionRechecker 复用 channeltest adapter 链路执行指定内部 model_id 的真实探测。
type ChannelPermissionRechecker interface {
	RecheckPermission(context.Context, channeltest.PermissionRecheckInput) (channeltest.PermissionRecheckResult, error)
}

// PermissionRecheckWorker 消费 403 Channel-Model 复检队列。每次 RunOnce 最多领取并处理一个任务；
// Redis 租约保证多个 worker-server 不会在租约内重复探测，失败按有上限的指数退避重新排队。
type PermissionRecheckWorker struct {
	store     PermissionRecheckStore
	rechecker ChannelPermissionRechecker
	settings  *appsettings.SettingsStore
	workerID  string
	logger    *zap.Logger
	backoff   func(int64) time.Duration
}

func NewPermissionRecheckWorker(
	store PermissionRecheckStore,
	rechecker ChannelPermissionRechecker,
	settings *appsettings.SettingsStore,
	workerID string,
	logger *zap.Logger,
) *PermissionRecheckWorker {
	if store == nil {
		panic("workers: permission recheck store is required")
	}
	if rechecker == nil {
		panic("workers: channel permission rechecker is required")
	}
	if workerID == "" {
		panic("workers: permission recheck worker id is required")
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &PermissionRecheckWorker{
		store: store, rechecker: rechecker, settings: settings, workerID: workerID,
		logger: logger, backoff: permissionRecheckBackoff,
	}
}

func (w *PermissionRecheckWorker) Name() string {
	return "permission_recheck"
}

func (w *PermissionRecheckWorker) RunOnce(ctx context.Context) (bool, error) {
	probeTimeout := appsettings.AdminBackendChannelTestProbeTimeout(ctx, w.settings)
	lease := probeTimeout + permissionRecheckLeasePadding
	task, err := w.store.ClaimPermissionRecheck(ctx, w.workerID, lease)
	if err != nil {
		return false, err
	}
	if task == nil {
		return false, nil
	}

	result, recheckErr := w.rechecker.RecheckPermission(ctx, channeltest.PermissionRecheckInput{
		ChannelID: task.ChannelID, ModelID: task.ModelID,
		ChannelConfigRevision:   task.ChannelConfigRevision,
		EndpointBaseURLRevision: task.EndpointBaseURLRevision,
		EndpointStatusRevision:  task.EndpointStatusRevision,
	})
	outcome := breakerstore.PermissionRecheckFailed
	retryAfter := w.backoff(task.Attempt)
	if recheckErr == nil {
		switch {
		case result.Stale:
			outcome = breakerstore.PermissionRecheckStale
			retryAfter = 0
		case result.Probe.Success:
			outcome = breakerstore.PermissionRecheckSucceeded
			retryAfter = 0
		}
	}

	completeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), permissionRecheckCompletionTimeout)
	disposition, completeErr := w.store.CompletePermissionRecheck(completeCtx, *task, outcome, retryAfter)
	cancel()
	if completeErr != nil {
		return true, completeErr
	}

	fields := []zap.Field{
		zap.String("worker", w.Name()),
		zap.Int64("channel_id", task.ChannelID),
		zap.Int64("model_id", task.ModelID),
		zap.Int64("config_revision", task.ChannelConfigRevision),
		zap.Int64("endpoint_base_url_revision", task.EndpointBaseURLRevision),
		zap.Int64("endpoint_status_revision", task.EndpointStatusRevision),
		zap.Int64("recheck_attempt", task.Attempt),
		zap.String("outcome", string(outcome)),
		zap.String("disposition", string(disposition)),
	}
	if recheckErr != nil {
		fields = append(fields, failure.LogFields(recheckErr)...)
		w.logger.Warn("channel-model permission recheck execution failed", fields...)
	} else if outcome != breakerstore.PermissionRecheckSucceeded {
		w.logger.Info("channel-model permission recheck completed", fields...)
	}
	return true, nil
}

func permissionRecheckBackoff(attempt int64) time.Duration {
	if attempt <= 1 {
		return permissionRecheckInitialBackoff
	}
	backoff := permissionRecheckInitialBackoff
	for n := int64(1); n < attempt && backoff < permissionRecheckMaximumBackoff; n++ {
		backoff *= 2
		if backoff >= permissionRecheckMaximumBackoff {
			return permissionRecheckMaximumBackoff
		}
	}
	return backoff
}
