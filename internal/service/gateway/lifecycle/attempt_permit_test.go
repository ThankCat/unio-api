package lifecycle

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/core/channel"
	"github.com/ThankCat/unio-gateway/internal/core/requestlog"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/core/usage"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/requestadmission"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/runtimefacts"
)

type attemptPermitStoreStub struct {
	mu sync.Mutex

	acquireInput   breakerstore.AcquireAttemptInput
	acquireResult  breakerstore.AttemptAdmission
	acquireResults []breakerstore.AttemptAdmission
	acquireErr     error
	acquireCalls   int
	renewCalls     int
	finishCalls    int
	abortCalls     int
	finishResult   breakerstore.FinishResult
	finishErr      error
	abortErr       error

	cooldownCalls       int
	cooldownChannelID   int64
	cooldownDurationMs  int64
	cooldownSourceMs    int64
	cooldownErr         error
	permissionCalls     int
	permissionChannelID int64
	permissionModelID   int64
	permissionConfigRev int64
	permissionBaseRev   int64
	permissionStatusRev int64
	permissionErr       error
}

func (s *attemptPermitStoreStub) AcquireAttempt(_ context.Context, in breakerstore.AcquireAttemptInput) (breakerstore.AttemptAdmission, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.acquireCalls++
	s.acquireInput = in
	if index := s.acquireCalls - 1; index < len(s.acquireResults) {
		return s.acquireResults[index], nil
	}
	return s.acquireResult, s.acquireErr
}

func (s *attemptPermitStoreStub) Renew(context.Context, breakerstore.AttemptPermit) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.renewCalls++
	return nil
}

func (s *attemptPermitStoreStub) Finish(context.Context, breakerstore.AttemptPermit, breakerstore.FinishOutcome) (breakerstore.FinishResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finishCalls++
	return s.finishResult, s.finishErr
}

func (s *attemptPermitStoreStub) Abort(context.Context, breakerstore.AttemptPermit) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.abortCalls++
	return s.abortErr
}

func (s *attemptPermitStoreStub) SetChannel429Cooldown(_ context.Context, channelID, durationMs, sourceRetryAfterMs int64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cooldownCalls++
	s.cooldownChannelID = channelID
	s.cooldownDurationMs = durationMs
	s.cooldownSourceMs = sourceRetryAfterMs
	return 0, s.cooldownErr
}

func (s *attemptPermitStoreStub) PauseChannelModelPermission(_ context.Context, channelID, modelID, configRev, baseURLRev, statusRev int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.permissionCalls++
	s.permissionChannelID = channelID
	s.permissionModelID = modelID
	s.permissionConfigRev = configRev
	s.permissionBaseRev = baseURLRev
	s.permissionStatusRev = statusRev
	return s.permissionErr
}

type attemptRuntimeFactsStub struct {
	integrity    runtimefacts.Integrity
	integrityErr error
	admission    runtimefacts.AdmissionRevisions
	routing      runtimefacts.RoutingRevisions
}

type attemptPermitMetricsStub struct {
	mu                 sync.Mutex
	operations         map[string]int
	active             float64
	ignored            map[string]int
	channelMismatches  int
	endpointMismatches int
}

func (m *attemptPermitMetricsStub) IncBreakerPermitOperation(operation, result string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.operations == nil {
		m.operations = make(map[string]int)
	}
	m.operations[operation+"/"+result]++
}

func (m *attemptPermitMetricsStub) AddBreakerPermitActive(delta float64) {
	m.mu.Lock()
	m.active += delta
	m.mu.Unlock()
}

func (m *attemptPermitMetricsStub) IncBreakerIgnoredResult(scope, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ignored == nil {
		m.ignored = make(map[string]int)
	}
	m.ignored[scope+"/"+reason]++
}

func (m *attemptPermitMetricsStub) IncChannelConfigRevisionMismatch(string) {
	m.mu.Lock()
	m.channelMismatches++
	m.mu.Unlock()
}

func (m *attemptPermitMetricsStub) IncEndpointStatusRevisionMismatch(string) {
	m.mu.Lock()
	m.endpointMismatches++
	m.mu.Unlock()
}

func (s attemptRuntimeFactsStub) Integrity(context.Context) (runtimefacts.Integrity, error) {
	if s.integrityErr != nil {
		return runtimefacts.Integrity{}, s.integrityErr
	}
	if s.integrity.Epoch != "" || s.integrity.Revision != 0 {
		return s.integrity, nil
	}
	return s.admission.Integrity, nil
}

func (s attemptRuntimeFactsStub) Admission(context.Context) (runtimefacts.AdmissionRevisions, error) {
	return s.admission, nil
}

func (s attemptRuntimeFactsStub) Routing(context.Context) (runtimefacts.RoutingRevisions, error) {
	return s.routing, nil
}

type attemptUsageSessionStub struct {
	requestID string
}

