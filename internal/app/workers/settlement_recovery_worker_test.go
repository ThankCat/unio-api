package workers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

type fakeSettlementRecoveryJobStore struct {
	claimJob       sqlc.SettlementRecoveryJob
	claimErr       error
	exhaustedJob   sqlc.SettlementRecoveryJob
	exhaustedErr   error
	exhaustedFound bool

	deadRunningJob   sqlc.SettlementRecoveryJob
	deadRunningFound bool
	deadRunningErr   error

	claimQueue       []sqlc.SettlementRecoveryJob
	claimCount       int
	claimArgs        []sqlc.ClaimNextSettlementRecoveryJobParams
	succeededArgs    []sqlc.MarkSettlementRecoveryJobSucceededParams
	retryArgs        []sqlc.MarkSettlementRecoveryJobRetryParams
	retryErr         error
	deadArgs         []sqlc.MarkSettlementRecoveryJobDeadParams
	deadErr          error
	exhaustedArgs    []sqlc.MarkExhaustedSettlementRecoveryJobDeadParams
	deadRunningCalls int
}

func (s *fakeSettlementRecoveryJobStore) GetDeadSettlementRecoveryJobWithRunningRequest(ctx context.Context) (sqlc.SettlementRecoveryJob, error) {
	s.deadRunningCalls++
	if s.deadRunningErr != nil {
		return sqlc.SettlementRecoveryJob{}, s.deadRunningErr
	}
	if !s.deadRunningFound {
		return sqlc.SettlementRecoveryJob{}, pgx.ErrNoRows
	}
	return s.deadRunningJob, nil
}

func (s *fakeSettlementRecoveryJobStore) ClaimNextSettlementRecoveryJob(ctx context.Context, arg sqlc.ClaimNextSettlementRecoveryJobParams) (sqlc.SettlementRecoveryJob, error) {
	s.claimArgs = append(s.claimArgs, arg)
	if s.claimErr != nil {
		return sqlc.SettlementRecoveryJob{}, s.claimErr
	}
	// 队列模式：依次返回多条到期 job，耗尽后 ErrNoRows，用于验证批量排空。
	if len(s.claimQueue) > 0 {
		job := s.claimQueue[0]
		s.claimQueue = s.claimQueue[1:]
		return job, nil
	}
	if s.claimJob.ID == 0 {
		return sqlc.SettlementRecoveryJob{}, pgx.ErrNoRows
	}
	// 批量排空（P2-5）：已 claim 的 job 在同批次不会被再次 claim（真实库由 next_run_at/锁状态排除），
	// 因此首条返回配置 job，其后返回 ErrNoRows 让批量循环收尾。
	if s.claimCount > 0 {
		return sqlc.SettlementRecoveryJob{}, pgx.ErrNoRows
	}
	s.claimCount++
	return s.claimJob, nil
}

func (s *fakeSettlementRecoveryJobStore) MarkSettlementRecoveryJobSucceeded(ctx context.Context, arg sqlc.MarkSettlementRecoveryJobSucceededParams) (sqlc.MarkSettlementRecoveryJobSucceededRow, error) {
	s.succeededArgs = append(s.succeededArgs, arg)
	return sqlc.MarkSettlementRecoveryJobSucceededRow{ID: arg.ID, Status: "succeeded"}, nil
}

func (s *fakeSettlementRecoveryJobStore) MarkSettlementRecoveryJobRetry(ctx context.Context, arg sqlc.MarkSettlementRecoveryJobRetryParams) (sqlc.SettlementRecoveryJob, error) {
	s.retryArgs = append(s.retryArgs, arg)
	if s.retryErr != nil {
		return sqlc.SettlementRecoveryJob{}, s.retryErr
	}
	return sqlc.SettlementRecoveryJob{ID: arg.ID, Status: "pending"}, nil
}

func (s *fakeSettlementRecoveryJobStore) MarkSettlementRecoveryJobDead(ctx context.Context, arg sqlc.MarkSettlementRecoveryJobDeadParams) (sqlc.SettlementRecoveryJob, error) {
	s.deadArgs = append(s.deadArgs, arg)
	if s.deadErr != nil {
		return sqlc.SettlementRecoveryJob{}, s.deadErr
	}
	return sqlc.SettlementRecoveryJob{ID: arg.ID, Status: "dead"}, nil
}

