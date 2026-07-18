package workers

import (
	"context"
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
type Runner struct {
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
				fields := append([]zap.Field{zap.String("worker", unit.Name())}, failure.LogFields(err)...)
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
