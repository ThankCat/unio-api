package workers

import (
	"context"
	"log/slog"
	"time"

	"github.com/ThankCat/unio-api/internal/core/capability/calibration"
	"github.com/ThankCat/unio-api/internal/platform/failure"
)

const (
	// capabilityCalibrateFailureAlertThreshold 是触发连续失败告警的阈值。
	capabilityCalibrateFailureAlertThreshold = 3
	capabilityCalibrateBaseRetryBackoff      = 5 * time.Minute
	capabilityCalibrateMaxRetryBackoff       = time.Hour
	// capabilityCalibrateLockRetryBackoff 是抢锁失败（另一实例在跑）后的短退避，避免空转刷 DB。
	capabilityCalibrateLockRetryBackoff = time.Minute
	// defaultCapabilityCalibrateLockTTL 是分布式租约默认 TTL（运行中续租，崩溃后据此自动释放）。
	defaultCapabilityCalibrateLockTTL = 10 * time.Minute
)

// CapabilityCalibrator 抽象一次能力自动校正执行（由 core/capability/calibration.Calibrator 实现）。
type CapabilityCalibrator interface {
	Run(ctx context.Context, opts calibration.Options) (calibration.Result, error)
}

// CalibrationLock 抽象能力自动校正的分布式互斥租约（多实例互斥）。
//
// 由 core/capability/calibration.Lease 实现（DB 行锁）。单实例部署可传 nil：worker 退化为纯进程内调度。
// Acquire 返回 false 表示另一实例正持有租约；运行中 worker 周期 Renew 续租，Renew 返回 false 表示租约
// 已丢失（被抢占/过期），worker 应立即中止本轮以免与新持有者并发。
type CalibrationLock interface {
	Acquire(ctx context.Context, ttl time.Duration) (bool, error)
	Renew(ctx context.Context, ttl time.Duration) (bool, error)
	Release(ctx context.Context) error
}

// CapabilityCalibrationWorker 按间隔调度能力自动校正：到期才跑、失败退避、连续失败告警、上游退化告警。
//
// 以进程内 nextRunAt 计时；多实例部署时由 lock（DB 单例租约）保证互斥执行——校正靠 watermark 幂等，
// 但 rollup 计数累加，并发会重复计数（DESIGN 风险 A）。lock 为 nil 时退化为单实例语义。
type CapabilityCalibrationWorker struct {
	calibrator CapabilityCalibrator
	lock       CalibrationLock
	logger     *slog.Logger
	interval   time.Duration
	lockTTL    time.Duration
	now        func() time.Time

	nextRunAt           time.Time
	retryNotBefore      time.Time
	consecutiveFailures int
}

