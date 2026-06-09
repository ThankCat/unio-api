package workers

import (
	"context"
	"log/slog"
	"time"

	"github.com/ThankCat/unio-api/internal/core/capability"
	"github.com/ThankCat/unio-api/internal/core/modelcatalog"
	"github.com/ThankCat/unio-api/internal/platform/failure"
)

const (
	// modelCatalogSyncFailureAlertThreshold 是触发连续失败告警的阈值（TASK-12.04）。
	modelCatalogSyncFailureAlertThreshold = 3
	// modelCatalogSyncDefaultPollInterval 限制 due 检查频率，避免空闲时高频查库。
	modelCatalogSyncDefaultPollInterval = time.Minute
	modelCatalogSyncMaxRetryBackoff     = time.Hour
	modelCatalogSyncBaseRetryBackoff    = 5 * time.Minute
)

// ModelCatalogSyncer 抽象一次 models.dev 同步执行（由 core/modelcatalog.Syncer 实现）。
type ModelCatalogSyncer interface {
	Sync(ctx context.Context, opts modelcatalog.Options) (modelcatalog.Result, error)
}

// ModelCatalogSyncWorker 按间隔调度 models.dev 同步：到期才跑、失败退避、连续失败告警。
//
// 调度以 model_capability_sync_jobs 最近一次任务为准（跨实例/重启幂等）：
// 距上次成功不足 interval 则跳过；上次失败按退避重试；连续 3 次失败发出告警日志。
type ModelCatalogSyncWorker struct {
	syncer   ModelCatalogSyncer
	store    modelcatalog.SyncStore
	logger   *slog.Logger
	interval time.Duration
	now      func() time.Time

	nextPollAt          time.Time
	retryNotBefore      time.Time
	consecutiveFailures int
}

// NewModelCatalogSyncWorker 创建 models.dev 同步 worker。
func NewModelCatalogSyncWorker(syncer ModelCatalogSyncer, store modelcatalog.SyncStore, logger *slog.Logger, interval time.Duration) *ModelCatalogSyncWorker {
	if syncer == nil {
		panic("workers: model catalog syncer is required")
	}
	if store == nil {
		panic("workers: model catalog store is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	if interval <= 0 {
		interval = 24 * time.Hour
	}

	return &ModelCatalogSyncWorker{
		syncer:   syncer,
		store:    store,
		logger:   logger,
		interval: interval,
		now:      time.Now,
	}
}

// Name 返回 worker 名称。
func (w *ModelCatalogSyncWorker) Name() string {
	return "model_catalog_sync"
}

// RunOnce 在到期时执行一次 models.dev 同步。
func (w *ModelCatalogSyncWorker) RunOnce(ctx context.Context) (bool, error) {
	now := w.now()

	// poll 节流：空闲期不必每秒查库。
	if now.Before(w.nextPollAt) {
		return false, nil
	}
	w.nextPollAt = now.Add(modelCatalogSyncDefaultPollInterval)

	// 失败退避：上次失败后未到重试时间则跳过。
	if now.Before(w.retryNotBefore) {
		return false, nil
	}

	due, err := w.due(ctx, now)
	if err != nil {
		return false, err
	}
	if !due {
		return false, nil
	}

	if _, err := w.syncer.Sync(ctx, modelcatalog.Options{}); err != nil {
		w.consecutiveFailures++
		w.retryNotBefore = now.Add(w.retryBackoff())

		args := append([]any{"worker", w.Name(), "consecutive_failures", w.consecutiveFailures}, failure.LogArgs(err)...)
		if w.consecutiveFailures >= modelCatalogSyncFailureAlertThreshold {
			// 连续失败告警：以稳定 alert 字段标记，供日志告警管线挂钩。
			args = append(args, "alert", "model_catalog_sync_consecutive_failures")
			w.logger.Error("model catalog sync repeated failure", args...)
		} else {
			w.logger.Warn("model catalog sync failed", args...)
		}

		return true, nil
	}

	w.consecutiveFailures = 0
	w.retryNotBefore = time.Time{}

	return true, nil
}

// due 依据最近一次同步任务判断当前是否应该执行。
func (w *ModelCatalogSyncWorker) due(ctx context.Context, now time.Time) (bool, error) {
	latest, err := w.store.LatestSyncJob(ctx)
	if err != nil {
		return false, err
	}
	if !latest.Found {
		return true, nil
	}

	switch latest.Status {
	case capability.SyncJobStatusRunning, capability.SyncJobStatusPending:
		// 另一实例或上一轮仍在进行，跳过本轮。
		return false, nil
	case capability.SyncJobStatusSucceeded:
		if latest.FinishedAt != nil && now.Sub(*latest.FinishedAt) < w.interval {
			return false, nil
		}
		return true, nil
	default:
		// failed 等终态：到期（受失败退避约束）即重试。
		return true, nil
	}
}

func (w *ModelCatalogSyncWorker) retryBackoff() time.Duration {
	shift := w.consecutiveFailures - 1
	if shift < 0 {
		shift = 0
	}
	if shift > 5 {
		shift = 5
	}

	backoff := modelCatalogSyncBaseRetryBackoff * time.Duration(1<<shift)
	if backoff > modelCatalogSyncMaxRetryBackoff {
		backoff = modelCatalogSyncMaxRetryBackoff
	}
	if backoff > w.interval {
		backoff = w.interval
	}

	return backoff
}
