package workers

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
)

const (
	defaultSettlementRecoveryLockTTL = 30 * time.Second
	settlementRecoveryLockMargin     = 5 * time.Second
	maxRecoveryErrorDetailBytes      = 2048
	// defaultSettlementRecoveryBackoffCap 是补偿重试指数退避的单次上限回退默认（配置 <=0 时使用）。
	defaultSettlementRecoveryBackoffCap = 5 * time.Minute
	// defaultSettlementRecoveryBatchSize 是单轮 RunOnce 最多 claim/处理的补偿任务数（P2-5）。
	// 批量排空把每轮固定开销（dead 收口 + exhausted 标记扫描）摊薄到多条 job 上，
	// 积压时显著加快排空；每条仍以 FOR UPDATE SKIP LOCKED 独立 claim，多副本/多 worker 并发安全。
	defaultSettlementRecoveryBatchSize = 16
)

// SettlementRecoveryJobStore 定义 worker claim 和推进 recovery job 状态所需的存储能力。
type SettlementRecoveryJobStore interface {
	ClaimNextSettlementRecoveryJob(ctx context.Context, arg sqlc.ClaimNextSettlementRecoveryJobParams) (sqlc.SettlementRecoveryJob, error)
	MarkSettlementRecoveryJobSucceeded(ctx context.Context, arg sqlc.MarkSettlementRecoveryJobSucceededParams) (sqlc.MarkSettlementRecoveryJobSucceededRow, error)
	MarkSettlementRecoveryJobRetry(ctx context.Context, arg sqlc.MarkSettlementRecoveryJobRetryParams) (sqlc.SettlementRecoveryJob, error)
	MarkSettlementRecoveryJobDead(ctx context.Context, arg sqlc.MarkSettlementRecoveryJobDeadParams) (sqlc.SettlementRecoveryJob, error)
	MarkExhaustedSettlementRecoveryJobDead(ctx context.Context, arg sqlc.MarkExhaustedSettlementRecoveryJobDeadParams) (sqlc.SettlementRecoveryJob, error)
	GetDeadSettlementRecoveryJobWithRunningRequest(ctx context.Context) (sqlc.SettlementRecoveryJob, error)
}

// SettlementRecoveryRecoverer 定义 worker 重放 settlement recovery job 的业务能力。
type SettlementRecoveryRecoverer interface {
	RecoverChatSettlement(ctx context.Context, job sqlc.SettlementRecoveryJob) error
	// FinalizeDeadChatSettlement 收口一条已 dead 但请求仍 running 的补偿任务：
	// 释放冻结余额（记风险敞口）并把请求推进到 failed。以「请求仍为 running」为幂等闸门。
	FinalizeDeadChatSettlement(ctx context.Context, job sqlc.SettlementRecoveryJob) error
}

// SettlementRecoveryWorker claim settlement_recovery_jobs 并驱动幂等 settlement 重试。
type SettlementRecoveryWorker struct {
	store      SettlementRecoveryJobStore
	recoverer  SettlementRecoveryRecoverer
	workerID   string
	lockTTL    time.Duration
	backoffCap time.Duration
	batchSize  int
}

// NewSettlementRecoveryWorker 创建 settlement recovery worker。
// backoffCap 是重试指数退避的单次上限；<=0 时回退默认。与 job.max_attempts 一起决定补偿总覆盖窗口。
func NewSettlementRecoveryWorker(store SettlementRecoveryJobStore, recoverer SettlementRecoveryRecoverer, workerID string, lockTTL time.Duration, backoffCap time.Duration) *SettlementRecoveryWorker {
	if store == nil {
		panic("workers: settlement recovery store is required")
	}
	if recoverer == nil {
		panic("workers: settlement recovery recoverer is required")
	}
	if workerID == "" {
		workerID = "settlement-recovery-worker"
	}
	if lockTTL <= 0 {
		lockTTL = defaultSettlementRecoveryLockTTL
	}
	if backoffCap <= 0 {
		backoffCap = defaultSettlementRecoveryBackoffCap
	}

	return &SettlementRecoveryWorker{
		store:      store,
		recoverer:  recoverer,
		workerID:   workerID,
		lockTTL:    lockTTL,
		backoffCap: backoffCap,
		batchSize:  defaultSettlementRecoveryBatchSize,
	}
}

// SetBatchSize 配置单轮 RunOnce 最多 claim/处理的补偿任务数（P2-5 积压排空）。<=0 时回退默认。
func (w *SettlementRecoveryWorker) SetBatchSize(size int) {
	if size <= 0 {
		size = defaultSettlementRecoveryBatchSize
	}
	w.batchSize = size
}

// Name 返回 worker 名称。
func (w *SettlementRecoveryWorker) Name() string {
	return "settlement_recovery"
}