// NewCapabilityCalibrationWorker 创建能力自动校正 worker。lock 为 nil 表示单实例（无分布式互斥）。
func NewCapabilityCalibrationWorker(calibrator CapabilityCalibrator, lock CalibrationLock, logger *slog.Logger, interval, lockTTL time.Duration) *CapabilityCalibrationWorker {
	if calibrator == nil {
		panic("workers: capability calibrator is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	if interval <= 0 {
		interval = 6 * time.Hour
	}
	if lockTTL <= 0 {
		lockTTL = defaultCapabilityCalibrateLockTTL
	}

	return &CapabilityCalibrationWorker{
		calibrator: calibrator,
		lock:       lock,
		logger:     logger,
		interval:   interval,
		lockTTL:    lockTTL,
		now:        time.Now,
	}
}

// Name 返回 worker 名称。
func (w *CapabilityCalibrationWorker) Name() string {
	return "capability_calibration"
}

// RunOnce 在到期时执行一次能力自动校正（多实例下先抢分布式租约）。
func (w *CapabilityCalibrationWorker) RunOnce(ctx context.Context) (bool, error) {
	now := w.now()
	if now.Before(w.nextRunAt) {
		return false, nil
	}
	if now.Before(w.retryNotBefore) {
		return false, nil
	}

	runCtx := ctx
	if w.lock != nil {
		acquired, err := w.lock.Acquire(ctx, w.lockTTL)
		if err != nil {
			// DB 故障：短退避后重试；不前进 nextRunAt（视为本轮未跑成功）。
			w.retryNotBefore = now.Add(w.lockRetryBackoff())
			w.logger.Warn("capability calibration lease acquire failed", failure.LogArgs(err)...)
			return true, nil
		}
		if !acquired {
			// 另一实例正在跑：短退避后再试。校正幂等，错过这轮无碍。
			w.retryNotBefore = now.Add(w.lockRetryBackoff())
			return false, nil
		}

		var cancel context.CancelFunc
		runCtx, cancel = context.WithCancel(ctx)
		stopRenew := w.startRenew(runCtx, cancel)
		defer func() {
			stopRenew()
			cancel()
			w.releaseLock(ctx)
		}()
	}

	result, err := w.calibrator.Run(runCtx, calibration.Options{})
	if err != nil {
		w.consecutiveFailures++
		w.retryNotBefore = now.Add(w.retryBackoff())

		args := append([]any{"worker", w.Name(), "consecutive_failures", w.consecutiveFailures}, failure.LogArgs(err)...)
		if w.consecutiveFailures >= capabilityCalibrateFailureAlertThreshold {
			args = append(args, "alert", "capability_calibration_consecutive_failures")
			w.logger.Error("capability calibration repeated failure", args...)
		} else {
			w.logger.Warn("capability calibration failed", args...)
		}

		return true, nil
	}

	w.consecutiveFailures = 0
	w.retryNotBefore = time.Time{}
	w.nextRunAt = now.Add(w.interval)

	w.logger.Info("capability calibration completed",
		"scanned_attempts", result.ScannedAttempts,
		"auto_applied", len(result.Plan.AutoApply),
		"suggested", len(result.Plan.Suggestions),
		"degradations", len(result.Degradations),
		"max_attempt_id", result.MaxAttemptID,
	)

	w.logDegradations(result.Degradations)

	return true, nil
}

// logDegradations 为每条上游退化候选打告警日志（只告警，不改库；删除能力永远人工）。
func (w *CapabilityCalibrationWorker) logDegradations(degradations []calibration.Degradation) {
	for _, d := range degradations {
		w.logger.Warn("capability upstream degradation suspected",
			"worker", w.Name(),
			"alert", "capability_upstream_degradation",
			"model_id", d.ModelID,
			"capability", string(d.Key),
			"success", d.SuccessCount,
			"evidence_count", d.EvidenceCount,
			"evidence_ratio", d.EvidenceRatio,
			"channel_ids", d.ChannelIDs,
		)
	}
}

// startRenew 启动续租 goroutine：周期 Renew 维持租约；一旦丢锁（被抢占/过期）即 onLost 取消本轮运行。
// 返回的 stop 用于在本轮结束时停止续租。
func (w *CapabilityCalibrationWorker) startRenew(ctx context.Context, onLost context.CancelFunc) func() {
	interval := w.lockTTL / 3
	if interval <= 0 {
		interval = w.lockTTL
	}

	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-ticker.C:
				ok, err := w.lock.Renew(ctx, w.lockTTL)
				if err != nil {
					w.logger.Warn("capability calibration lease renew failed", failure.LogArgs(err)...)
					continue
				}
				if !ok {
					w.logger.Warn("capability calibration lease lost; aborting run", "worker", w.Name())
					onLost()
					return
				}
			}
		}
	}()

	var stopped bool
	return func() {
		if stopped {
			return
		}
		stopped = true
		close(done)
	}
}

// releaseLock 释放租约（与外部 ctx 解耦并设短超时，确保即便外部已取消也能落地释放）。
func (w *CapabilityCalibrationWorker) releaseLock(ctx context.Context) {
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := w.lock.Release(releaseCtx); err != nil {
		w.logger.Warn("capability calibration lease release failed", failure.LogArgs(err)...)
	}
}

func (w *CapabilityCalibrationWorker) retryBackoff() time.Duration {
	shift := w.consecutiveFailures - 1
	if shift < 0 {
		shift = 0
	}
	if shift > 5 {
		shift = 5
	}

	backoff := capabilityCalibrateBaseRetryBackoff * time.Duration(1<<shift)
	if backoff > capabilityCalibrateMaxRetryBackoff {
		backoff = capabilityCalibrateMaxRetryBackoff
	}
	if backoff > w.interval {
		backoff = w.interval
	}

	return backoff
}

func (w *CapabilityCalibrationWorker) lockRetryBackoff() time.Duration {
	backoff := capabilityCalibrateLockRetryBackoff
	if backoff > w.interval {
		backoff = w.interval
	}
	return backoff
}
