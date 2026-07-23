package lifecycle

import (
	"context"
	"time"

	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
)

// SetHeadWaitSource 保留 sticky 的队首短等配置源；nil 表示关闭短等。
func (r *AttemptRunner) SetHeadWaitSource(src *StickyRouter) {
	if r != nil {
		r.headWait = src
	}
}

// acquireAttemptWithHeadWait retries the first candidate once after a capacity denial.
// A denied Acquire owns no candidate resources, so the wait is resource-free and the retry
// receives a fresh permit ID and current runtime-control revisions.
func (r *AttemptRunner) acquireAttemptWithHeadWait(
	ctx context.Context,
	params AttemptPermitAcquireParams,
	isHead bool,
	headWaitUsed *bool,
) (breakerstore.AttemptAdmission, *AttemptPermitOwner, error) {
	admission, owner, err := r.permitManager.Acquire(ctx, params)
	if err != nil || admission.Mode != breakerstore.AdmissionDenied || !isHead ||
		headWaitUsed == nil || *headWaitUsed || r.headWait == nil || !isCapacityDenial(admission.Reason) {
		return admission, owner, err
	}

	budget := r.headWait.SampleHeadWait()
	if budget <= 0 {
		return admission, owner, nil
	}
	*headWaitUsed = true
	waited, waitErr := sleepHeadWait(ctx, budget)
	r.recordHeadWait(waited)
	if waitErr != nil {
		return admission, nil, waitErr
	}
	return r.permitManager.Acquire(ctx, params)
}

func isCapacityDenial(reason breakerstore.DeniedReason) bool {
	return reason == breakerstore.ReasonConcurrencyLimited || reason == breakerstore.ReasonRateLimited
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
