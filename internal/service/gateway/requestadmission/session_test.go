package requestadmission

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/runtimefacts"
)

type storeStub struct {
	mu sync.Mutex

	acquireInput   breakerstore.RequestAdmissionInput
	acquireResult  breakerstore.RequestAdmissionResult
	acquireErr     error
	reserveResult  breakerstore.ReserveResult
	reserveErr     error
	renewOutcome   breakerstore.RequestAdmissionLifecycleOutcome
	renewErr       error
	renewCalls     int
	renewEpoch     string
	renewRevision  int64
	finishOutcome  breakerstore.RequestAdmissionLifecycleOutcome
	finishErr      error
	finishErrs     []error
	finishCalls    int
	finishActual   int64
	finishEpoch    string
	finishRevision int64
	snapshotInput  breakerstore.SnapshotManyInput
	snapshotResult breakerstore.SnapshotManyResult
	snapshotErr    error
}

func (s *storeStub) AcquireRequestAdmission(_ context.Context, input breakerstore.RequestAdmissionInput) (breakerstore.RequestAdmissionResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.acquireInput = input
	return s.acquireResult, s.acquireErr
}

func (s *storeStub) ReserveRequestTokens(context.Context, string, int64, int64, int64, string, int64) (breakerstore.ReserveResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reserveResult, s.reserveErr
}

func (s *storeStub) RenewRequestAdmission(_ context.Context, _ string, _, _ int64, epoch string, revision int64) (breakerstore.RequestAdmissionLifecycleOutcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.renewCalls++
	s.renewEpoch = epoch
	s.renewRevision = revision
	outcome := s.renewOutcome
	if outcome == "" {
		outcome = breakerstore.RequestLifecycleRenewed
	}
	return outcome, s.renewErr
}

func (s *storeStub) FinishRequestAdmission(_ context.Context, _ string, _, _ int64, actual int64, epoch string, revision int64) (breakerstore.RequestAdmissionLifecycleOutcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finishCalls++
	s.finishActual = actual
	s.finishEpoch = epoch
	s.finishRevision = revision
	outcome := s.finishOutcome
	if outcome == "" {
		outcome = breakerstore.RequestLifecycleFinished
	}
	err := s.finishErr
	if len(s.finishErrs) > 0 {
		err = s.finishErrs[0]
		s.finishErrs = s.finishErrs[1:]
	}
	return outcome, err
}

func (s *storeStub) SnapshotMany(_ context.Context, input breakerstore.SnapshotManyInput) (breakerstore.SnapshotManyResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshotInput = input
	return s.snapshotResult, s.snapshotErr
}

type factsStub struct {
	mu             sync.Mutex
	integrity      runtimefacts.Integrity
	integrityErr   error
	integrityErrs  []error
	integrityCalls int
	admission      runtimefacts.AdmissionRevisions
	admissionErr   error
	admissionCalls int
	routing        runtimefacts.RoutingRevisions
	routingErr     error
	routingCalls   int
}

func (f *factsStub) Integrity(context.Context) (runtimefacts.Integrity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.integrityCalls++
	err := f.integrityErr
	if len(f.integrityErrs) > 0 {
		err = f.integrityErrs[0]
		f.integrityErrs = f.integrityErrs[1:]
	}
	return f.integrity, err
}

type metricsStub struct {
	mu         sync.Mutex
	operations map[string]int
	active     float64
}

func (m *metricsStub) IncRequestAdmissionOperation(endpoint, result string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.operations == nil {
		m.operations = make(map[string]int)
	}
	m.operations[endpoint+"/"+result]++
}

func (m *metricsStub) AddRequestAdmissionActive(delta float64) {
	m.mu.Lock()
	m.active += delta
	m.mu.Unlock()
}

func (m *metricsStub) snapshot() (map[string]int, float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	operations := make(map[string]int, len(m.operations))
	for key, count := range m.operations {
		operations[key] = count
	}
	return operations, m.active
}