// RunOnce 收口 dead 残留、标记到期耗尽，并批量 claim/处理一批到期 settlement recovery job。
//
// 批量排空（P2-5）：单轮把每轮固定开销（dead 收口 + exhausted 标记）摊薄到至多 batchSize 条 job，
// 积压时显著减少 DB 往返、加快排空；每条仍以独立的 FOR UPDATE SKIP LOCKED claim，多 worker 并发安全。
func (w *SettlementRecoveryWorker) RunOnce(ctx context.Context) (bool, error) {
	now := time.Now()

	// dead 收口与 exhausted 标记保持「每 tick 至多一次、且优先于 claim」的语义：
	// 任一做了事就抢占本 tick 返回，依赖 Runner 的「有工作即立刻再跑」继续推进，避免与批量 claim 互相抢锁。
	if worked, err := w.finalizeNextDeadJob(ctx); worked || err != nil {
		return worked, err
	}

	if worked, err := w.markExhausted(ctx, now); worked || err != nil {
		return worked, err
	}

	batchSize := w.batchSize
	if batchSize <= 0 {
		batchSize = defaultSettlementRecoveryBatchSize
	}

	// 仅对 claim+重放阶段做批量排空：单 tick 连续 claim 至多 batchSize 条到期 job，
	// 把每轮 dead/exhausted 扫描的固定开销摊薄到多条 job，积压时显著加快排空。
	worked := false
	for i := 0; i < batchSize; i++ {
		processed, err := w.claimAndRecoverOne(ctx)
		if err != nil {
			return true, err
		}
		if !processed {
			break
		}
		worked = true
	}

	return worked, nil
}

// claimAndRecoverOne claim 并处理一条到期 settlement recovery job；processed=false 表示当前已无到期任务。
func (w *SettlementRecoveryWorker) claimAndRecoverOne(ctx context.Context) (processed bool, err error) {
	now := time.Now()

	job, err := w.store.ClaimNextSettlementRecoveryJob(ctx, sqlc.ClaimNextSettlementRecoveryJobParams{
		LockedBy: pgtype.Text{String: w.workerID, Valid: true},
		LockedUntil: pgtype.Timestamptz{
			Time:  now.Add(w.lockTTL),
			Valid: true,
		},
		NowAt: pgtype.Timestamptz{Time: now, Valid: true},
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}

		return false, failure.Wrap(
			failure.CodeGatewayChatSettlementFailed,
			err,
			failure.WithMessage("claim settlement recovery job"),
		)
	}

	// job 执行超时必须短于数据库锁 TTL，给当前 worker 留出更新 job 状态的时间。
	// 否则锁刚过期时旧 worker 才取消，容易和新 worker 的重新 claim 发生竞争。
	jobCtx, cancel := context.WithTimeout(ctx, w.recoveryTimeout())
	defer cancel()

	if err := w.recoverer.RecoverChatSettlement(jobCtx, job); err != nil {
		return true, w.markFailed(ctx, job, err)
	}

	_, err = w.store.MarkSettlementRecoveryJobSucceeded(ctx, sqlc.MarkSettlementRecoveryJobSucceededParams{
		ID:          job.ID,
		CompletedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	})
	if err != nil {
		return true, failure.Wrap(
			failure.CodeGatewayChatSettlementFailed,
			err,
			failure.WithMessage("mark settlement recovery job succeeded"),
		)
	}

	return true, nil
}

// finalizeNextDeadJob 收口一条已 dead、但请求仍停留在 running 的补偿任务。
//
// 这类残留来自 settlement 永久失败 + 补偿重试耗尽：请求记录会卡在 running、冻结余额不释放。
// 委托 recoverer 在单事务内释放冻结余额（记风险敞口）并把请求推进到 failed；以「请求仍为 running」
// 为幂等闸门，崩溃后下个 tick 安全重放，多 worker 并发时由请求记录行锁串行化。
func (w *SettlementRecoveryWorker) finalizeNextDeadJob(ctx context.Context) (bool, error) {
	job, err := w.store.GetDeadSettlementRecoveryJobWithRunningRequest(ctx)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}

		return false, failure.Wrap(
			failure.CodeGatewayChatSettlementFailed,
			err,
			failure.WithMessage("find dead settlement recovery job to finalize"),
		)
	}

	finalizeCtx, cancel := context.WithTimeout(ctx, w.recoveryTimeout())
	defer cancel()

	if err := w.recoverer.FinalizeDeadChatSettlement(finalizeCtx, job); err != nil {
		return true, failure.Wrap(
			failure.CodeGatewayChatSettlementFailed,
			err,
			failure.WithMessage("finalize dead settlement recovery job"),
		)
	}

	return true, nil
}