func (s *fakeSettlementRecoveryJobStore) MarkExhaustedSettlementRecoveryJobDead(ctx context.Context, arg sqlc.MarkExhaustedSettlementRecoveryJobDeadParams) (sqlc.SettlementRecoveryJob, error) {
	s.exhaustedArgs = append(s.exhaustedArgs, arg)
	if s.exhaustedErr != nil {
		return sqlc.SettlementRecoveryJob{}, s.exhaustedErr
	}
	if !s.exhaustedFound {
		return sqlc.SettlementRecoveryJob{}, pgx.ErrNoRows
	}
	return s.exhaustedJob, nil
}

type fakeSettlementRecoveryRecoverer struct {
	jobs         []sqlc.SettlementRecoveryJob
	err          error
	finalizeJobs []sqlc.SettlementRecoveryJob
	finalizeErr  error
}

func (r *fakeSettlementRecoveryRecoverer) RecoverChatSettlement(ctx context.Context, job sqlc.SettlementRecoveryJob) error {
	r.jobs = append(r.jobs, job)
	return r.err
}

func (r *fakeSettlementRecoveryRecoverer) FinalizeDeadChatSettlement(ctx context.Context, job sqlc.SettlementRecoveryJob) error {
	r.finalizeJobs = append(r.finalizeJobs, job)
	return r.finalizeErr
}

func TestSettlementRecoveryWorkerRunOnceReturnsIdleWhenNoJob(t *testing.T) {
	store := &fakeSettlementRecoveryJobStore{}
	worker := NewSettlementRecoveryWorker(store, &fakeSettlementRecoveryRecoverer{}, "worker-a", time.Second, time.Minute)

	worked, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce returned err: %v", err)
	}
	if worked {
		t.Fatal("expected idle worker when no job exists")
	}
	if len(store.claimArgs) != 1 {
		t.Fatalf("expected one claim attempt, got %d", len(store.claimArgs))
	}
	if store.claimArgs[0].LockedBy.String != "worker-a" || !store.claimArgs[0].LockedBy.Valid {
		t.Fatalf("expected locked_by worker-a, got %#v", store.claimArgs[0].LockedBy)
	}
}

func TestSettlementRecoveryWorkerMarksSucceededAfterRecovery(t *testing.T) {
	store := &fakeSettlementRecoveryJobStore{claimJob: recoveryJob(10, 1, 3)}
	recoverer := &fakeSettlementRecoveryRecoverer{}
	worker := NewSettlementRecoveryWorker(store, recoverer, "worker-a", time.Second, time.Minute)

	worked, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce returned err: %v", err)
	}
	if !worked {
		t.Fatal("expected worker to process job")
	}
	if len(recoverer.jobs) != 1 || recoverer.jobs[0].ID != 10 {
		t.Fatalf("expected recoverer to receive job 10, got %#v", recoverer.jobs)
	}
	if len(store.succeededArgs) != 1 || store.succeededArgs[0].ID != 10 {
		t.Fatalf("expected job 10 to be marked succeeded, got %#v", store.succeededArgs)
	}
	if len(store.retryArgs) != 0 || len(store.deadArgs) != 0 {
		t.Fatalf("expected no retry/dead mark, got retry=%d dead=%d", len(store.retryArgs), len(store.deadArgs))
	}
}

