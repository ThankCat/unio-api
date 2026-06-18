package workers

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/ThankCat/unio-api/internal/core/capability/calibration"
)

type fakeCalibrator struct {
	calls  int
	result calibration.Result
	err    error
}

func (f *fakeCalibrator) Run(_ context.Context, _ calibration.Options) (calibration.Result, error) {
	f.calls++
	return f.result, f.err
}

// fakeCalibrationLock 是 CalibrationLock 的测试替身：可配置 Acquire 结果并记录调用。
type fakeCalibrationLock struct {
	mu         sync.Mutex
	acquireOK  bool
	acquireErr error
	acquired   int
	released   int
}

func (l *fakeCalibrationLock) Acquire(_ context.Context, _ time.Duration) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.acquireErr != nil {
		return false, l.acquireErr
	}
	if l.acquireOK {
		l.acquired++
	}
	return l.acquireOK, nil
}

func (l *fakeCalibrationLock) Renew(_ context.Context, _ time.Duration) (bool, error) {
	return true, nil
}

func (l *fakeCalibrationLock) Release(_ context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.released++
	return nil
}

func (l *fakeCalibrationLock) counts() (acquired, released int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.acquired, l.released
}

// TestCapabilityCalibrationWorkerIntervalGating 验证：首轮执行、间隔内跳过、到期再执行（无锁单实例）。
func TestCapabilityCalibrationWorkerIntervalGating(t *testing.T) {
	cal := &fakeCalibrator{result: calibration.Result{ScannedAttempts: 3}}
	w := NewCapabilityCalibrationWorker(cal, nil, slog.Default(), time.Hour, 0)
	base := time.Now()
	w.now = func() time.Time { return base }

	did, err := w.RunOnce(context.Background())
	if err != nil || !did {
		t.Fatalf("first run: did=%v err=%v", did, err)
	}
	if cal.calls != 1 {
		t.Fatalf("expected 1 call, got %d", cal.calls)
	}

	// 间隔内立即再跑应跳过。
	if did, _ := w.RunOnce(context.Background()); did {
		t.Fatal("expected skip within interval")
	}
	if cal.calls != 1 {
		t.Fatalf("expected still 1 call within interval, got %d", cal.calls)
	}

	// 超过间隔后再次执行。
	w.now = func() time.Time { return base.Add(2 * time.Hour) }
	if did, _ := w.RunOnce(context.Background()); !did {
		t.Fatal("expected execution after interval elapsed")
	}
	if cal.calls != 2 {
		t.Fatalf("expected 2 calls after interval, got %d", cal.calls)
	}
}

// TestCapabilityCalibrationWorkerFailureBackoff 验证：失败不向上传播（记日志）、累计失败、退避阻止立即重试。
func TestCapabilityCalibrationWorkerFailureBackoff(t *testing.T) {
	cal := &fakeCalibrator{err: errors.New("boom")}
	w := NewCapabilityCalibrationWorker(cal, nil, slog.Default(), time.Hour, 0)
	base := time.Now()
	w.now = func() time.Time { return base }

	did, err := w.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("handled failure must not propagate err, got %v", err)
	}
	if !did {
		t.Fatal("expected did=true on attempted run")
	}
	if w.consecutiveFailures != 1 {
		t.Fatalf("expected 1 consecutive failure, got %d", w.consecutiveFailures)
	}

	// 退避期内立即重试应跳过。
	if did, _ := w.RunOnce(context.Background()); did {
		t.Fatal("expected retry backoff to block immediate re-run")
	}
}

// TestCapabilityCalibrationWorkerLockAcquired 验证：抢到租约则执行校正，并在结束后释放。
func TestCapabilityCalibrationWorkerLockAcquired(t *testing.T) {
	cal := &fakeCalibrator{result: calibration.Result{ScannedAttempts: 1}}
	lock := &fakeCalibrationLock{acquireOK: true}
	w := NewCapabilityCalibrationWorker(cal, lock, slog.Default(), time.Hour, time.Minute)
	w.now = func() time.Time { return time.Now() }

	did, err := w.RunOnce(context.Background())
	if err != nil || !did {
		t.Fatalf("expected run with acquired lock: did=%v err=%v", did, err)
	}
	if cal.calls != 1 {
		t.Fatalf("expected calibrator to run once, got %d", cal.calls)
	}
	acquired, released := lock.counts()
	if acquired != 1 {
		t.Fatalf("expected 1 acquire, got %d", acquired)
	}
	if released != 1 {
		t.Fatalf("expected lease released after run, got %d", released)
	}
}

// TestCapabilityCalibrationWorkerLockBusy 验证：另一实例持有租约时跳过本轮（不执行校正、不前进 nextRunAt）。
func TestCapabilityCalibrationWorkerLockBusy(t *testing.T) {
	cal := &fakeCalibrator{result: calibration.Result{}}
	lock := &fakeCalibrationLock{acquireOK: false}
	w := NewCapabilityCalibrationWorker(cal, lock, slog.Default(), time.Hour, time.Minute)
	base := time.Now()
	w.now = func() time.Time { return base }

	did, err := w.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("busy lock must not propagate err, got %v", err)
	}
	if did {
		t.Fatal("expected did=false when another instance holds the lease")
	}
	if cal.calls != 0 {
		t.Fatalf("expected calibrator not to run when lease busy, got %d", cal.calls)
	}
	if _, released := lock.counts(); released != 0 {
		t.Fatalf("expected no release when lease not acquired, got %d", released)
	}
}

// TestCapabilityCalibrationWorkerLockAcquireError 验证：抢锁 DB 故障时短退避、不执行校正、不向上传播错误。
func TestCapabilityCalibrationWorkerLockAcquireError(t *testing.T) {
	cal := &fakeCalibrator{result: calibration.Result{}}
	lock := &fakeCalibrationLock{acquireErr: errors.New("db down")}
	w := NewCapabilityCalibrationWorker(cal, lock, slog.Default(), time.Hour, time.Minute)
	base := time.Now()
	w.now = func() time.Time { return base }

	did, err := w.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("acquire error must be handled, not propagated, got %v", err)
	}
	if !did {
		t.Fatal("expected did=true on attempted acquire")
	}
	if cal.calls != 0 {
		t.Fatalf("expected calibrator not to run on acquire error, got %d", cal.calls)
	}

	// 退避期内立即重试应跳过。
	if did, _ := w.RunOnce(context.Background()); did {
		t.Fatal("expected lock retry backoff to block immediate re-run")
	}
}
