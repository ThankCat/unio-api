package lifecycle

import (
	"context"
	"math/rand"
	"sync/atomic"
	"time"
)

// 全池短等的内部实现常量（§9.4）。轮询间隔以 75ms 为中心、正负 25ms 抖动，
// 这两个值是实现细节，不暴露为 Channel 配置。
const (
	defaultCapacityWaitTimeout   = time.Second
	capacityWaitPollCenter       = 75 * time.Millisecond
	capacityWaitPollJitterWindow = 50 * time.Millisecond // 中心 ±25ms
)

// capacityWaitOutcome 是一次全池短等的稳定结果（写入 trace 的 capacity_wait_result，§13.2）。
type capacityWaitOutcome string

const (
	capacityWaitNotWaited         capacityWaitOutcome = "not_waited"
	capacityWaitAcquired          capacityWaitOutcome = "acquired"
	capacityWaitCapacityExhausted capacityWaitOutcome = "capacity_exhausted"
	capacityWaitRateLimited       capacityWaitOutcome = "rate_limited"
	capacityWaitCanceled          capacityWaitOutcome = "canceled"
)

// capacityWaitPolicy 持有可热更新的全池短等预算（gateway.capacity_wait_timeout_ms，默认 1000ms）。
//
// timeoutNanos 用 0 表示「尚未配置」（回退冻结默认），负值表示「显式关闭短等」，
// 这样显式配置的 0ms 不会被误认为未配置。
type capacityWaitPolicy struct {
	timeoutNanos atomic.Int64
}

// SetCapacityWaitTimeout 原子替换全池短等预算；<=0 表示显式关闭短等。
func (p *capacityWaitPolicy) SetCapacityWaitTimeout(d time.Duration) {
	if p == nil {
		return
	}
	if d <= 0 {
		p.timeoutNanos.Store(-1)
		return
	}
	p.timeoutNanos.Store(int64(d))
}

// Timeout 返回当前生效的短等预算；未配置时回退默认 1000ms，显式关闭时返回 0。
func (p *capacityWaitPolicy) Timeout() time.Duration {
	if p == nil {
		return defaultCapacityWaitTimeout
	}
	switch n := p.timeoutNanos.Load(); {
	case n > 0:
		return time.Duration(n)
	case n < 0:
		return 0
	default:
		return defaultCapacityWaitTimeout
	}
}

// SetCapacityWaitTimeout 供 bootstrap 的 settings applier 热更新全池短等预算。
func (r *AttemptRunner) SetCapacityWaitTimeout(d time.Duration) {
	if r != nil {
		r.capacityWait.SetCapacityWaitTimeout(d)
	}
}

// waitForChannelCapacity 执行 §9.4 的「全池共享、有界、单次」等待。
//
// 等待期间不持有任何 Channel permit；每次轮询前检查请求取消；预算耗尽后由调用方
// 只做一次完整 acquire 重扫，绝不逐渠道各等一次（否则总等待随渠道数线性增长）。
// 返回实际等待时长与结果：canceled 表示客户已放弃，不归咎任何 Channel。
func (r *AttemptRunner) waitForChannelCapacity(ctx context.Context) (time.Duration, capacityWaitOutcome) {
	budget := r.capacityWait.Timeout()
	if budget <= 0 {
		return 0, capacityWaitNotWaited
	}
	if deadline, ok := ctx.Deadline(); ok {
		if remain := time.Until(deadline); remain <= 0 {
			return 0, capacityWaitCanceled
		} else if budget > remain {
			budget = remain
		}
	}

	start := time.Now()
	deadline := start.Add(budget)
	for {
		if ctx.Err() != nil {
			return time.Since(start), capacityWaitCanceled
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			waited := time.Since(start)
			r.recordCapacityWait(waited)
			return waited, capacityWaitCapacityExhausted
		}
		sleep := capacityWaitPollInterval()
		if sleep > remaining {
			sleep = remaining
		}
		timer := time.NewTimer(sleep)
		select {
		case <-ctx.Done():
			timer.Stop()
			return time.Since(start), capacityWaitCanceled
		case <-timer.C:
		}
	}
}

// capacityWaitPollInterval 返回 75ms 中心、±25ms 抖动的轮询间隔，避免多请求同步唤醒。
func capacityWaitPollInterval() time.Duration {
	jitter := time.Duration(rand.Int63n(int64(capacityWaitPollJitterWindow) + 1))
	return capacityWaitPollCenter - capacityWaitPollJitterWindow/2 + jitter
}

func (r *AttemptRunner) recordRoutingSkip(reason string) {
	if r == nil || r.lifecycle == nil || r.lifecycle.metrics == nil {
		return
	}
	r.lifecycle.metrics.IncRoutingSkip(reason)
}

func (r *AttemptRunner) recordCapacityWait(d time.Duration) {
	if r == nil || r.lifecycle == nil || r.lifecycle.metrics == nil || d <= 0 {
		return
	}
	r.lifecycle.metrics.ObserveRoutingCapacityWait(d)
}
