package lifecycle

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/core/channel"
	"github.com/ThankCat/unio-gateway/internal/core/requestlog"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/requestadmission"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/runtimefacts"
)

type headWaitPermitStore struct {
	mu      sync.Mutex
	inputs  []breakerstore.AcquireAttemptInput
	results []breakerstore.AttemptAdmission
}

func (s *headWaitPermitStore) AcquireAttempt(_ context.Context, in breakerstore.AcquireAttemptInput) (breakerstore.AttemptAdmission, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inputs = append(s.inputs, in)
	result := s.results[0]
	s.results = s.results[1:]
	return result, nil
}

func (*headWaitPermitStore) Renew(context.Context, breakerstore.AttemptPermit) error { return nil }
func (*headWaitPermitStore) Finish(context.Context, breakerstore.AttemptPermit, breakerstore.FinishOutcome) (breakerstore.FinishResult, error) {
	return breakerstore.FinishResult{}, nil
}
func (*headWaitPermitStore) Abort(context.Context, breakerstore.AttemptPermit) error { return nil }

func TestSleepHeadWaitCapsToDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()

	start := time.Now()
	waited, err := sleepHeadWait(ctx, 500*time.Millisecond)
	elapsed := time.Since(start)

	// 预算被截到剩余截止时间后定时器先于 ctx.Done 触发时 err 可为 nil；
	// 关键义是「不等超过客户端超时」，不是「必须以 deadline error 返回」。
	if waited <= 0 {
		t.Fatalf("expected positive waited duration, got %v (err=%v)", waited, err)
	}
	if elapsed > 200*time.Millisecond {
		t.Fatalf("sleep should cap to ctx deadline, elapsed=%v", elapsed)
	}
}

func TestSleepHeadWaitCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := sleepHeadWait(ctx, 200*time.Millisecond)
	if err == nil {
		t.Fatal("expected canceled error")
	}
}

func TestSleepHeadWaitZeroNoop(t *testing.T) {
	waited, err := sleepHeadWait(context.Background(), 0)
	if err != nil || waited != 0 {
		t.Fatalf("zero wait must be noop, got waited=%v err=%v", waited, err)
	}
}

func TestAcquireAttemptHeadWaitRetriesCapacityWithFreshPermit(t *testing.T) {
	integrity := runtimefacts.Integrity{Epoch: "epoch-ready", Revision: 9}
	store := &headWaitPermitStore{results: []breakerstore.AttemptAdmission{
		{Mode: breakerstore.AdmissionDenied, Reason: breakerstore.ReasonConcurrencyLimited},
		{Mode: breakerstore.AdmissionPermit, Permit: &breakerstore.AttemptPermit{
			PermitID: "permit-2", IntegrityEpoch: integrity.Epoch, IntegrityRevision: integrity.Revision,
			PermitTTLMs: 30_000, RenewMs: 10_000, TerminalTTLMs: 300_000,
		}},
	}}
	manager := NewAttemptPermitManager(store, attemptRuntimeFactsStub{
		admission: runtimefacts.AdmissionRevisions{
			Integrity: integrity, RouteRateLimits: 3, ChannelRateLimits: 8, Concurrency: 4,
		},
		routing: runtimefacts.RoutingRevisions{Integrity: integrity, CircuitBreaker: 5},
	}, AttemptPermitManagerOptions{})
	permitIDs := []string{"permit-1", "permit-2"}
	manager.newPermitID = func() string {
		id := permitIDs[0]
		permitIDs = permitIDs[1:]
		return id
	}

	sticky := NewStickyRouter(newFakeStickyStore())
	sticky.SetConfig(true, time.Hour, time.Millisecond, 0)
	runner := &AttemptRunner{permitManager: manager, headWait: sticky}
	ctx := requestadmission.ContextWithUsageSession(context.Background(), &attemptUsageSessionStub{requestID: "request-token"})
	used := false
	admission, owner, err := runner.acquireAttemptWithHeadWait(ctx, AttemptPermitAcquireParams{
		Candidate: routing.ChatRouteCandidate{
			ModelDBID: 11, ProviderOriginID: 12, ProviderOriginBaseURLRevision: 13,
			ProviderOriginStatusRevision: 14, ChannelConfigRevision: 15,
			ChannelAdmissionLimitsRevision: 16, Channel: channel.Runtime{ID: 17},
		},
		UpstreamEndpoint: requestlog.UpstreamEndpointChatCompletions,
		RequestMode:       breakerstore.ModeNonStream,
	}, true, &used)
	if err != nil || admission.Mode != breakerstore.AdmissionPermit || owner == nil {
		t.Fatalf("admission=%+v owner=%v err=%v", admission, owner, err)
	}
	if err := owner.Abort(context.Background()); err != nil {
		t.Fatalf("abort: %v", err)
	}
	if !used || len(store.inputs) != 2 {
		t.Fatalf("used=%v acquire calls=%d", used, len(store.inputs))
	}
	if store.inputs[0].PermitID == store.inputs[1].PermitID {
		t.Fatal("head-wait retry must use a fresh permit id")
	}
}