func (s *attemptUsageSessionStub) Reserve(context.Context, int64) error { return nil }
func (s *attemptUsageSessionStub) PublishAuthoritativeUsage(int64) bool { return true }
func (s *attemptUsageSessionStub) BindAttempt(in *breakerstore.AcquireAttemptInput) error {
	in.RequestAdmissionID = s.requestID
	return nil
}

func TestAttemptPermitManagerRequiresOneIntegrityEpoch(t *testing.T) {
	integrity := runtimefacts.Integrity{Epoch: "epoch-a", Revision: 7}
	store := &attemptPermitStoreStub{}
	manager := NewAttemptPermitManager(store, attemptRuntimeFactsStub{
		admission: runtimefacts.AdmissionRevisions{
			Integrity: integrity, RouteRateLimits: 3, ChannelRateLimits: 8, Concurrency: 4,
		},
		routing: runtimefacts.RoutingRevisions{
			Integrity:      runtimefacts.Integrity{Epoch: "epoch-b", Revision: 8},
			CircuitBreaker: 5,
		},
	}, AttemptPermitManagerOptions{})

	_, _, err := manager.Acquire(context.Background(), AttemptPermitAcquireParams{})
	if failure.CodeOf(err) != failure.CodeGatewayRuntimeSyncRequired {
		t.Fatalf("epoch mismatch code = %q, want %q", failure.CodeOf(err), failure.CodeGatewayRuntimeSyncRequired)
	}
	if store.acquireCalls != 0 {
		t.Fatalf("epoch mismatch reached store %d times", store.acquireCalls)
	}
}

func TestAttemptPermitManagerBuildsBoundAuthoritativeInput(t *testing.T) {
	integrity := runtimefacts.Integrity{Epoch: "epoch-ready", Revision: 9}
	store := &attemptPermitStoreStub{acquireResult: breakerstore.AttemptAdmission{
		Mode: breakerstore.AdmissionPermit,
		Permit: &breakerstore.AttemptPermit{
			PermitID: "permit-1", IntegrityEpoch: integrity.Epoch, IntegrityRevision: integrity.Revision,
			PermitTTLMs: 30_000, RenewMs: 10_000, TerminalTTLMs: 300_000,
		},
	}}
	manager := NewAttemptPermitManager(store, attemptRuntimeFactsStub{
		admission: runtimefacts.AdmissionRevisions{
			Integrity: integrity, RouteRateLimits: 3, ChannelRateLimits: 8, Concurrency: 4,
		},
		routing: runtimefacts.RoutingRevisions{Integrity: integrity, CircuitBreaker: 5, RoutingBalance: 6},
	}, AttemptPermitManagerOptions{})
	manager.newPermitID = func() string { return "permit-1" }
	ctx := requestadmission.ContextWithUsageSession(context.Background(), &attemptUsageSessionStub{requestID: "request-token"})
	candidate := routing.ChatRouteCandidate{
		ModelDBID: 11, ProviderEndpointID: 12, ProviderEndpointBaseURLRevision: 13,
		ProviderEndpointStatusRevision: 14, ChannelConfigRevision: 15,
		ChannelAdmissionLimitsRevision: 16, Channel: channel.Runtime{ID: 17},
	}

	_, owner, err := manager.Acquire(ctx, AttemptPermitAcquireParams{
		Candidate: candidate, UpstreamOperation: requestlog.UpstreamOperationResponses,
		RequestMode: breakerstore.ModeNonStream, EstimatedInputTokens: 123,
	})
	if err != nil || owner == nil {
		t.Fatalf("acquire owner=%v err=%v", owner, err)
	}
	if err := owner.Abort(context.Background()); err != nil {
		t.Fatalf("abort owner: %v", err)
	}
	got := store.acquireInput
	if got.RequestAdmissionID != "request-token" || got.IntegrityEpoch != integrity.Epoch || got.IntegrityRevision != integrity.Revision ||
		got.ChannelRateRevision != 8 || got.GlobalConcurrencyRevision != 4 || got.CircuitBreakerRevision != 5 ||
		got.ChannelAdmissionRevision != 16 || got.EstimatedInputTokens != 123 || !got.EnforceEndpointControl ||
		got.AdmissionFingerprint == "" {
		t.Fatalf("unexpected acquire input: %+v", got)
	}

	changed := got
	changed.EstimatedInputTokens++
	if attemptAdmissionFingerprint(got) == attemptAdmissionFingerprint(changed) {
		t.Fatal("estimated tokens must participate in the attempt fingerprint")
	}
}

