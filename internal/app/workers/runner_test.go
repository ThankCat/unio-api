package workers

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
)

type countingUnit struct {
	name  string
	runs  atomic.Int64
	block time.Duration
	err   error
}

func (u *countingUnit) Name() string { return u.name }

func (u *countingUnit) RunOnce(ctx context.Context) (bool, error) {
	u.runs.Add(1)
	if u.block > 0 {
		select {
		case <-ctx.Done():
		case <-time.After(u.block):
		}
	}
	return false, u.err
}

// 慢单元（模拟外呼探测）只能拖住自己所在的 runner；另一 runner 必须按自身节奏继续轮转。
func TestGroupIsolatesSlowRunnerFromFastRunner(t *testing.T) {
	slow := &countingUnit{name: "probe", block: 400 * time.Millisecond}
	fast := &countingUnit{name: "settlement"}
	group := NewGroup(
		NewRunner(zap.NewNop(), 10*time.Millisecond, slow).WithName("maintenance"),
		NewRunner(zap.NewNop(), 10*time.Millisecond, fast).WithName("settlement"),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := group.Run(ctx); err != nil {
		t.Fatalf("group run: %v", err)
	}
	if slow.runs.Load() != 1 {
		t.Fatalf("slow unit runs = %d, want exactly 1 within the window", slow.runs.Load())
	}
	if fast.runs.Load() < 5 {
		t.Fatalf("fast unit runs = %d, want many rounds despite the slow sibling runner", fast.runs.Load())
	}
}

// unit 出错只记日志、不终止 runner；ctx 取消时 runner 与 group 都正常返回 nil。
func TestRunnerKeepsGoingAfterUnitErrorAndStopsOnCancel(t *testing.T) {
	failing := &countingUnit{name: "flaky", err: errors.New("boom")}
	runner := NewRunner(zap.NewNop(), 5*time.Millisecond, failing, nil).WithName("test")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	if err := NewGroup(runner, nil).Run(ctx); err != nil {
		t.Fatalf("group run: %v", err)
	}
	if failing.runs.Load() < 2 {
		t.Fatalf("failing unit must keep being scheduled, runs = %d", failing.runs.Load())
	}
}

func TestEmptyGroupWaitsForContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := NewGroup().Run(ctx); err != nil {
		t.Fatalf("empty group run: %v", err)
	}
}
