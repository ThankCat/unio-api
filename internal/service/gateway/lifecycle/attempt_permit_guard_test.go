package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/core/channel"
	"github.com/ThankCat/unio-gateway/internal/core/requestlog"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/requestadmission"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/runtimefacts"
)

const permitGuardPanicValue = "panic after attempt permit acquire"

type permitGuardPanicLog struct {
	*attemptAuditLog
	panicOnCall int
	createCalls int
	created     []requestlog.CreateAttemptParams
}

func (s *permitGuardPanicLog) CreateAttempt(
	_ context.Context,
	params requestlog.CreateAttemptParams,
) (requestlog.AttemptRecord, error) {
	s.createCalls++
	s.created = append(s.created, params)
	if s.createCalls == s.panicOnCall {
		panic(permitGuardPanicValue)
	}
	return requestlog.AttemptRecord{
		ID:                int64(s.createCalls),
		UpstreamEndpoint: params.UpstreamEndpoint,
	}, nil
}

func TestAttemptRateLimitSkipsCandidateBeforeAttemptAndTransport(t *testing.T) {
	tests := []struct {
		name      string
		endpoint requestlog.Endpoint
		upstream  requestlog.UpstreamEndpoint
	}{
		{name: "chat_completions", endpoint: requestlog.EndpointChatCompletions, upstream: requestlog.UpstreamEndpointChatCompletions},
		{name: "responses", endpoint: requestlog.EndpointResponses, upstream: requestlog.UpstreamEndpointResponses},
		{name: "messages", endpoint: requestlog.EndpointMessages, upstream: requestlog.UpstreamEndpointMessages},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, stream := range []bool{false, true} {
				mode := "non_stream"
				if stream {
					mode = "stream"
				}
				t.Run(mode, func(t *testing.T) {
					log := &permitGuardPanicLog{attemptAuditLog: &attemptAuditLog{}}
					runner, store, _, ctx := newPermitGuardRunner(log)
					runner.lifecycle.endpoint = tc.endpoint
					store.acquireResults = []breakerstore.AttemptAdmission{
						{Mode: breakerstore.AdmissionDenied, Reason: breakerstore.ReasonRateLimited},
						store.acquireResult,
					}

					first := permitGuardCandidate()
					first.Channel.ID = 18
					first.Channel.Name = "Rate Limited"
					second := permitGuardCandidate()
					second.Channel.ID = 19
					second.Channel.Name = "Fallback"
					candidates := []Candidate{
						{RouteIndex: 0, Route: first},
						{RouteIndex: 1, Route: second},
					}
					invokeErr := errors.New("stop after fallback transport")
					var invoked []int64

					var result RunResult
					var err error
					if stream {
						result, err = RunStreamGeneric(ctx, runner, RunStreamParamsGeneric[struct{}]{
							Candidates: candidates,
							Stream: func(_ context.Context, candidate routing.ChatRouteCandidate, _ func(struct{}) error) (*adapter.ResponseFacts, error) {
								invoked = append(invoked, candidate.Channel.ID)
								return nil, invokeErr
							},
							ChunkMeta: func(struct{}) StreamChunkMeta { return StreamChunkMeta{} },
						})
					} else {
						result, err = runner.RunNonStream(ctx, RunNonStreamParams{
							Candidates: candidates,
							Invoke: func(_ context.Context, candidate routing.ChatRouteCandidate) (AttemptSuccess, error) {
								invoked = append(invoked, candidate.Channel.ID)
								return AttemptSuccess{}, invokeErr
							},
						})
					}

					if !errors.Is(err, invokeErr) {
						t.Fatalf("result error = %v, want fallback transport error", err)
					}
					if result.Attempts != 1 || log.createCalls != 1 {
						t.Fatalf("attempts result=%d create_calls=%d, want 1/1", result.Attempts, log.createCalls)
					}
					if !result.RoutingFallback || len(result.TransportChain) != 1 ||
						result.TransportChain[0].ChannelID != second.Channel.ID ||
						result.TransportChain[0].UpstreamEndpoint != tc.upstream {
						t.Fatalf("transport chain must exclude the denied candidate: %+v", result)
					}
					if len(log.created) != 1 || log.created[0].ChannelID != second.Channel.ID {
						t.Fatalf("attempt must belong only to fallback channel %d, got %+v", second.Channel.ID, log.created)
					}
					if log.created[0].UpstreamEndpoint != tc.upstream || store.acquireInput.UpstreamEndpoint != breakerstore.UpstreamEndpoint(tc.upstream) {
						t.Fatalf("upstream endpoint attempt=%q permit=%q, want %q", log.created[0].UpstreamEndpoint, store.acquireInput.UpstreamEndpoint, tc.upstream)
					}
					if len(invoked) != 1 || invoked[0] != second.Channel.ID {
						t.Fatalf("transport calls = %v, want only fallback channel %d", invoked, second.Channel.ID)
					}
					if store.acquireCalls != 2 {
						t.Fatalf("permit acquire calls = %d, want 2", store.acquireCalls)
					}
				})
			}
		})
	}
}