func TestAttemptPermitMetricsFollowPermitOwnershipAndStaleFinish(t *testing.T) {
	integrity := runtimefacts.Integrity{Epoch: "epoch-ready", Revision: 9}
	metrics := &attemptPermitMetricsStub{}
	store := &attemptPermitStoreStub{
		acquireResult: breakerstore.AttemptAdmission{
			Mode: breakerstore.AdmissionPermit,
			Permit: &breakerstore.AttemptPermit{
				PermitID: "permit-metrics", IntegrityEpoch: integrity.Epoch, IntegrityRevision: integrity.Revision,
				EndpointID: 12, ChannelID: 17, PermitTTLMs: 30_000, RenewMs: 30_000,
			},
		},
		finishResult: breakerstore.FinishResult{
			EndpointDisposition: breakerstore.DispositionStaleStatusRev,
			ChannelDisposition:  breakerstore.DispositionStaleConfigRev,
		},
	}
	manager := NewAttemptPermitManager(store, attemptRuntimeFactsStub{
		admission: runtimefacts.AdmissionRevisions{
			Integrity: integrity, RouteRateLimits: 3, ChannelRateLimits: 8, Concurrency: 4,
		},
		routing: runtimefacts.RoutingRevisions{Integrity: integrity, CircuitBreaker: 5, RoutingBalance: 6},
	}, AttemptPermitManagerOptions{Metrics: metrics})
	manager.newPermitID = func() string { return "permit-metrics" }
	ctx := requestadmission.ContextWithUsageSession(context.Background(), &attemptUsageSessionStub{requestID: "request-token"})
	_, owner, err := manager.Acquire(ctx, AttemptPermitAcquireParams{
		Candidate: routing.ChatRouteCandidate{
			ModelDBID: 11, ProviderEndpointID: 12, ProviderEndpointBaseURLRevision: 13,
			ProviderEndpointStatusRevision: 14, ChannelConfigRevision: 15,
			ChannelAdmissionLimitsRevision: 16, Channel: channel.Runtime{ID: 17},
		},
		UpstreamOperation: requestlog.UpstreamOperationResponses,
		RequestMode:       breakerstore.ModeNonStream,
	})
	if err != nil || owner == nil {
		t.Fatalf("acquire owner=%v err=%v", owner, err)
	}
	if _, err := owner.Finish(context.Background(), breakerstore.FinishOutcome{
		EndpointOutcome: breakerstore.OutcomeIgnored,
		ChannelOutcome:  breakerstore.OutcomeIgnored,
	}); err != nil {
		t.Fatalf("finish: %v", err)
	}
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	if metrics.active != 0 || metrics.operations["acquire/permit"] != 1 || metrics.operations["finish/mixed"] != 1 {
		t.Fatalf("active=%v operations=%v", metrics.active, metrics.operations)
	}
	if metrics.ignored["endpoint/stale_status_revision"] != 1 || metrics.ignored["channel/stale_config_revision"] != 1 ||
		metrics.endpointMismatches != 1 || metrics.channelMismatches != 1 {
		t.Fatalf("ignored=%v endpoint_mismatch=%d channel_mismatch=%d", metrics.ignored, metrics.endpointMismatches, metrics.channelMismatches)
	}
}

func newRuntimeFeedbackOwner(
	store *attemptPermitStoreStub,
	policy *channel429CooldownPolicy,
) *AttemptPermitOwner {
	permit := breakerstore.AttemptPermit{
		PermitID: "permit-feedback", IntegrityEpoch: "epoch-ready", IntegrityRevision: 1,
		ChannelID: 17, ModelID: 23, ChannelConfigRevision: 31,
		EndpointBaseURLRevision: 37, EndpointStatusRevision: 41,
		PermitTTLMs: 30_000, RenewMs: 10_000,
	}
	return newAttemptPermitOwnerWithFeedback(
		store,
		store,
		policy,
		attemptRuntimeFactsStub{integrity: runtimefacts.Integrity{
			Epoch: permit.IntegrityEpoch, Revision: permit.IntegrityRevision,
		}},
		permit,
		zap.NewNop(),
		100*time.Millisecond,
	)
}

func feedbackUpstreamError(category adapter.UpstreamErrorCategory, status int, retryAfter time.Duration) error {
	return adapter.NewUpstreamError(
		category,
		adapter.UpstreamMetadata{StatusCode: status, RetryAfter: retryAfter},
		failure.New(failure.CodeAdapterUpstreamStatus),
	)
}

func TestAttemptPermitOwnerRecords429CooldownUsingPolicy(t *testing.T) {
	tests := []struct {
		name           string
		retryAfter     time.Duration
		defaultValue   time.Duration
		cap            time.Duration
		wantCalls      int
		wantDurationMs int64
		wantSourceMs   int64
	}{
		{name: "retry after", retryAfter: 20 * time.Second, defaultValue: 5 * time.Second, cap: time.Minute, wantCalls: 1, wantDurationMs: 20_000, wantSourceMs: 20_000},
		{name: "default", defaultValue: 5 * time.Second, cap: time.Minute, wantCalls: 1, wantDurationMs: 5_000},
		{name: "cap", retryAfter: 2 * time.Minute, defaultValue: 5 * time.Second, cap: time.Minute, wantCalls: 1, wantDurationMs: 60_000, wantSourceMs: 120_000},
		{name: "disabled without retry after", cap: time.Minute},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := &attemptPermitStoreStub{}
			policy := &channel429CooldownPolicy{}
			policy.Set(tc.defaultValue, tc.cap)
			owner := newRuntimeFeedbackOwner(store, policy)

			_, err := owner.FinishTransport(
				context.Background(),
				breakerstore.FinishOutcome{EndpointOutcome: breakerstore.OutcomeIgnored, ChannelOutcome: breakerstore.OutcomeIgnored},
				feedbackUpstreamError(adapter.UpstreamErrorRateLimit, 429, tc.retryAfter),
			)
			if err != nil {
				t.Fatalf("finish transport: %v", err)
			}
			if store.finishCalls != 1 || store.cooldownCalls != tc.wantCalls {
				t.Fatalf("calls finish=%d cooldown=%d, want 1/%d", store.finishCalls, store.cooldownCalls, tc.wantCalls)
			}
			if tc.wantCalls == 1 && (store.cooldownChannelID != 17 || store.cooldownDurationMs != tc.wantDurationMs || store.cooldownSourceMs != tc.wantSourceMs) {
				t.Fatalf("unexpected cooldown feedback: channel=%d duration=%d source=%d", store.cooldownChannelID, store.cooldownDurationMs, store.cooldownSourceMs)
			}
		})
	}
}