func TestAcquireAttemptHeadWaitDoesNotRetryBreakerDenial(t *testing.T) {
	integrity := runtimefacts.Integrity{Epoch: "epoch-ready", Revision: 9}
	store := &headWaitPermitStore{results: []breakerstore.AttemptAdmission{
		{Mode: breakerstore.AdmissionDenied, Reason: breakerstore.ReasonOpen},
	}}
	manager := NewAttemptPermitManager(store, attemptRuntimeFactsStub{
		admission: runtimefacts.AdmissionRevisions{Integrity: integrity},
		routing:   runtimefacts.RoutingRevisions{Integrity: integrity},
	}, AttemptPermitManagerOptions{})
	sticky := NewStickyRouter(newFakeStickyStore())
	sticky.SetConfig(true, time.Hour, time.Millisecond, 0)
	runner := &AttemptRunner{permitManager: manager, headWait: sticky}
	ctx := requestadmission.ContextWithUsageSession(context.Background(), &attemptUsageSessionStub{requestID: "request-token"})
	used := false
	admission, owner, err := runner.acquireAttemptWithHeadWait(ctx, AttemptPermitAcquireParams{}, true, &used)
	if err != nil || owner != nil || admission.Reason != breakerstore.ReasonOpen {
		t.Fatalf("admission=%+v owner=%v err=%v", admission, owner, err)
	}
	if used || len(store.inputs) != 1 {
		t.Fatalf("breaker denial must not wait: used=%v calls=%d", used, len(store.inputs))
	}
}

func TestSampleHeadWaitIncludesJitterBound(t *testing.T) {
	store := newFakeStickyStore()
	router := NewStickyRouter(store)
	router.SetConfig(true, time.Hour, 100*time.Millisecond, 50*time.Millisecond)

	for i := 0; i < 20; i++ {
		d := router.SampleHeadWait()
		if d < 100*time.Millisecond || d > 150*time.Millisecond {
			t.Fatalf("sample out of [100ms,150ms]: %v", d)
		}
	}

	router.SetConfig(true, time.Hour, 0, 100*time.Millisecond)
	if d := router.SampleHeadWait(); d != 0 {
		t.Fatalf("wait=0 must disable head wait, got %v", d)
	}
}

func TestApplyPlanOutcomeClearsOnPinLost(t *testing.T) {
	store := newFakeStickyStore()
	router := NewStickyRouter(store)
	session := router.Resolve(context.Background(), stickyResolveParams("sess"))
	session.BindSuccess(context.Background(), 7)

	second := router.Resolve(context.Background(), stickyResolveParams("sess"))
	if second.BoundChannelID() != 7 {
		t.Fatalf("expected bound 7, got %d", second.BoundChannelID())
	}

	second.ApplyPlanOutcome(context.Background(), CandidatePlan{StickyPinned: false})
	if second.BoundChannelID() != 0 {
		t.Fatal("pin_lost must clear local binding")
	}
	third := router.Resolve(context.Background(), stickyResolveParams("sess"))
	if third.BoundChannelID() != 0 {
		t.Fatal("pin_lost must clear redis binding")
	}
}