func TestAttemptTraceRecordsCompactFallbackAsTwoSameChannelTransports(t *testing.T) {
	log := &permitGuardPanicLog{attemptAuditLog: &attemptAuditLog{}}
	runner, _, _, ctx := newPermitGuardRunner(log)
	candidate := permitGuardCandidate()
	nativeErr := errors.New("native compact unsupported")
	syntheticErr := errors.New("synthetic compact failed")

	result, err := runner.RunNonStream(ctx, RunNonStreamParams{
		Candidates: []Candidate{{Route: candidate}},
		EndpointForCandidate: func(routing.ChatRouteCandidate) requestlog.UpstreamEndpoint {
			return requestlog.UpstreamEndpointResponsesCompact
		},
		Invoke: func(ctx context.Context, _ routing.ChatRouteCandidate) (AttemptSuccess, error) {
			adapter.MarkTransportStarted(ctx)
			return AttemptSuccess{}, nativeErr
		},
		TransparentFallback: &NonStreamTransparentFallback{
			Match:             func(routing.ChatRouteCandidate, error) bool { return true },
			UpstreamEndpoint: requestlog.UpstreamEndpointChatCompletions,
			Invoke: func(ctx context.Context, _ routing.ChatRouteCandidate) (AttemptSuccess, error) {
				adapter.MarkTransportStarted(ctx)
				return AttemptSuccess{}, syntheticErr
			},
		},
	})
	if !errors.Is(err, syntheticErr) {
		t.Fatalf("result error = %v, want synthetic transport error", err)
	}
	if result.Attempts != 2 || !result.RoutingFallback || len(result.TransportChain) != 2 {
		t.Fatalf("unexpected compact fallback result: %+v", result)
	}
	if result.TransportChain[0].ChannelID != candidate.Channel.ID ||
		result.TransportChain[1].ChannelID != candidate.Channel.ID ||
		result.TransportChain[0].UpstreamEndpoint != requestlog.UpstreamEndpointResponsesCompact ||
		result.TransportChain[1].UpstreamEndpoint != requestlog.UpstreamEndpointChatCompletions {
		t.Fatalf("compact fallback transport order is not truthful: %+v", result.TransportChain)
	}
}

func TestAttemptPermitGuardAbortsCreateAttemptPanic(t *testing.T) {
	t.Run("non_stream", func(t *testing.T) {
		log := &permitGuardPanicLog{attemptAuditLog: &attemptAuditLog{}, panicOnCall: 1}
		runner, store, permitMetrics, ctx := newPermitGuardRunner(log)

		panicValue := capturePermitGuardPanic(func() {
			_, _ = runner.RunNonStream(ctx, RunNonStreamParams{
				Candidates: []Candidate{{Route: permitGuardCandidate()}},
			})
		})

		if panicValue != permitGuardPanicValue {
			t.Fatalf("panic = %#v, want %q", panicValue, permitGuardPanicValue)
		}
		assertPermitGuardTerminals(t, store, permitMetrics, 1, 0, 1)
	})

	t.Run("stream", func(t *testing.T) {
		log := &permitGuardPanicLog{attemptAuditLog: &attemptAuditLog{}, panicOnCall: 1}
		runner, store, permitMetrics, ctx := newPermitGuardRunner(log)

		panicValue := capturePermitGuardPanic(func() {
			_, _ = RunStreamGeneric(ctx, runner, RunStreamParamsGeneric[struct{}]{
				Candidates: []Candidate{{Route: permitGuardCandidate()}},
				ChunkMeta: func(struct{}) StreamChunkMeta {
					return StreamChunkMeta{}
				},
			})
		})

		if panicValue != permitGuardPanicValue {
			t.Fatalf("panic = %#v, want %q", panicValue, permitGuardPanicValue)
		}
		assertPermitGuardTerminals(t, store, permitMetrics, 1, 0, 1)
	})
}