func TestAttemptPermitOwnerRecords403PermissionWithPermitRevisions(t *testing.T) {
	store := &attemptPermitStoreStub{}
	owner := newRuntimeFeedbackOwner(store, &channel429CooldownPolicy{})

	_, err := owner.FinishTransport(
		context.Background(),
		breakerstore.FinishOutcome{EndpointOutcome: breakerstore.OutcomeIgnored, ChannelOutcome: breakerstore.OutcomeIgnored},
		feedbackUpstreamError(adapter.UpstreamErrorPermission, 403, 0),
	)
	if err != nil {
		t.Fatalf("finish transport: %v", err)
	}
	if store.permissionCalls != 1 || store.permissionChannelID != 17 || store.permissionModelID != 23 ||
		store.permissionConfigRev != 31 || store.permissionBaseRev != 37 || store.permissionStatusRev != 41 {
		t.Fatalf("unexpected permission feedback: %+v", store)
	}
}

func TestAttemptPermitOwnerFeedbackIsExactAndFirstTerminalWins(t *testing.T) {
	tests := []struct {
		name     string
		category adapter.UpstreamErrorCategory
		status   int
	}{
		{name: "rate category wrong status", category: adapter.UpstreamErrorRateLimit, status: 503},
		{name: "permission category wrong status", category: adapter.UpstreamErrorPermission, status: 429},
		{name: "server status", category: adapter.UpstreamErrorServer, status: 503},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := &attemptPermitStoreStub{}
			policy := &channel429CooldownPolicy{}
			policy.Set(5*time.Second, time.Minute)
			owner := newRuntimeFeedbackOwner(store, policy)
			_, err := owner.FinishTransport(
				context.Background(),
				breakerstore.FinishOutcome{EndpointOutcome: breakerstore.OutcomeIgnored, ChannelOutcome: breakerstore.OutcomeIgnored},
				feedbackUpstreamError(tc.category, tc.status, time.Second),
			)
			if err != nil {
				t.Fatalf("finish transport: %v", err)
			}
			if store.cooldownCalls != 0 || store.permissionCalls != 0 {
				t.Fatalf("mismatched feedback was recorded: cooldown=%d permission=%d", store.cooldownCalls, store.permissionCalls)
			}
		})
	}

	store := &attemptPermitStoreStub{}
	policy := &channel429CooldownPolicy{}
	policy.Set(5*time.Second, time.Minute)
	owner := newRuntimeFeedbackOwner(store, policy)
	rateErr := feedbackUpstreamError(adapter.UpstreamErrorRateLimit, 429, time.Second)
	for i := 0; i < 2; i++ {
		if _, err := owner.FinishTransport(
			context.Background(),
			breakerstore.FinishOutcome{EndpointOutcome: breakerstore.OutcomeIgnored, ChannelOutcome: breakerstore.OutcomeIgnored},
			rateErr,
		); err != nil {
			t.Fatalf("finish transport %d: %v", i, err)
		}
	}
	if store.finishCalls != 1 || store.cooldownCalls != 1 {
		t.Fatalf("duplicate terminal calls finish=%d cooldown=%d, want 1/1", store.finishCalls, store.cooldownCalls)
	}
}

type attemptIntegrityFactsSpy struct {
	mu        sync.Mutex
	integrity runtimefacts.Integrity
	err       error
	calls     int
}

func (s *attemptIntegrityFactsSpy) Integrity(context.Context) (runtimefacts.Integrity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.integrity, s.err
}

func (s *attemptIntegrityFactsSpy) Admission(context.Context) (runtimefacts.AdmissionRevisions, error) {
	return runtimefacts.AdmissionRevisions{}, errors.New("unexpected admission read")
}

func (s *attemptIntegrityFactsSpy) Routing(context.Context) (runtimefacts.RoutingRevisions, error) {
	return runtimefacts.RoutingRevisions{}, errors.New("unexpected routing read")
}