func TestSettlementRecoveryWorkerRetriesRecoverableFailure(t *testing.T) {
	recoverErr := failure.New(failure.CodeGatewayChatSettlementFailed, failure.WithMessage("temporary settlement failure"))
	store := &fakeSettlementRecoveryJobStore{claimJob: recoveryJob(20, 2, 4)}
	worker := NewSettlementRecoveryWorker(store, &fakeSettlementRecoveryRecoverer{err: recoverErr}, "worker-a", time.Second, time.Minute)

	worked, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce returned err: %v", err)
	}
	if !worked {
		t.Fatal("expected worker to process failed job")
	}
	if len(store.retryArgs) != 1 || store.retryArgs[0].ID != 20 {
		t.Fatalf("expected job 20 to be marked retry, got %#v", store.retryArgs)
	}
	if store.retryArgs[0].LockedBy.String != "worker-a" || store.retryArgs[0].AttemptCount != 2 {
		t.Fatalf("expected retry to use current worker lock facts, got locked_by=%#v attempt_count=%d", store.retryArgs[0].LockedBy, store.retryArgs[0].AttemptCount)
	}
	if !store.retryArgs[0].LockedUntil.Valid {
		t.Fatalf("expected retry to include locked_until, got %#v", store.retryArgs[0].LockedUntil)
	}
	if store.retryArgs[0].LastErrorCode.String != string(failure.CodeGatewayChatSettlementFailed) {
		t.Fatalf("expected failure code, got %#v", store.retryArgs[0].LastErrorCode)
	}
	if !store.retryArgs[0].NextRunAt.Valid || !store.retryArgs[0].NextRunAt.Time.After(time.Now()) {
		t.Fatalf("expected future retry time, got %#v", store.retryArgs[0].NextRunAt)
	}
	if len(store.deadArgs) != 0 {
		t.Fatalf("expected no dead mark before max attempts, got %d", len(store.deadArgs))
	}
}

func TestSettlementRecoveryWorkerMarksDeadOnFinalFailure(t *testing.T) {
	store := &fakeSettlementRecoveryJobStore{claimJob: recoveryJob(30, 3, 3)}
	worker := NewSettlementRecoveryWorker(store, &fakeSettlementRecoveryRecoverer{err: errors.New("permanent settlement failure")}, "worker-a", time.Second, time.Minute)

	worked, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce returned err: %v", err)
	}
	if !worked {
		t.Fatal("expected worker to process final failed job")
	}
	if len(store.deadArgs) != 1 || store.deadArgs[0].ID != 30 {
		t.Fatalf("expected job 30 to be marked dead, got %#v", store.deadArgs)
	}
	if store.deadArgs[0].LockedBy.String != "worker-a" || store.deadArgs[0].AttemptCount != 3 {
		t.Fatalf("expected dead mark to use current worker lock facts, got locked_by=%#v attempt_count=%d", store.deadArgs[0].LockedBy, store.deadArgs[0].AttemptCount)
	}
	if !store.deadArgs[0].LockedUntil.Valid {
		t.Fatalf("expected dead mark to include locked_until, got %#v", store.deadArgs[0].LockedUntil)
	}
	if store.deadArgs[0].LastErrorCode.String != string(failure.CodeGatewayChatSettlementFailed) {
		t.Fatalf("expected fallback failure code, got %#v", store.deadArgs[0].LastErrorCode)
	}
	if len(store.retryArgs) != 0 {
		t.Fatalf("expected no retry at max attempts, got %d", len(store.retryArgs))
	}
}

func TestSettlementRecoveryWorkerMarksExhaustedJobDeadBeforeClaim(t *testing.T) {
	store := &fakeSettlementRecoveryJobStore{
		exhaustedFound: true,
		exhaustedJob:   recoveryJob(40, 3, 3),
		claimJob:       recoveryJob(50, 1, 3),
	}
	recoverer := &fakeSettlementRecoveryRecoverer{}
	worker := NewSettlementRecoveryWorker(store, recoverer, "worker-a", time.Second, time.Minute)

	worked, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce returned err: %v", err)
	}
	if !worked {
		t.Fatal("expected exhausted job to count as work")
	}
	if len(store.exhaustedArgs) != 1 {
		t.Fatalf("expected one exhausted mark, got %d", len(store.exhaustedArgs))
	}
	if !store.exhaustedArgs[0].CompletedAt.Valid {
		t.Fatal("expected exhausted dead mark to set completed_at")
	}
	if len(store.claimArgs) != 0 {
		t.Fatalf("expected claim to be skipped after exhausted mark, got %d", len(store.claimArgs))
	}
	if len(recoverer.jobs) != 0 {
		t.Fatalf("expected recoverer not to run for exhausted job, got %d calls", len(recoverer.jobs))
	}
}