func (f *factsStub) Admission(context.Context) (runtimefacts.AdmissionRevisions, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.admissionCalls++
	return f.admission, f.admissionErr
}

func (f *factsStub) Routing(context.Context) (runtimefacts.RoutingRevisions, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routingCalls++
	return f.routing, f.routingErr
}

func readyFacts() *factsStub {
	integrity := runtimefacts.Integrity{Epoch: "00112233445566778899aabbccddeeff", Revision: 7}
	return &factsStub{
		integrity: integrity,
		admission: runtimefacts.AdmissionRevisions{
			Integrity: integrity, RouteRateLimits: 3, ChannelRateLimits: 8, Concurrency: 4,
		},
		routing: runtimefacts.RoutingRevisions{Integrity: integrity, CircuitBreaker: 5, RoutingBalance: 6},
	}
}

func TestSessionOwnsRenewReserveBindAndUniqueFinish(t *testing.T) {
	store := &storeStub{
		acquireResult: breakerstore.RequestAdmissionResult{
			Outcome:      breakerstore.RequestAllowed,
			LeaseUntilMs: time.Now().Add(time.Minute).UnixMilli(),
		},
		reserveResult: breakerstore.ReserveReserved,
	}
	rpm, tpm, rpd := int64(10), int64(0), int64(20)
	facts := readyFacts()
	manager := NewManager(store, facts, ManagerOptions{
		RenewInterval:    5 * time.Millisecond,
		OperationTimeout: 100 * time.Millisecond,
	})
	manager.newID = func() string { return "request-admission-1" }

	result, err := manager.Acquire(context.Background(), Identity{
		RouteID: 11, UserID: 22, Scope: "POST /v1/responses",
		RPMLimitOverride: &rpm, TPMLimitOverride: &tpm, RPDLimitOverride: &rpd,
	})
	if err != nil || result.Outcome != breakerstore.RequestAllowed || result.Session == nil {
		t.Fatalf("acquire result=%+v err=%v", result, err)
	}
	if got := store.acquireInput; got.Fingerprint == "" || got.RPMLimitOverride == nil || *got.RPMLimitOverride != 10 ||
		got.TPMLimitOverride == nil || *got.TPMLimitOverride != 0 || got.RPDLimitOverride == nil || *got.RPDLimitOverride != 20 ||
		got.RouteRateRevision != 3 || got.GlobalConcurrencyRevision != 4 {
		t.Fatalf("unexpected acquire input: %+v", got)
	}

	usage := result.Session.Usage()
	if err := usage.Reserve(context.Background(), 123); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	ctx := ContextWithUsageSession(context.Background(), usage)
	attempt := breakerstore.AcquireAttemptInput{EstimatedInputTokens: 123}
	if err := BindAttemptInput(ctx, &attempt); err != nil || attempt.RequestAdmissionID != "request-admission-1" {
		t.Fatalf("bind attempt id=%q err=%v", attempt.RequestAdmissionID, err)
	}
	if !usage.PublishAuthoritativeUsage(77) || usage.PublishAuthoritativeUsage(88) {
		t.Fatal("authoritative usage must be first-write-wins")
	}

	deadline := time.Now().Add(200 * time.Millisecond)
	for {
		store.mu.Lock()
		renewed := store.renewCalls > 0
		store.mu.Unlock()
		if renewed || time.Now().After(deadline) {
			if !renewed {
				t.Fatal("renewer did not run")
			}
			break
		}
		time.Sleep(time.Millisecond)
	}

	if err := result.Session.Finalize(context.Background()); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if err := result.Session.Finalize(context.Background()); err != nil {
		t.Fatalf("duplicate finalize: %v", err)
	}
	store.mu.Lock()
	if store.finishCalls != 1 || store.finishActual != 77 ||
		store.renewEpoch != facts.integrity.Epoch || store.renewRevision != facts.integrity.Revision ||
		store.finishEpoch != facts.integrity.Epoch || store.finishRevision != facts.integrity.Revision {
		t.Fatalf("unexpected lifecycle calls: renew=%d epoch=%s/%d finish=%d actual=%d epoch=%s/%d",
			store.renewCalls, store.renewEpoch, store.renewRevision,
			store.finishCalls, store.finishActual, store.finishEpoch, store.finishRevision)
	}
	store.mu.Unlock()
	facts.mu.Lock()
	defer facts.mu.Unlock()
	if facts.admissionCalls != 1 || facts.integrityCalls < 3 {
		t.Fatalf("expected only acquire to read admission facts and lifecycle writes to read integrity: admission=%d integrity=%d",
			facts.admissionCalls, facts.integrityCalls)
	}
}