func (s *attemptIntegrityFactsSpy) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func TestAttemptPermitOwnerRejectsStalePostgresEpochBeforeRedis(t *testing.T) {
	tests := []struct {
		name       string
		invoke     func(context.Context, *AttemptPermitOwner) error
		storeCalls func(*attemptPermitStoreStub) int
	}{
		{
			name: "renew",
			invoke: func(ctx context.Context, owner *AttemptPermitOwner) error {
				err := owner.renew(ctx)
				owner.stopRenewer()
				return err
			},
			storeCalls: func(store *attemptPermitStoreStub) int { return store.renewCalls },
		},
		{
			name: "finish",
			invoke: func(ctx context.Context, owner *AttemptPermitOwner) error {
				_, err := owner.Finish(ctx, breakerstore.FinishOutcome{
					EndpointOutcome: breakerstore.OutcomeIgnored,
					ChannelOutcome:  breakerstore.OutcomeIgnored,
				})
				return err
			},
			storeCalls: func(store *attemptPermitStoreStub) int { return store.finishCalls },
		},
		{
			name: "abort",
			invoke: func(ctx context.Context, owner *AttemptPermitOwner) error {
				return owner.Abort(ctx)
			},
			storeCalls: func(store *attemptPermitStoreStub) int { return store.abortCalls },
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := &attemptPermitStoreStub{}
			facts := &attemptIntegrityFactsSpy{integrity: runtimefacts.Integrity{Epoch: "epoch-new", Revision: 2}}
			permit := breakerstore.AttemptPermit{
				PermitID: "permit", IntegrityEpoch: "epoch-old", IntegrityRevision: 1,
				PermitTTLMs: 30_000, RenewMs: 30_000,
			}
			owner := newAttemptPermitOwner(store, facts, permit, zap.NewNop(), 100*time.Millisecond)

			err := tc.invoke(context.Background(), owner)
			if !errors.Is(err, breakerstore.ErrStaleIntegrityEpoch) ||
				failure.CodeOf(err) != failure.CodeGatewayRuntimeSyncRequired {
				t.Fatalf("unexpected stale epoch error: code=%q err=%v", failure.CodeOf(err), err)
			}
			if got := tc.storeCalls(store); got != 0 {
				t.Fatalf("stale PostgreSQL epoch reached Redis store %d times", got)
			}
			if got := facts.callCount(); got != 1 {
				t.Fatalf("integrity reads = %d, want 1", got)
			}
		})
	}
}

func TestAttemptPermitOwnerRenewsLongTransportAndStopsBeforeFinish(t *testing.T) {
	integrity := runtimefacts.Integrity{Epoch: "epoch-long-stream", Revision: 1}
	store := &attemptPermitStoreStub{
		finishResult: breakerstore.FinishResult{
			EndpointDisposition: breakerstore.DispositionApplied,
			ChannelDisposition:  breakerstore.DispositionApplied,
		},
	}
	owner := newAttemptPermitOwner(
		store,
		attemptRuntimeFactsStub{integrity: integrity},
		breakerstore.AttemptPermit{
			PermitID:          "permit-long-stream",
			IntegrityEpoch:    integrity.Epoch,
			IntegrityRevision: integrity.Revision,
			PermitTTLMs:       90,
			RenewMs:           10,
		},
		zap.NewNop(),
		100*time.Millisecond,
	)

	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		store.mu.Lock()
		renewCalls := store.renewCalls
		store.mu.Unlock()
		if renewCalls >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("long transport renew calls = %d, want at least 2", renewCalls)
		}
		time.Sleep(5 * time.Millisecond)
	}

	if _, err := owner.Finish(context.Background(), breakerstore.FinishOutcome{
		EndpointOutcome: breakerstore.OutcomeEligibleSuccess,
		ChannelOutcome:  breakerstore.OutcomeEligibleSuccess,
	}); err != nil {
		t.Fatalf("finish long transport: %v", err)
	}
	store.mu.Lock()
	renewCallsAfterFinish := store.renewCalls
	finishCalls := store.finishCalls
	store.mu.Unlock()
	if finishCalls != 1 {
		t.Fatalf("finish calls = %d, want 1", finishCalls)
	}

	time.Sleep(3 * minimumAttemptPermitRenewInterval)
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.renewCalls != renewCallsAfterFinish {
		t.Fatalf("renewer continued after terminal: before=%d after=%d", renewCallsAfterFinish, store.renewCalls)
	}
}

type attemptAuditLog struct {
	captureAttemptFailedLog
	timing      requestlog.RecordAttemptTimingParams
	disposition requestlog.RecordAttemptBreakerDispositionParams
}

func (s *attemptAuditLog) RecordAttemptTiming(_ context.Context, params requestlog.RecordAttemptTimingParams) (requestlog.AttemptRecord, error) {
	s.timing = params
	return requestlog.AttemptRecord{ID: params.ID}, nil
}

func (s *attemptAuditLog) RecordAttemptBreakerDisposition(_ context.Context, params requestlog.RecordAttemptBreakerDispositionParams) (requestlog.AttemptRecord, error) {
	s.disposition = params
	return requestlog.AttemptRecord{ID: params.ID}, nil
}