func TestSettlementRecoveryWorkerFinalizesDeadJobWithRunningRequestBeforeOtherWork(t *testing.T) {
	store := &fakeSettlementRecoveryJobStore{
		deadRunningFound: true,
		deadRunningJob:   recoveryJob(80, 3, 3),
		// 同时存在 exhausted/claim 工作，验证 dead 收口优先且抢占本 tick。
		exhaustedFound: true,
		exhaustedJob:   recoveryJob(81, 3, 3),
		claimJob:       recoveryJob(82, 1, 3),
	}
	recoverer := &fakeSettlementRecoveryRecoverer{}
	worker := NewSettlementRecoveryWorker(store, recoverer, "worker-a", time.Second, time.Minute)

	worked, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce returned err: %v", err)
	}
	if !worked {
		t.Fatal("expected dead-job finalize to count as work")
	}
	if len(recoverer.finalizeJobs) != 1 || recoverer.finalizeJobs[0].ID != 80 {
		t.Fatalf("expected finalize for job 80, got %#v", recoverer.finalizeJobs)
	}
	if len(store.exhaustedArgs) != 0 {
		t.Fatalf("expected markExhausted skipped after finalize, got %d", len(store.exhaustedArgs))
	}
	if len(store.claimArgs) != 0 {
		t.Fatalf("expected claim skipped after finalize, got %d", len(store.claimArgs))
	}
	if len(recoverer.jobs) != 0 {
		t.Fatalf("expected recovery replay not called during finalize tick, got %d", len(recoverer.jobs))
	}
}

func TestSettlementRecoveryWorkerFinalizeErrorPropagates(t *testing.T) {
	store := &fakeSettlementRecoveryJobStore{
		deadRunningFound: true,
		deadRunningJob:   recoveryJob(90, 3, 3),
	}
	recoverer := &fakeSettlementRecoveryRecoverer{finalizeErr: errors.New("finalize boom")}
	worker := NewSettlementRecoveryWorker(store, recoverer, "worker-a", time.Second, time.Minute)

	worked, err := worker.RunOnce(context.Background())
	if err == nil {
		t.Fatal("expected finalize error to propagate so the runner retries next tick")
	}
	if !worked {
		t.Fatal("expected finalize attempt to count as processed work")
	}
	if len(recoverer.finalizeJobs) != 1 || recoverer.finalizeJobs[0].ID != 90 {
		t.Fatalf("expected one finalize attempt for job 90, got %#v", recoverer.finalizeJobs)
	}
}

func TestSettlementRecoveryWorkerIgnoresRetryWhenLockOwnershipWasLost(t *testing.T) {
	store := &fakeSettlementRecoveryJobStore{
		claimJob: recoveryJob(60, 2, 4),
		retryErr: pgx.ErrNoRows,
	}
	worker := NewSettlementRecoveryWorker(store, &fakeSettlementRecoveryRecoverer{err: errors.New("stale worker failure")}, "worker-a", time.Second, time.Minute)

	worked, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce returned err: %v", err)
	}
	if !worked {
		t.Fatal("expected stale retry conflict to still count as processed work")
	}
	if len(store.retryArgs) != 1 {
		t.Fatalf("expected one retry attempt, got %d", len(store.retryArgs))
	}
}

func TestSettlementRecoveryWorkerIgnoresDeadMarkWhenLockOwnershipWasLost(t *testing.T) {
	store := &fakeSettlementRecoveryJobStore{
		claimJob: recoveryJob(70, 3, 3),
		deadErr:  pgx.ErrNoRows,
	}
	worker := NewSettlementRecoveryWorker(store, &fakeSettlementRecoveryRecoverer{err: errors.New("stale final failure")}, "worker-a", time.Second, time.Minute)

	worked, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce returned err: %v", err)
	}
	if !worked {
		t.Fatal("expected stale dead conflict to still count as processed work")
	}
	if len(store.deadArgs) != 1 {
		t.Fatalf("expected one dead mark attempt, got %d", len(store.deadArgs))
	}
}

func TestSettlementRecoveryWorkerDrainsBatchPerTick(t *testing.T) {
	store := &fakeSettlementRecoveryJobStore{
		claimQueue: []sqlc.SettlementRecoveryJob{
			recoveryJob(101, 1, 3),
			recoveryJob(102, 1, 3),
			recoveryJob(103, 1, 3),
		},
	}
	recoverer := &fakeSettlementRecoveryRecoverer{}
	worker := NewSettlementRecoveryWorker(store, recoverer, "worker-a", time.Second, time.Minute)

	worked, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce returned err: %v", err)
	}
	if !worked {
		t.Fatal("expected worker to process queued jobs")
	}
	// 单 tick 内批量排空全部三条 job（每条独立 claim + 重放 + 标记成功）。
	if len(recoverer.jobs) != 3 {
		t.Fatalf("expected 3 jobs recovered in one tick, got %d", len(recoverer.jobs))
	}
	if len(store.succeededArgs) != 3 {
		t.Fatalf("expected 3 jobs marked succeeded in one tick, got %d", len(store.succeededArgs))
	}
}