func TestAttemptPermitGuardAbortsCompactFallbackCreateAttemptPanic(t *testing.T) {
	log := &permitGuardPanicLog{attemptAuditLog: &attemptAuditLog{}, panicOnCall: 2}
	runner, store, permitMetrics, ctx := newPermitGuardRunner(log)
	unsupportedErr := errors.New("native compact unsupported")

	panicValue := capturePermitGuardPanic(func() {
		_, _ = runner.RunNonStream(ctx, RunNonStreamParams{
			Candidates: []Candidate{{Route: permitGuardCandidate()}},
			EndpointForCandidate: func(routing.ChatRouteCandidate) requestlog.UpstreamEndpoint {
				return requestlog.UpstreamEndpointResponsesCompact
			},
			Invoke: func(ctx context.Context, _ routing.ChatRouteCandidate) (AttemptSuccess, error) {
				adapter.MarkTransportStarted(ctx)
				return AttemptSuccess{}, unsupportedErr
			},
			TransparentFallback: &NonStreamTransparentFallback{
				Match:             func(routing.ChatRouteCandidate, error) bool { return true },
				UpstreamEndpoint: requestlog.UpstreamEndpointChatCompletions,
				Invoke: func(context.Context, routing.ChatRouteCandidate) (AttemptSuccess, error) {
					t.Fatal("fallback transport must not start after attempt persistence panic")
					return AttemptSuccess{}, nil
				},
			},
		})
	})

	if panicValue != permitGuardPanicValue {
		t.Fatalf("panic = %#v, want %q", panicValue, permitGuardPanicValue)
	}
	if log.createCalls != 2 {
		t.Fatalf("attempt create calls = %d, want 2", log.createCalls)
	}
	assertPermitGuardTerminals(t, store, permitMetrics, 2, 1, 1)
}

func newPermitGuardRunner(log requestlog.Service) (
	*AttemptRunner,
	*attemptPermitStoreStub,
	*attemptPermitMetricsStub,
	context.Context,
) {
	integrity := runtimefacts.Integrity{Epoch: "permit-guard-epoch", Revision: 1}
	store := &attemptPermitStoreStub{
		acquireResult: breakerstore.AttemptAdmission{
			Mode: breakerstore.AdmissionPermit,
			Permit: &breakerstore.AttemptPermit{
				PermitID:          "permit-guard",
				IntegrityEpoch:    integrity.Epoch,
				IntegrityRevision: integrity.Revision,
				PermitTTLMs:       30_000,
				RenewMs:           10_000,
				TerminalTTLMs:     300_000,
			},
		},
		finishResult: breakerstore.FinishResult{
			OriginDisposition: breakerstore.DispositionApplied,
			ChannelDisposition:  breakerstore.DispositionApplied,
		},
	}
	permitMetrics := &attemptPermitMetricsStub{}
	manager := NewAttemptPermitManager(store, attemptRuntimeFactsStub{
		integrity: integrity,
		admission: runtimefacts.AdmissionRevisions{
			Integrity:         integrity,
			RouteRateLimits:   1,
			ChannelRateLimits: 2,
			Concurrency:       1,
		},
		routing: runtimefacts.RoutingRevisions{
			Integrity:      integrity,
			CircuitBreaker: 1,
			RoutingBalance: 1,
		},
	}, AttemptPermitManagerOptions{Metrics: permitMetrics})
	runner := &AttemptRunner{
		lifecycle: &RequestLifecycle{
			requestLog: log,
			endpoint:  requestlog.EndpointResponses,
		},
		retryClassifier: NeverRetryClassifier{},
		permitManager:   manager,
	}
	ctx := requestadmission.ContextWithUsageSession(
		context.Background(),
		&attemptUsageSessionStub{requestID: "request-admission-permit-guard"},
	)
	return runner, store, permitMetrics, ctx
}

func permitGuardCandidate() routing.ChatRouteCandidate {
	return routing.ChatRouteCandidate{
		ModelDBID:                       11,
		ProviderID:                      12,
		ProviderOriginID:              13,
		ProviderOriginBaseURLRevision: 14,
		ProviderOriginStatusRevision:  15,
		ChannelConfigRevision:           16,
		ChannelAdmissionLimitsRevision:  17,
		AdapterKey:                      "permit-guard",
		Protocol:                        routing.ProtocolOpenAI,
		UpstreamModel:                   "permit-guard-model",
		Channel: channel.Runtime{
			ID:           18,
			Name:         "Permit Guard",
			ProviderSlug: "permit-guard",
		},
	}
}

func capturePermitGuardPanic(fn func()) (panicValue any) {
	defer func() {
		panicValue = recover()
	}()
	fn()
	return nil
}

func assertPermitGuardTerminals(
	t *testing.T,
	store *attemptPermitStoreStub,
	permitMetrics *attemptPermitMetricsStub,
	wantAcquire int,
	wantFinish int,
	wantAbort int,
) {
	t.Helper()
	store.mu.Lock()
	acquireCalls := store.acquireCalls
	finishCalls := store.finishCalls
	abortCalls := store.abortCalls
	store.mu.Unlock()
	if acquireCalls != wantAcquire || finishCalls != wantFinish || abortCalls != wantAbort {
		t.Fatalf(
			"permit calls acquire=%d finish=%d abort=%d, want %d/%d/%d",
			acquireCalls,
			finishCalls,
			abortCalls,
			wantAcquire,
			wantFinish,
			wantAbort,
		)
	}

	permitMetrics.mu.Lock()
	active := permitMetrics.active
	permitMetrics.mu.Unlock()
	if active != 0 {
		t.Fatalf("active permit metric = %v, want 0", active)
	}
}