func TestInvokeNonStreamAttemptUsesTransportBoundary(t *testing.T) {
	tests := []struct {
		name       string
		start      bool
		wantFinish int
		wantAbort  int
	}{
		{name: "pre transport", wantAbort: 1},
		{name: "transport started", start: true, wantFinish: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := &attemptPermitStoreStub{finishResult: breakerstore.FinishResult{
				EndpointDisposition: breakerstore.DispositionApplied,
				ChannelDisposition:  breakerstore.DispositionApplied,
			}}
			permit := breakerstore.AttemptPermit{
				PermitID: "permit", IntegrityEpoch: "epoch-ready", IntegrityRevision: 1,
				PermitTTLMs: 30_000, RenewMs: 10_000,
			}
			owner := newAttemptPermitOwner(store, attemptRuntimeFactsStub{
				integrity: runtimefacts.Integrity{Epoch: permit.IntegrityEpoch, Revision: permit.IntegrityRevision},
			}, permit, zap.NewNop(), 100*time.Millisecond)
			audit := &attemptAuditLog{}
			runner := &AttemptRunner{lifecycle: &RequestLifecycle{requestLog: audit}}
			invokeErr := errors.New("invoke failed")

			_, gotErr := runner.invokeNonStreamAttempt(
				context.Background(),
				routing.ChatRouteCandidate{Channel: channel.Runtime{ID: 17}},
				requestlog.AttemptRecord{ID: 42},
				owner,
				func(ctx context.Context, _ routing.ChatRouteCandidate) (AttemptSuccess, error) {
					if tc.start {
						adapter.MarkTransportStarted(ctx)
					}
					return AttemptSuccess{}, invokeErr
				},
			)
			if !errors.Is(gotErr, invokeErr) {
				t.Fatalf("invoke error = %v, want %v", gotErr, invokeErr)
			}
			if store.finishCalls != tc.wantFinish || store.abortCalls != tc.wantAbort {
				t.Fatalf("terminal calls finish=%d abort=%d, want %d/%d", store.finishCalls, store.abortCalls, tc.wantFinish, tc.wantAbort)
			}
			if tc.start && (audit.timing.UpstreamStartedAt == nil || audit.timing.UpstreamCompletedAt == nil) {
				t.Fatalf("transport timing was not persisted: %+v", audit.timing)
			}
			if !tc.start && (audit.timing.UpstreamStartedAt != nil || audit.timing.UpstreamCompletedAt != nil) {
				t.Fatalf("pre-transport attempt gained transport timing: %+v", audit.timing)
			}
		})
	}
}

func TestInvokeNonStreamAttemptDoesNotFeedbackBeforeTransport(t *testing.T) {
	store := &attemptPermitStoreStub{}
	policy := &channel429CooldownPolicy{}
	policy.Set(5*time.Second, time.Minute)
	owner := newRuntimeFeedbackOwner(store, policy)
	runner := &AttemptRunner{lifecycle: &RequestLifecycle{requestLog: &attemptAuditLog{}}}
	rateErr := feedbackUpstreamError(adapter.UpstreamErrorRateLimit, 429, time.Second)

	_, err := runner.invokeNonStreamAttempt(
		context.Background(),
		routing.ChatRouteCandidate{Channel: channel.Runtime{ID: 17}},
		requestlog.AttemptRecord{ID: 42},
		owner,
		func(context.Context, routing.ChatRouteCandidate) (AttemptSuccess, error) {
			return AttemptSuccess{}, rateErr
		},
	)
	if !errors.Is(err, rateErr) {
		t.Fatalf("invoke error = %v, want upstream rate error", err)
	}
	if store.abortCalls != 1 || store.finishCalls != 0 || store.cooldownCalls != 0 {
		t.Fatalf("pre-transport calls abort=%d finish=%d cooldown=%d, want 1/0/0", store.abortCalls, store.finishCalls, store.cooldownCalls)
	}
}