func TestSettlementRecoveryWorkerBatchSizeBoundsDrain(t *testing.T) {
	store := &fakeSettlementRecoveryJobStore{
		claimQueue: []sqlc.SettlementRecoveryJob{
			recoveryJob(201, 1, 3),
			recoveryJob(202, 1, 3),
			recoveryJob(203, 1, 3),
		},
	}
	recoverer := &fakeSettlementRecoveryRecoverer{}
	worker := NewSettlementRecoveryWorker(store, recoverer, "worker-a", time.Second, time.Minute)
	worker.SetBatchSize(2)

	worked, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce returned err: %v", err)
	}
	if !worked {
		t.Fatal("expected worker to process queued jobs")
	}
	// batchSize=2 限制单 tick 只处理两条，剩余留待下个 tick。
	if len(recoverer.jobs) != 2 {
		t.Fatalf("expected 2 jobs recovered when batch size is 2, got %d", len(recoverer.jobs))
	}
}

func TestSettlementRecoveryWorkerRecoveryTimeoutKeepsLockMargin(t *testing.T) {
	worker := NewSettlementRecoveryWorker(&fakeSettlementRecoveryJobStore{}, &fakeSettlementRecoveryRecoverer{}, "worker-a", 30*time.Second, time.Minute)

	if got := worker.recoveryTimeout(); got != 25*time.Second {
		t.Fatalf("expected recovery timeout %v, got %v", 25*time.Second, got)
	}
}

func TestSettlementRecoveryBackoffCapsAndCoversWindow(t *testing.T) {
	cap := 5 * time.Minute

	cases := []struct {
		attemptCount int32
		want         time.Duration
	}{
		{attemptCount: 0, want: time.Second},
		{attemptCount: 1, want: time.Second},
		{attemptCount: 2, want: 2 * time.Second},
		{attemptCount: 3, want: 4 * time.Second},
		{attemptCount: 9, want: 256 * time.Second},
		{attemptCount: 10, want: cap},  // 512s 截断到 cap
		{attemptCount: 20, want: cap},  // 远超 cap 仍稳定在 cap
		{attemptCount: 100, want: cap}, // 大尝试次数不溢出
	}
	for _, tc := range cases {
		if got := settlementRecoveryBackoff(tc.attemptCount, cap); got != tc.want {
			t.Fatalf("backoff(%d) = %v, want %v", tc.attemptCount, got, tc.want)
		}
	}

	// 覆盖窗口：max_attempts=20 时，第 1..19 次失败后各排一次退避，总窗口应达分钟~小时级（远大于旧的 ~4 分钟）。
	var total time.Duration
	for attempt := int32(1); attempt <= 19; attempt++ {
		total += settlementRecoveryBackoff(attempt, cap)
	}
	if total < 45*time.Minute {
		t.Fatalf("expected total recovery window >= 45m, got %v", total)
	}
}

func TestSettlementRecoveryBackoffFallsBackToDefaultCap(t *testing.T) {
	if got := settlementRecoveryBackoff(100, 0); got != defaultSettlementRecoveryBackoffCap {
		t.Fatalf("backoff with non-positive cap = %v, want default %v", got, defaultSettlementRecoveryBackoffCap)
	}
}

func recoveryJob(id int64, attemptCount int32, maxAttempts int32) sqlc.SettlementRecoveryJob {
	return sqlc.SettlementRecoveryJob{
		ID:           id,
		Status:       "running",
		AttemptCount: attemptCount,
		MaxAttempts:  maxAttempts,
		LockedBy:     pgtype.Text{String: "worker-a", Valid: true},
		LockedUntil:  pgtype.Timestamptz{Time: time.Now().Add(time.Second), Valid: true},
	}
}
