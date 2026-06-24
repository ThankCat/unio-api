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
	store     SettlementRecoveryJobStore
	recoverer SettlementRecoveryRecoverer
	workerID  string
	lockTTL   time.Duration
}

// NewSettlementRecoveryWorker 创建 settlement recovery worker。
func NewSettlementRecoveryWorker(store SettlementRecoveryJobStore, recoverer SettlementRecoveryRecoverer, workerID string, lockTTL time.Duration) *SettlementRecoveryWorker {
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

	return &SettlementRecoveryWorker{
		store:     store,
		recoverer: recoverer,
		workerID:  workerID,
		lockTTL:   lockTTL,
	}
}

// Name 返回 worker 名称。
func (w *SettlementRecoveryWorker) Name() string {
	return "settlement_recovery"
}

// RunOnce claim 并处理一条到期 settlement recovery job。
func (w *SettlementRecoveryWorker) RunOnce(ctx context.Context) (bool, error) {
	now := time.Now()

	// 先收口已 dead 但请求仍停留在 running 的补偿任务，避免请求永远显示「进行中」且余额被永久冻结。
	if worked, err := w.finalizeNextDeadJob(ctx); worked || err != nil {
		return worked, err
	}

	if worked, err := w.markExhausted(ctx, now); worked || err != nil {
		return worked, err
	}

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
		NextRunAt:               pgtype.Timestamptz{Time: now.Add(settlementRecoveryBackoff(job.AttemptCount)), Valid: true},
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

func settlementRecoveryBackoff(attemptCount int32) time.Duration {
	if attemptCount <= 1 {
		return time.Second
	}

	shift := attemptCount - 1
	if shift > 6 {
		shift = 6
	}

	return time.Second * time.Duration(1<<shift)
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
