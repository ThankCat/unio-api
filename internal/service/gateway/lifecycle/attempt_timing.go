package lifecycle

import (
	"sync"
	"time"
)

// AttemptTimingFacts 是一次真实 upstream transport 的协议无关时间事实。
type AttemptTimingFacts struct {
	UpstreamStartedAt    *time.Time
	UpstreamFirstTokenAt *time.Time
	UpstreamCompletedAt  *time.Time
}

// FirstTokenMs 只在流式 attempt 已观测到有效 FirstToken 时返回样本。
func (f AttemptTimingFacts) FirstTokenMs() *int64 {
	if f.UpstreamStartedAt == nil || f.UpstreamFirstTokenAt == nil {
		return nil
	}
	value := f.UpstreamFirstTokenAt.Sub(*f.UpstreamStartedAt).Milliseconds()
	if value < 0 {
		value = 0
	}
	return &value
}

// AttemptTimingObserver 用 first-write-wins 记录 transport start / stream FirstToken / completion。
// 非流式 observer 永远不产生 FirstToken 事实。
type AttemptTimingObserver struct {
	mu     sync.Mutex
	stream bool
	now    func() time.Time
	facts  AttemptTimingFacts
}

func NewAttemptTimingObserver(stream bool) *AttemptTimingObserver {
	return newAttemptTimingObserver(stream, time.Now)
}

func newAttemptTimingObserver(stream bool, now func() time.Time) *AttemptTimingObserver {
	if now == nil {
		now = time.Now
	}
	return &AttemptTimingObserver{stream: stream, now: now}
}

// TransportStarted 由 adapter 在紧邻 http.Client.Do 前调用。
func (o *AttemptTimingObserver) TransportStarted() {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.facts.UpstreamStartedAt != nil {
		return
	}
	now := o.now()
	o.facts.UpstreamStartedAt = &now
}

// FirstTokenEligible 只由协议层已标记 FirstTokenEligible 的流事件调用。
// 它与 SuppressEmit 、客户 SSE write-ack 及 delivery response_started_at 相互独立。
func (o *AttemptTimingObserver) FirstTokenEligible() {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.stream || o.facts.UpstreamStartedAt == nil || o.facts.UpstreamCompletedAt != nil || o.facts.UpstreamFirstTokenAt != nil {
		return
	}
	now := notBefore(o.now(), *o.facts.UpstreamStartedAt)
	o.facts.UpstreamFirstTokenAt = &now
}

// TransportCompleted 由 lifecycle 在 adapter 成功或失败返回时调用。
// transport 从未开始时保持三项均为 nil。
func (o *AttemptTimingObserver) TransportCompleted() {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.facts.UpstreamStartedAt == nil || o.facts.UpstreamCompletedAt != nil {
		return
	}
	now := notBefore(o.now(), *o.facts.UpstreamStartedAt)
	if o.facts.UpstreamFirstTokenAt != nil {
		now = notBefore(now, *o.facts.UpstreamFirstTokenAt)
	}
	o.facts.UpstreamCompletedAt = &now
}

func (o *AttemptTimingObserver) Snapshot() AttemptTimingFacts {
	if o == nil {
		return AttemptTimingFacts{}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return cloneAttemptTimingFacts(o.facts)
}

func cloneAttemptTimingFacts(in AttemptTimingFacts) AttemptTimingFacts {
	return AttemptTimingFacts{
		UpstreamStartedAt:    cloneTime(in.UpstreamStartedAt),
		UpstreamFirstTokenAt: cloneTime(in.UpstreamFirstTokenAt),
		UpstreamCompletedAt:  cloneTime(in.UpstreamCompletedAt),
	}
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func notBefore(value, lowerBound time.Time) time.Time {
	if value.Before(lowerBound) {
		return lowerBound
	}
	return value
}