func TestInvokeNonStreamAttemptFailsClosedOnRuntimeFeedbackError(t *testing.T) {
	store := &attemptPermitStoreStub{
		finishResult: breakerstore.FinishResult{
			EndpointDisposition: breakerstore.DispositionApplied,
			ChannelDisposition:  breakerstore.DispositionApplied,
		},
		cooldownErr: breakerstore.ErrStoreUnavailable,
	}
	policy := &channel429CooldownPolicy{}
	policy.Set(5*time.Second, time.Minute)
	owner := newRuntimeFeedbackOwner(store, policy)
	audit := &attemptAuditLog{}
	runner := &AttemptRunner{lifecycle: &RequestLifecycle{requestLog: audit}}

	_, err := runner.invokeNonStreamAttempt(
		context.Background(),
		routing.ChatRouteCandidate{Channel: channel.Runtime{ID: 17}},
		requestlog.AttemptRecord{ID: 42},
		owner,
		func(ctx context.Context, _ routing.ChatRouteCandidate) (AttemptSuccess, error) {
			adapter.MarkTransportStarted(ctx)
			return AttemptSuccess{}, feedbackUpstreamError(adapter.UpstreamErrorRateLimit, 429, time.Second)
		},
	)
	if !errors.Is(err, ErrAttemptRuntimeFeedback) || failure.CodeOf(err) != failure.CodeGatewayBreakerStoreUnavailable {
		t.Fatalf("feedback error code=%q err=%v", failure.CodeOf(err), err)
	}
	if store.finishCalls != 1 || store.cooldownCalls != 1 {
		t.Fatalf("feedback failure calls finish=%d cooldown=%d, want 1/1", store.finishCalls, store.cooldownCalls)
	}
	if audit.disposition.EndpointDisposition != string(breakerstore.DispositionApplied) ||
		audit.disposition.ChannelDisposition != string(breakerstore.DispositionApplied) {
		t.Fatalf("confirmed Finish disposition was lost: %+v", audit.disposition)
	}
}

func TestInvokeNonStreamAttemptAuditsUnknownFinishResult(t *testing.T) {
	store := &attemptPermitStoreStub{finishErr: breakerstore.ErrStoreUnavailable}
	permit := breakerstore.AttemptPermit{
		PermitID: "permit", IntegrityEpoch: "epoch-ready", IntegrityRevision: 1,
		PermitTTLMs: 30_000, RenewMs: 10_000,
	}
	owner := newAttemptPermitOwner(store, attemptRuntimeFactsStub{
		integrity: runtimefacts.Integrity{Epoch: permit.IntegrityEpoch, Revision: permit.IntegrityRevision},
	}, permit, zap.NewNop(), 100*time.Millisecond)
	audit := &attemptAuditLog{}
	runner := &AttemptRunner{lifecycle: &RequestLifecycle{requestLog: audit}}

	_, err := runner.invokeNonStreamAttempt(
		context.Background(),
		routing.ChatRouteCandidate{Channel: channel.Runtime{ID: 17}},
		requestlog.AttemptRecord{ID: 42},
		owner,
		func(ctx context.Context, _ routing.ChatRouteCandidate) (AttemptSuccess, error) {
			adapter.MarkTransportStarted(ctx)
			return AttemptSuccess{}, nil
		},
	)
	if err != nil {
		t.Fatalf("business result changed by finish audit failure: %v", err)
	}
	if store.finishCalls != attemptPermitTerminalTries {
		t.Fatalf("finish calls = %d, want %d", store.finishCalls, attemptPermitTerminalTries)
	}
	if audit.disposition.EndpointDisposition != string(breakerstore.DispositionResultUnknown) ||
		audit.disposition.ChannelDisposition != string(breakerstore.DispositionResultUnknown) {
		t.Fatalf("unexpected disposition audit: %+v", audit.disposition)
	}
}

func TestInvokeNonStreamAttemptStopsFallbackWhenFailedTransportFinishIsUnknown(t *testing.T) {
	store := &attemptPermitStoreStub{finishErr: breakerstore.ErrStoreUnavailable}
	permit := breakerstore.AttemptPermit{
		PermitID: "permit", IntegrityEpoch: "epoch-ready", IntegrityRevision: 1,
		PermitTTLMs: 30_000, RenewMs: 10_000,
	}
	owner := newAttemptPermitOwner(store, attemptRuntimeFactsStub{
		integrity: runtimefacts.Integrity{Epoch: permit.IntegrityEpoch, Revision: permit.IntegrityRevision},
	}, permit, zap.NewNop(), 100*time.Millisecond)
	audit := &attemptAuditLog{}
	runner := &AttemptRunner{lifecycle: &RequestLifecycle{requestLog: audit}}
	upstreamErr := feedbackUpstreamError(adapter.UpstreamErrorServer, 503, 0)

	_, err := runner.invokeNonStreamAttempt(
		context.Background(),
		routing.ChatRouteCandidate{Channel: channel.Runtime{ID: 17}},
		requestlog.AttemptRecord{ID: 42},
		owner,
		func(ctx context.Context, _ routing.ChatRouteCandidate) (AttemptSuccess, error) {
			adapter.MarkTransportStarted(ctx)
			return AttemptSuccess{}, upstreamErr
		},
	)
	if !errors.Is(err, errAttemptPermitFinish) || !errors.Is(err, upstreamErr) ||
		failure.CodeOf(err) != failure.CodeGatewayBreakerStoreUnavailable {
		t.Fatalf("finish failure code=%q err=%v", failure.CodeOf(err), err)
	}
	if audit.disposition.EndpointDisposition != string(breakerstore.DispositionResultUnknown) ||
		audit.disposition.ChannelDisposition != string(breakerstore.DispositionResultUnknown) {
		t.Fatalf("unexpected disposition audit: %+v", audit.disposition)
	}
}

