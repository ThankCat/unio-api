package lifecycle

import (
	"context"
	"time"

	"github.com/ThankCat/unio-gateway/internal/core/routing"
)

// candidateAdmitResult 是一次候选准入（并发槽 + 渠道级限流）的结果。
type candidateAdmitResult struct {
	release    func()
	admitted   bool
	waitedMs   int64
	skipReason string // breaker 不走本函数；concurrency / ratelimit / ratelimit_store
	err        error
}

// admitCandidate 占用渠道并发名额并做渠道级限流预占。
//
// 队首短等（决议 4 / R10）：仅对 isHead 候选、且本请求尚未等过时，在并发满或渠道 TPM/RPM 满
// 时 sleep 一次（预算来自 StickyRouter.SampleHeadWait），等待期间不持有名额/预占，再重试准入；
// 超时或仍满则跳过该候选走 failover。非队首候选直接跳过，不等。
func (r *AttemptRunner) admitCandidate(
	ctx context.Context,
	candidate routing.ChatRouteCandidate,
	estTokens int64,
	isHead bool,
) candidateAdmitResult {
	budget := time.Duration(0)
	if isHead && r.headWait != nil {
		budget = r.headWait.SampleHeadWait()
	}

	waitedTotal := time.Duration(0)
	retried := false

	for {
		releaseSlot, slotOK := r.acquireChannelSlot(candidate)
		if !slotOK {
			if !retried && budget > 0 {
				waited, err := sleepHeadWait(ctx, budget)
				waitedTotal += waited
				if err != nil {
					return candidateAdmitResult{
						release:    func() {},
						admitted:   false,
						waitedMs:   waitedTotal.Milliseconds(),
						skipReason: "concurrency",
						err:        err,
					}
				}
				retried = true
				continue
			}
			r.recordRoutingSkip("concurrency")
			if waitedTotal > 0 {
				r.recordHeadWait(waitedTotal)
			}
			return candidateAdmitResult{
				release:    func() {},
				admitted:   false,
				waitedMs:   waitedTotal.Milliseconds(),
				skipReason: "concurrency",
				err:        channelConcurrencyLimitedError(),
			}
		}

		dec, allowed, err := r.guardChannel(ctx, candidate, estTokens)
		if err != nil {
			releaseSlot()
			r.recordRoutingSkip("ratelimit_store")
			if waitedTotal > 0 {
				r.recordHeadWait(waitedTotal)
			}
			return candidateAdmitResult{
				release:    func() {},
				admitted:   false,
				waitedMs:   waitedTotal.Milliseconds(),
				skipReason: "ratelimit_store",
				err:        err,
			}
		}
		if !allowed {
			releaseSlot()
			if !retried && budget > 0 {
				waited, sleepErr := sleepHeadWait(ctx, budget)
				waitedTotal += waited
				if sleepErr != nil {
					return candidateAdmitResult{
						release:    func() {},
						admitted:   false,
						waitedMs:   waitedTotal.Milliseconds(),
						skipReason: "ratelimit",
						err:        sleepErr,
					}
				}
				retried = true
				continue
			}
			r.recordRoutingSkip("ratelimit")
			if waitedTotal > 0 {
				r.recordHeadWait(waitedTotal)
			}
			return candidateAdmitResult{
				release:    func() {},
				admitted:   false,
				waitedMs:   waitedTotal.Milliseconds(),
				skipReason: "ratelimit",
				err:        channelRateLimitedError(dec),
			}
		}

		if waitedTotal > 0 {
			r.recordHeadWait(waitedTotal)
		}
		return candidateAdmitResult{
			release:  releaseSlot,
			admitted: true,
			waitedMs: waitedTotal.Milliseconds(),
		}
	}
}

// sleepHeadWait 等待 d，但不超过 ctx 剩余截止时间（计入客户端超时预算，R10）。
// 返回实际等待时长；ctx 取消/超时时返回已等待时长与 ctx.Err()。
func sleepHeadWait(ctx context.Context, d time.Duration) (time.Duration, error) {
	if d <= 0 {
		return 0, nil
	}
	if deadline, ok := ctx.Deadline(); ok {
		remain := time.Until(deadline)
		if remain <= 0 {
			return 0, ctx.Err()
		}
		if d > remain {
			d = remain
		}
	}

	start := time.Now()
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return time.Since(start), ctx.Err()
	case <-timer.C:
		return d, nil
	}
}

func (r *AttemptRunner) recordRoutingSkip(reason string) {
	if r == nil || r.lifecycle == nil || r.lifecycle.metrics == nil {
		return
	}
	r.lifecycle.metrics.IncRoutingSkip(reason)
}

func (r *AttemptRunner) recordHeadWait(d time.Duration) {
	if r == nil || r.lifecycle == nil || r.lifecycle.metrics == nil || d <= 0 {
		return
	}
	r.lifecycle.metrics.ObserveRoutingHeadWait(d)
}