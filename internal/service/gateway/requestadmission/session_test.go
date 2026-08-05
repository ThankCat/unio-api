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
	renewOutcome   breakerstore.RequestAdmissionLifecycleOutcome
	renewErr       error
	renewCalls     int
	renewEpoch     string
	renewRevision  int64
	finishOutcome  breakerstore.RequestAdmissionLifecycleOutcome
	finishErr      error
	finishErrs     []error
	finishCalls    int
	finishEpoch    string
	finishRevision int64
	snapshotInput  breakerstore.SnapshotManyInput
	snapshotResult breakerstore.SnapshotManyResult
	snapshotErr    error

	sampleChannelIDs []int64
	sampleWindows    map[int64]breakerstore.ChannelSampleWindow
	sampleErr        error
}

func (s *storeStub) AcquireRequestAdmission(_ context.Context, input breakerstore.RequestAdmissionInput) (breakerstore.RequestAdmissionResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.acquireInput = input
	return s.acquireResult, s.acquireErr
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

func (s *storeStub) FinishRequestAdmission(_ context.Context, _ string, _, _ int64, epoch string, revision int64) (breakerstore.RequestAdmissionLifecycleOutcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finishCalls++
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

func (s *storeStub) AggregateChannelSamples(_ context.Context, channelIDs []int64) (map[int64]breakerstore.ChannelSampleWindow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sampleChannelIDs = channelIDs
	return s.sampleWindows, s.sampleErr
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
			Integrity: integrity, RouteRateLimits: 3, Concurrency: 4,
		},
		routing: runtimefacts.RoutingRevisions{Integrity: integrity, CircuitBreaker: 5, RoutingBalance: 6},
	}
}

func TestSessionOwnsRenewBindAndUniqueFinish(t *testing.T) {
	store := &storeStub{
		acquireResult: breakerstore.RequestAdmissionResult{
			Outcome:      breakerstore.RequestAllowed,
			LeaseUntilMs: time.Now().Add(time.Minute).UnixMilli(),
		},
	}
	rpm, rpd := int64(10), int64(20)
	facts := readyFacts()
	manager := NewManager(store, facts, ManagerOptions{
		RenewInterval:    5 * time.Millisecond,
		OperationTimeout: 100 * time.Millisecond,
	})
	manager.newID = func() string { return "request-admission-1" }

	result, err := manager.Acquire(context.Background(), Identity{
		RouteID: 11, UserID: 22, Scope: "POST /v1/responses",
		RPMLimitOverride: &rpm, RPDLimitOverride: &rpd,
	})
	if err != nil || result.Outcome != breakerstore.RequestAllowed || result.Session == nil {
		t.Fatalf("acquire result=%+v err=%v", result, err)
	}
	if got := store.acquireInput; got.Fingerprint == "" || got.RPMLimitOverride == nil || *got.RPMLimitOverride != 10 ||
		got.RPDLimitOverride == nil || *got.RPDLimitOverride != 20 ||
		got.RouteRateRevision != 3 || got.GlobalConcurrencyRevision != 4 {
		t.Fatalf("unexpected acquire input: %+v", got)
	}

	ctx := ContextWithRequestSession(context.Background(), result.Session.Request())
	// 绑定不再要求先预占，也不再比较输入估算：TPM 不是准入维度。
	attempt := breakerstore.AcquireAttemptInput{InputEstimate: 123}
	if err := BindAttemptInput(ctx, &attempt); err != nil || attempt.RequestAdmissionID != "request-admission-1" {
		t.Fatalf("bind attempt id=%q err=%v", attempt.RequestAdmissionID, err)
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
	if store.finishCalls != 1 ||
		store.renewEpoch != facts.integrity.Epoch || store.renewRevision != facts.integrity.Revision ||
		store.finishEpoch != facts.integrity.Epoch || store.finishRevision != facts.integrity.Revision {
		t.Fatalf("unexpected lifecycle calls: renew=%d epoch=%s/%d finish=%d epoch=%s/%d",
			store.renewCalls, store.renewEpoch, store.renewRevision,
			store.finishCalls, store.finishEpoch, store.finishRevision)
	}
	store.mu.Unlock()
	facts.mu.Lock()
	defer facts.mu.Unlock()
	// acquire 读一次 admission revision；renew 与 finish 各自强读 integrity。
	if facts.admissionCalls != 1 || facts.integrityCalls < 2 {
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
			RoutingBalance: breakerstore.RoutingBalanceSnapshot{Revision: 6},
		},
	}
	facts := readyFacts()
	manager := NewManager(store, facts, ManagerOptions{RenewInterval: time.Hour})
	result, err := manager.Acquire(context.Background(), Identity{RouteID: 10, UserID: 20, Scope: "POST /v1/responses"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := ContextWithRequestSession(context.Background(), result.Session.Request())
	candidates := []breakerstore.SnapshotCandidateInput{{
		ProviderID: 30, ChannelID: 40, OriginRevision: 2, ProviderStatusRevision: 3,
		ChannelConfigRevision: 4, ChannelCapacityRevision: 5,
	}}
	snapshot, present, err := SnapshotManyIfPresent(ctx, 50, candidates)
	if err != nil || !present || snapshot.RoutingBalance.Revision != 6 {
		t.Fatalf("snapshot=%+v present=%v err=%v", snapshot, present, err)
	}
	store.mu.Lock()
	input := store.snapshotInput
	store.mu.Unlock()
	if input.IntegrityEpoch != facts.admission.Epoch || input.IntegrityRevision != facts.admission.Revision ||
		input.GlobalConcurrencyRevision != 4 ||
		input.CircuitBreakerRevision != 5 || input.RoutingBalanceRevision != 6 || input.ModelID != 50 ||
		len(input.Candidates) != 1 || input.Candidates[0].ChannelID != 40 {
		t.Fatalf("snapshot revisions were not injected correctly: %+v", input)
	}
	if snapshot.RouteRateRevision != 3 {
		t.Fatalf("snapshot did not preserve route rate revision: %+v", snapshot)
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

func TestRequestAdmissionMetricsFollowTokenOwnership(t *testing.T) {
	metrics := &metricsStub{}
	store := &storeStub{
		acquireResult: breakerstore.RequestAdmissionResult{
			Outcome:      breakerstore.RequestAllowed,
			LeaseUntilMs: time.Now().Add(time.Minute).UnixMilli(),
		},
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
	operations, active := metrics.snapshot()
	if active != 1 || operations["acquire/allowed"] != 1 {
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