func TestManagerDeniedDoesNotCreateSession(t *testing.T) {
	store := &storeStub{acquireResult: breakerstore.RequestAdmissionResult{Outcome: breakerstore.RequestLimited}}
	manager := NewManager(store, readyFacts(), ManagerOptions{})
	result, err := manager.Acquire(context.Background(), Identity{RouteID: 1, UserID: 2, Scope: "GET /v1/models"})
	if err != nil || result.Outcome != breakerstore.RequestLimited || result.Session != nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.renewCalls != 0 || store.finishCalls != 0 {
		t.Fatalf("denied token renewed=%d finished=%d", store.renewCalls, store.finishCalls)
	}
}

func TestSessionSnapshotInjectsFrozenAdmissionAndFreshRoutingRevisions(t *testing.T) {
	store := &storeStub{
		acquireResult: breakerstore.RequestAdmissionResult{
			Outcome: breakerstore.RequestAllowed, LeaseUntilMs: time.Now().Add(time.Minute).UnixMilli(),
		},
		snapshotResult: breakerstore.SnapshotManyResult{
			ChannelRateRevision: 8,
			RoutingBalance:      breakerstore.RoutingBalanceSnapshot{Revision: 6},
		},
	}
	facts := readyFacts()
	manager := NewManager(store, facts, ManagerOptions{RenewInterval: time.Hour})
	result, err := manager.Acquire(context.Background(), Identity{RouteID: 10, UserID: 20, Scope: "POST /v1/responses"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := ContextWithUsageSession(context.Background(), result.Session.Usage())
	candidates := []breakerstore.SnapshotCandidateInput{{
		OriginID: 30, ChannelID: 40, OriginBaseURLRevision: 2, OriginStatusRevision: 3,
		ChannelConfigRevision: 4, ChannelAdmissionRevision: 5,
	}}
	snapshot, present, err := SnapshotManyIfPresent(ctx, 50, candidates)
	if err != nil || !present || snapshot.RoutingBalance.Revision != 6 {
		t.Fatalf("snapshot=%+v present=%v err=%v", snapshot, present, err)
	}
	store.mu.Lock()
	input := store.snapshotInput
	store.mu.Unlock()
	if input.IntegrityEpoch != facts.admission.Epoch || input.IntegrityRevision != facts.admission.Revision ||
		input.ChannelRateRevision != 8 || input.GlobalConcurrencyRevision != 4 ||
		input.CircuitBreakerRevision != 5 || input.RoutingBalanceRevision != 6 || input.ModelID != 50 ||
		len(input.Candidates) != 1 || input.Candidates[0].ChannelID != 40 {
		t.Fatalf("snapshot revisions were not injected correctly: %+v", input)
	}
	if snapshot.RouteRateRevision != 3 || snapshot.ChannelRateRevision != 8 {
		t.Fatalf("snapshot did not preserve split rate revisions: %+v", snapshot)
	}
	if err := result.Session.Finalize(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSessionFinalizeFailsClosedWhenFreshIntegrityCannotBeRead(t *testing.T) {
	store := &storeStub{
		acquireResult: breakerstore.RequestAdmissionResult{
			Outcome:      breakerstore.RequestAllowed,
			LeaseUntilMs: time.Now().Add(time.Minute).UnixMilli(),
		},
	}
	facts := readyFacts()
	manager := NewManager(store, facts, ManagerOptions{RenewInterval: time.Hour})
	result, err := manager.Acquire(context.Background(), Identity{
		RouteID: 31,
		UserID:  32,
		Scope:   "GET /v1/models",
	})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	wantErr := errors.New("postgres unavailable")
	facts.mu.Lock()
	facts.integrityErr = failure.Wrap(
		failure.CodeDependencyPostgresUnavailable,
		wantErr,
		failure.WithMessage("postgres unavailable"),
	)
	facts.mu.Unlock()
	if err := result.Session.Finalize(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("finalize want fresh facts error, got %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.finishCalls != 0 {
		t.Fatalf("finish must not reach Redis without fresh PG epoch, calls=%d", store.finishCalls)
	}
}

func TestSessionFinalizeRetriesTransientIntegrityRead(t *testing.T) {
	store := &storeStub{
		acquireResult: breakerstore.RequestAdmissionResult{
			Outcome:      breakerstore.RequestAllowed,
			LeaseUntilMs: time.Now().Add(time.Minute).UnixMilli(),
		},
	}
	facts := readyFacts()
	facts.integrityErrs = []error{
		failure.New(
			failure.CodeDependencyPostgresUnavailable,
			failure.WithMessage("temporary postgres failure"),
		),
		nil,
	}
	manager := NewManager(store, facts, ManagerOptions{RenewInterval: time.Hour})
	result, err := manager.Acquire(context.Background(), Identity{
		RouteID: 33,
		UserID:  34,
		Scope:   "GET /v1/models",
	})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := result.Session.Finalize(context.Background()); err != nil {
		t.Fatalf("finalize after retry: %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.finishCalls != 1 {
		t.Fatalf("finish calls=%d want=1 after PG retry", store.finishCalls)
	}
}

func TestSessionFinalizeRetriesSameTokenAfterStoreFailure(t *testing.T) {
	store := &storeStub{
		acquireResult: breakerstore.RequestAdmissionResult{
			Outcome:      breakerstore.RequestAllowed,
			LeaseUntilMs: time.Now().Add(time.Minute).UnixMilli(),
		},
		finishErrs: []error{
			failure.Wrap(
				failure.CodeGatewayBreakerStoreUnavailable,
				breakerstore.ErrStoreUnavailable,
				failure.WithMessage("temporary Redis failure"),
			),
			nil,
		},
	}
	manager := NewManager(store, readyFacts(), ManagerOptions{RenewInterval: time.Hour})
	result, err := manager.Acquire(context.Background(), Identity{
		RouteID: 35,
		UserID:  36,
		Scope:   "GET /v1/models",
	})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := result.Session.Finalize(context.Background()); err != nil {
		t.Fatalf("finalize after retry: %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.finishCalls != requestTerminalTries {
		t.Fatalf("finish calls=%d want=%d", store.finishCalls, requestTerminalTries)
	}
}

func TestSessionFinalizeRecordsUnknownAfterStoreRetriesExhausted(t *testing.T) {
	metrics := &metricsStub{}
	storeErr := failure.Wrap(
		failure.CodeGatewayBreakerStoreUnavailable,
		breakerstore.ErrStoreUnavailable,
		failure.WithMessage("Redis result is unknown"),
	)
	store := &storeStub{
		acquireResult: breakerstore.RequestAdmissionResult{
			Outcome:      breakerstore.RequestAllowed,
			LeaseUntilMs: time.Now().Add(time.Minute).UnixMilli(),
		},
		finishErr: storeErr,
	}
	manager := NewManager(store, readyFacts(), ManagerOptions{
		Metrics:       metrics,
		RenewInterval: time.Hour,
	})
	result, err := manager.Acquire(context.Background(), Identity{
		RouteID: 37,
		UserID:  38,
		Scope:   "GET /v1/models",
	})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := result.Session.Finalize(context.Background()); !errors.Is(err, breakerstore.ErrStoreUnavailable) {
		t.Fatalf("finalize error=%v", err)
	}
	store.mu.Lock()
	finishCalls := store.finishCalls
	store.mu.Unlock()
	operations, active := metrics.snapshot()
	if finishCalls != requestTerminalTries || active != 0 || operations["finish/result_unknown"] != 1 {
		t.Fatalf("finish calls=%d active=%v operations=%v", finishCalls, active, operations)
	}
}

func TestSessionReserveRetainsFirstLimitedResult(t *testing.T) {
	store := &storeStub{
		acquireResult: breakerstore.RequestAdmissionResult{Outcome: breakerstore.RequestAllowed, LeaseUntilMs: time.Now().Add(time.Minute).UnixMilli()},
		reserveResult: breakerstore.ReserveLimited,
	}
	manager := NewManager(store, readyFacts(), ManagerOptions{RenewInterval: time.Hour})
	result, err := manager.Acquire(context.Background(), Identity{RouteID: 1, UserID: 2, Scope: "POST /v1/messages"})
	if err != nil {
		t.Fatal(err)
	}
	if err := result.Session.Usage().Reserve(context.Background(), 50); failure.CodeOf(err) != failure.CodeRateLimitExceeded {
		t.Fatalf("limited reserve code=%q err=%v", failure.CodeOf(err), err)
	}
	if err := result.Session.Usage().Reserve(context.Background(), 50); failure.CodeOf(err) != failure.CodeRateLimitExceeded {
		t.Fatalf("replayed limited reserve code=%q err=%v", failure.CodeOf(err), err)
	}
	if err := result.Session.Usage().Reserve(context.Background(), 51); !errors.Is(err, ErrReserveConflict) {
		t.Fatalf("different estimate err=%v", err)
	}
	_ = result.Session.Finalize(context.Background())
}

func TestRequestAdmissionMetricsFollowTokenOwnership(t *testing.T) {
	metrics := &metricsStub{}
	store := &storeStub{
		acquireResult: breakerstore.RequestAdmissionResult{
			Outcome:      breakerstore.RequestAllowed,
			LeaseUntilMs: time.Now().Add(time.Minute).UnixMilli(),
		},
		reserveResult: breakerstore.ReserveReserved,
	}
	manager := NewManager(store, readyFacts(), ManagerOptions{
		Metrics:       metrics,
		RenewInterval: time.Hour,
	})
	result, err := manager.Acquire(context.Background(), Identity{
		RouteID: 41,
		UserID:  42,
		Scope:   "POST /v1/responses",
	})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := result.Session.Usage().Reserve(context.Background(), 12); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	operations, active := metrics.snapshot()
	if active != 1 || operations["acquire/allowed"] != 1 || operations["reserve/reserved"] != 1 {
		t.Fatalf("active=%v operations=%v", active, operations)
	}

	if err := result.Session.Finalize(context.Background()); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if err := result.Session.Finalize(context.Background()); err != nil {
		t.Fatalf("duplicate finalize: %v", err)
	}
	operations, active = metrics.snapshot()
	if active != 0 || operations["finish/finished"] != 1 {
		t.Fatalf("active=%v operations=%v", active, operations)
	}
}

func TestDeniedRequestAdmissionMetricsNeverBecomeActive(t *testing.T) {
	metrics := &metricsStub{}
	manager := NewManager(
		&storeStub{acquireResult: breakerstore.RequestAdmissionResult{Outcome: breakerstore.RequestLimited}},
		readyFacts(),
		ManagerOptions{Metrics: metrics},
	)
	result, err := manager.Acquire(context.Background(), Identity{RouteID: 1, UserID: 2, Scope: "GET /v1/models"})
	if err != nil || result.Session != nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	operations, active := metrics.snapshot()
	if active != 0 || operations["acquire/limited"] != 1 {
		t.Fatalf("active=%v operations=%v", active, operations)
	}
}