func (w *SettlementRecoveryWorker) markExhausted(ctx context.Context, now time.Time) (bool, error) {
	_, err := w.store.MarkExhaustedSettlementRecoveryJobDead(ctx, sqlc.MarkExhaustedSettlementRecoveryJobDeadParams{
		NowAt:                   pgtype.Timestamptz{Time: now, Valid: true},
		LastErrorCode:           nullableText(string(failure.CodeGatewayChatSettlementFailed)),
		LastErrorMessage:        nullableText("Settlement recovery attempts exhausted."),
		LastInternalErrorDetail: nullableText("settlement recovery job reached max_attempts before worker could claim another retry"),
		CompletedAt:             pgtype.Timestamptz{Time: now, Valid: true},
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}

		return false, failure.Wrap(
			failure.CodeGatewayChatSettlementFailed,
			err,
			failure.WithMessage("mark exhausted settlement recovery job dead"),
		)
	}

	return true, nil
}

func (w *SettlementRecoveryWorker) markFailed(ctx context.Context, job sqlc.SettlementRecoveryJob, recoverErr error) error {
	now := time.Now()
	code, safeMessage, internalDetail := recoveryErrorFacts(recoverErr)

	if job.AttemptCount >= job.MaxAttempts {
		_, err := w.store.MarkSettlementRecoveryJobDead(ctx, sqlc.MarkSettlementRecoveryJobDeadParams{
			ID:                      job.ID,
			LockedBy:                job.LockedBy,
			LockedUntil:             job.LockedUntil,
			AttemptCount:            job.AttemptCount,
			LastErrorCode:           nullableText(code),
			LastErrorMessage:        nullableText(safeMessage),
			LastInternalErrorDetail: nullableText(internalDetail),
			CompletedAt:             pgtype.Timestamptz{Time: now, Valid: true},
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}

			return failure.Wrap(
				failure.CodeGatewayChatSettlementFailed,
				err,
				failure.WithMessage("mark settlement recovery job dead"),
			)
		}

		return nil
	}

	_, err := w.store.MarkSettlementRecoveryJobRetry(ctx, sqlc.MarkSettlementRecoveryJobRetryParams{
		ID:                      job.ID,
		LockedBy:                job.LockedBy,
		LockedUntil:             job.LockedUntil,
		AttemptCount:            job.AttemptCount,
		NextRunAt:               pgtype.Timestamptz{Time: now.Add(settlementRecoveryBackoff(job.AttemptCount, w.backoffCap)), Valid: true},
		LastErrorCode:           nullableText(code),
		LastErrorMessage:        nullableText(safeMessage),
		LastInternalErrorDetail: nullableText(internalDetail),
		UpdatedAt:               pgtype.Timestamptz{Time: now, Valid: true},
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}

		return failure.Wrap(
			failure.CodeGatewayChatSettlementFailed,
			err,
			failure.WithMessage("mark settlement recovery job retry"),
		)
	}

	return nil
}

func (w *SettlementRecoveryWorker) recoveryTimeout() time.Duration {
	if w.lockTTL <= 0 {
		return defaultSettlementRecoveryLockTTL - settlementRecoveryLockMargin
	}
	if w.lockTTL <= settlementRecoveryLockMargin {
		timeout := w.lockTTL / 2
		if timeout <= 0 {
			return w.lockTTL
		}
		return timeout
	}

	return w.lockTTL - settlementRecoveryLockMargin
}

// settlementRecoveryBackoff 计算第 attemptCount 次失败后的下次重试延迟：
// 指数退避 1s,2s,4s,...，增长到 cap 后保持平稳。cap<=0 时回退默认上限。
// 退避序列在 cap 处封顶，避免随尝试次数无界增长，同时把总覆盖窗口拉长到分钟~小时级。
func settlementRecoveryBackoff(attemptCount int32, backoffCap time.Duration) time.Duration {
	if backoffCap <= 0 {
		backoffCap = defaultSettlementRecoveryBackoffCap
	}
	if attemptCount <= 1 {
		return minDuration(time.Second, backoffCap)
	}

	// 用 30 位封顶移位幂，避免 1<<shift 在大 attemptCount 时溢出；幂一旦超过 cap 即提前返回 cap。
	shift := attemptCount - 1
	if shift > 30 {
		shift = 30
	}

	backoff := time.Second * time.Duration(int64(1)<<uint(shift))
	if backoff <= 0 || backoff > backoffCap {
		return backoffCap
	}

	return backoff
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}

	return b
}

func recoveryErrorFacts(err error) (code string, safeMessage string, internalDetail string) {
	code = string(failure.CodeOf(err))
	if code == "" {
		code = string(failure.CodeGatewayChatSettlementFailed)
	}

	return code, "Settlement recovery failed.", truncateRecoveryErrorDetail(err)
}

func truncateRecoveryErrorDetail(err error) string {
	if err == nil {
		return ""
	}

	detail := strings.TrimSpace(err.Error())
	if len(detail) <= maxRecoveryErrorDetailBytes {
		return detail
	}

	return detail[:maxRecoveryErrorDetailBytes] + "...[truncated]"
}

func nullableText(value string) pgtype.Text {
	if value == "" {
		return pgtype.Text{Valid: false}
	}

	return pgtype.Text{String: value, Valid: true}
}