func TestNonStreamFinishAttributionIsConservative(t *testing.T) {
	serverError := adapter.NewUpstreamError(
		adapter.UpstreamErrorServer,
		adapter.UpstreamMetadata{StatusCode: 503},
		failure.New(failure.CodeAdapterUpstreamStatus),
	)
	clientError := adapter.NewUpstreamError(
		adapter.UpstreamErrorBadRequest,
		adapter.UpstreamMetadata{StatusCode: 400},
		failure.New(failure.CodeAdapterUpstreamStatus),
	)
	protocolError := adapter.NewUpstreamError(
		adapter.UpstreamErrorUnknown,
		adapter.UpstreamMetadata{StatusCode: 200},
		failure.New(failure.CodeAdapterInvalidResponse),
	)
	http500Error := adapter.NewUpstreamError(
		adapter.UpstreamErrorServer,
		adapter.UpstreamMetadata{StatusCode: 500},
		failure.New(failure.CodeAdapterUpstreamStatus),
	)
	bodyTimeout := adapter.NewUpstreamError(
		adapter.UpstreamErrorTimeout,
		adapter.UpstreamMetadata{},
		failure.New(failure.CodeAdapterReadStreamFailed),
	)
	canceledBodyRead := failure.Wrap(
		failure.CodeAdapterReadStreamFailed,
		context.Canceled,
		failure.WithMessage("read canceled response body"),
	)
	tests := []struct {
		name     string
		err      error
		endpoint breakerstore.Outcome
		channel  breakerstore.Outcome
		evidence breakerstore.EndpointEvidenceCategory
	}{
		{name: "gateway status", err: serverError, endpoint: breakerstore.OutcomeEligibleFailure, channel: breakerstore.OutcomeEligibleFailure},
		{name: "http 500", err: http500Error, channel: breakerstore.OutcomeEligibleFailure, evidence: breakerstore.EndpointEvidenceHTTP500},
		{name: "body timeout", err: bodyTimeout, channel: breakerstore.OutcomeEligibleFailure, evidence: breakerstore.EndpointEvidenceBodyReadTimeout},
		{name: "client", err: clientError, channel: breakerstore.OutcomeIgnored},
		{name: "protocol", err: protocolError, channel: breakerstore.OutcomeEligibleFailure},
		{name: "canceled", err: context.Canceled, channel: breakerstore.OutcomeIgnored},
		{name: "wrapped canceled body read", err: canceledBodyRead, channel: breakerstore.OutcomeIgnored},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := nonStreamFinishOutcome(AttemptSuccess{}, AttemptTimingFacts{}, tc.err)
			wantEndpoint := tc.endpoint
			if wantEndpoint == "" {
				wantEndpoint = breakerstore.OutcomeIgnored
			}
			if got.EndpointOutcome != wantEndpoint || got.ChannelOutcome != tc.channel ||
				got.EndpointEvidence != tc.evidence || got.FirstTokenMs != nil {
				t.Fatalf("unexpected attribution: %+v", got)
			}
		})
	}
}

func TestStreamFinishTimeoutEvidenceUsesFirstTokenTiming(t *testing.T) {
	started := time.Now()
	firstToken := started.Add(250 * time.Millisecond)
	completed := started.Add(time.Second)
	timeoutErr := adapter.NewUpstreamError(
		adapter.UpstreamErrorTimeout,
		adapter.UpstreamMetadata{},
		failure.New(failure.CodeAdapterStreamIdleTimeout),
	)

	tests := []struct {
		name     string
		first    *time.Time
		evidence breakerstore.EndpointEvidenceCategory
	}{
		{name: "first token", evidence: breakerstore.EndpointEvidenceFirstTokenTimeout},
		{name: "body", first: &firstToken, evidence: breakerstore.EndpointEvidenceBodyReadTimeout},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := streamFinishOutcome(nil, AttemptTimingFacts{
				UpstreamStartedAt: &started, UpstreamFirstTokenAt: tc.first, UpstreamCompletedAt: &completed,
			}, timeoutErr)
			if got.ChannelOutcome != breakerstore.OutcomeEligibleFailure || got.EndpointOutcome != breakerstore.OutcomeIgnored || got.EndpointEvidence != tc.evidence {
				t.Fatalf("unexpected stream timeout attribution: %+v", got)
			}
		})
	}
}

func TestStreamFinishUsesAuthoritativeTPMWhenTailFails(t *testing.T) {
	facts := &adapter.ResponseFacts{
		Usage: usage.Facts{
			UncachedInputTokens: usage.KnownTokens(11),
			OutputTokensTotal:   usage.KnownTokens(7),
		},
		UsageSource: usage.SourceUpstreamStream,
	}
	got := streamFinishOutcome(facts, AttemptTimingFacts{}, errors.New("stream tail failed"))
	if got.ChannelTPMActual == nil || *got.ChannelTPMActual != 18 {
		t.Fatalf("authoritative TPM was not reconciled: %+v", got)
	}
	if got.ChannelOutcome != breakerstore.OutcomeIgnored {
		t.Fatalf("plain local tail error must not become an upstream failure: %+v", got)
	}
}
