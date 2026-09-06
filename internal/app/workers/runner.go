package workers

import (
	"context"
	"errors"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

const defaultRunnerIdleInterval = time.Second

// Unit 表示 runner 可调度的一类后台任务。
type Unit interface {
	// Name 返回 worker 名称，用于日志和 worker id。
	Name() string
	// RunOnce 执行一次工作；返回 true 表示本轮处理了任务。
	RunOnce(ctx context.Context) (bool, error)
}

// Runner 顺序调度一组后台 worker，并在空闲时按固定间隔休眠。
//
// 同一 Runner 内的 unit 串行执行：一个慢单元（如外呼上游的渠道探测）会推迟同组其它单元的下一轮。
// 需要隔离延迟的任务分到不同 Runner，再由 Group 并行运行。
type Runner struct {
	name         string
	logger       *zap.Logger
	idleInterval time.Duration
	workers      []Unit
}

// NewRunner 创建后台 worker runner。
func NewRunner(logger *zap.Logger, idleInterval time.Duration, units ...Unit) *Runner {
	if logger == nil {
		logger = zap.NewNop()
	}
	if idleInterval <= 0 {
		idleInterval = defaultRunnerIdleInterval
	}

	return &Runner{
		logger:       logger,
		idleInterval: idleInterval,
		workers:      units,
	}
}

// WithName 给 runner 一个用于日志归因的名字（如 settlement / maintenance）。
func (r *Runner) WithName(name string) *Runner {
	if r != nil {
		r.name = name
	}
	return r
}

// Run 持续执行 worker，直到 ctx 被取消。
func (r *Runner) Run(ctx context.Context) error {
	for {
		// 每一轮开始前先检查 ctx 是否已经被取消。
		// 如果外部已经取消，就直接退出。
		if err := ctx.Err(); err != nil {
			return nil
		}

		// worked 用来记录这一轮是否有任何 worker 实际做了事情。
		// 只要有一个 worker 返回 didWork == true，就认为本轮有工作发生。
		worked := false

		for _, unit := range r.workers {
			if unit == nil {
				continue
			}

			// RunOnce 表示让 worker 尝试执行一次任务。
			// didWork 表示这次是否真的处理了任务。
			didWork, err := unit.RunOnce(ctx)
			if err != nil {
				fields := []zap.Field{zap.String("worker", unit.Name())}
				if r.name != "" {
					fields = append(fields, zap.String("runner", r.name))
				}
				fields = append(fields, failure.LogFields(err)...)
				r.logger.Error("worker run failed", fields...)
			}

			// 一旦某个 worker 做过事情，worked 就保持为 true。
			// 后续 worker 即使 didWork == false，也不会把 worked 改回 false。
			worked = worked || didWork
		}

		// 如果这一轮有 worker 做了事，说明系统里可能还有任务。
		// 这里不休眠，立刻进入下一轮继续处理。
		if worked {
			continue
		}

		// 如果这一轮所有 worker 都没有做事，说明当前可能没有任务。
		// 这里休眠 idleInterval，避免空转占用 CPU。
		timer := time.NewTimer(r.idleInterval)

		select {
		case <-ctx.Done():
			// Stop 为 false 时 timer.C 不保证可读，非阻塞 drain 避免卡住退出路径。
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return nil

		case <-timer.C:
			// idleInterval 到了，继续下一轮循环。
		}
	}
}

// Group 并行运行多个独立 Runner，让账务补偿这类关键路径不被慢探测拖住。
//
// 任一 Runner 返回错误时取消其余 Runner 并返回首个错误；全部因 ctx 取消而正常退出时返回 nil。
type Group struct {
	runners []*Runner
}

// NewGroup 创建并行 runner 组；nil runner 被忽略。
func NewGroup(runners ...*Runner) *Group {
	group := &Group{}
	for _, runner := range runners {
		if runner != nil {
			group.runners = append(group.runners, runner)
		}
	}
	return group
}

// Run 阻塞运行全部 runner，直到 ctx 取消或任一 runner 出错。
func (g *Group) Run(ctx context.Context) error {
	if g == nil || len(g.runners) == 0 {
		<-ctx.Done()
		return nil
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
	)
	for _, runner := range g.runners {
		wg.Add(1)
		go func(runner *Runner) {
			defer wg.Done()
			if err := runner.Run(runCtx); err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				cancel()
			}
		}(runner)
	}
	wg.Wait()
	if firstErr != nil && !errors.Is(firstErr, context.Canceled) {
		return firstErr
	}
	return nil
}
