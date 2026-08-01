package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/ThankCat/unio-gateway/internal/core/requestlog"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

type fakeDiagnosticRoutingTraceStore struct {
	fakeRoutingTraceStore
	pool []sqlc.RouteRuntimePoolRow
}

func (s *fakeDiagnosticRoutingTraceStore) RouteRuntimePool(context.Context, sqlc.RouteRuntimePoolParams) ([]sqlc.RouteRuntimePoolRow, error) {
	return s.pool, nil
}

type fakeRoutingTraceStore struct {
	writes []sqlc.UpsertRoutingDecisionTraceParams
	err    error
}

type fakeRoutingTraceMetrics struct {
	results []string
}

func (m *fakeRoutingTraceMetrics) IncRoutingTraceWrite(result string) {
	m.results = append(m.results, result)
}

func (s *fakeRoutingTraceStore) UpsertRoutingDecisionTrace(_ context.Context, in sqlc.UpsertRoutingDecisionTraceParams) error {
	s.writes = append(s.writes, in)
	return s.err
}

func TestRoutingTraceRecorderWritesEveryRequestAndCompletesTrace(t *testing.T) {
	store := &fakeRoutingTraceStore{}
	recorder := NewRoutingTraceRecorder(store, zap.NewNop())
	traceMetrics := &fakeRoutingTraceMetrics{}
	recorder.SetMetrics(traceMetrics)
	request := requestlog.RequestRecord{
		ID: 1, RequestID: "req-complete-trace", RequestedModelID: "openai/gpt",
		IngressProtocol: requestlog.ProtocolOpenAI, Endpoint: requestlog.EndpointChatCompletions,
	}
	plan := CandidatePlan{Candidates: []Candidate{{Route: candidateRoute(7, "openai"), Balance: BalanceScore{ConcurrencyScore: 50, FinalScore: 90}}}}

	recorder.Record(context.Background(), RoutingDecisionTraceInput{Request: request, RouteID: 3, Mode: "balanced", PoolSize: 1, Plan: plan})
	if len(store.writes) != 1 {
		t.Fatalf("every planned request must create a trace, writes=%d", len(store.writes))
	}
	if store.writes[0].TraceStatus != string(TraceStatusPartial) {
		t.Fatalf("initial trace must be partial: %+v", store.writes[0])
	}

	plan.Candidates = append(plan.Candidates, Candidate{Route: candidateRoute(8, "openai")})
	recorder.Record(context.Background(), RoutingDecisionTraceInput{
		Request: request, RouteID: 3, Mode: "balanced", PoolSize: 2, Plan: plan,
		Status: TraceStatusComplete, SelectedChannelID: 7, FinalResult: "success",
		ActualScanOrder: []int64{7}, AttemptedChannelIDs: []int64{7},
		FallbackOccurred: true,
		FallbackChain: []TransportAttempt{
			{ChannelID: 7, UpstreamEndpoint: requestlog.UpstreamEndpointResponsesCompact},
			{ChannelID: 7, UpstreamEndpoint: requestlog.UpstreamEndpointChatCompletions},
		},
	})
	if len(store.writes) != 2 {
		t.Fatalf("completion must upsert the same request trace, writes=%d", len(store.writes))
	}
	if len(traceMetrics.results) != 2 || traceMetrics.results[0] != "success" || traceMetrics.results[1] != "success" {
		t.Fatalf("unexpected trace write metrics: %+v", traceMetrics.results)
	}
	got := store.writes[1]
	if got.TraceStatus != string(TraceStatusComplete) || got.FinalResult.String != "success" ||
		!got.SelectedChannelID.Valid || got.SelectedChannelID.Int64 != 7 {
		t.Fatalf("completion facts missing from trace: %+v", got)
	}
	if got.AlgorithmVersion != "objective_v1" {
		t.Fatalf("algorithm version = %q, want objective_v1", got.AlgorithmVersion)
	}
	var payload tracePayload
	if err := json.Unmarshal(got.TracePayload, &payload); err != nil {
		t.Fatalf("decode trace payload: %v", err)
	}
	if len(payload.AbnormalReasons) != 1 || payload.AbnormalReasons[0] != "fallback" {
		t.Fatalf("unexpected fallback trace: %+v", payload)
	}
	chain := payload.Attempts
	if len(chain) != 2 || chain[0].ChannelID != 7 || chain[1].ChannelID != 7 ||
		chain[0].UpstreamEndpoint != requestlog.UpstreamEndpointResponsesCompact ||
		chain[1].UpstreamEndpoint != requestlog.UpstreamEndpointChatCompletions {
		t.Fatalf("same-channel transport attempts lost from trace: %+v", chain)
	}
}

func TestRoutingTraceLogsPlanAndCompletionOnce(t *testing.T) {
	core, observed := observer.New(zapcore.DebugLevel)
	recorder := NewRoutingTraceRecorder(&fakeRoutingTraceStore{}, zap.New(core))
	request := requestlog.RequestRecord{ID: 1, RequestID: "req-log-once", RequestedModelID: "openai/gpt"}
	in := RoutingDecisionTraceInput{
		Request: request,
		RouteID: 3,
		Mode:    "balanced",
		Plan: CandidatePlan{Candidates: []Candidate{
			{Route: candidateRoute(7, "openai")},
		}},
	}
	recorder.Record(context.Background(), in)
	in.Status = TraceStatusComplete
	in.SelectedChannelID = 7
	in.FinalResult = FinalResultSuccess
	in.FallbackChain = []TransportAttempt{{ChannelID: 7}}
	recorder.complete(context.Background(), in)

	if got := observed.FilterMessage("routing plan created").Len(); got != 1 {
		t.Fatalf("routing plan log count = %d", got)
	}
	if got := observed.FilterMessage("routing completed").Len(); got != 1 {
		t.Fatalf("routing completion log count = %d", got)
	}
}

func TestRoutingTraceFallbackChainUsesActualTransportAfterAdmissionSkip(t *testing.T) {
	store := &fakeRoutingTraceStore{}
	recorder := NewRoutingTraceRecorder(store, zap.NewNop())
	plan := CandidatePlan{Candidates: []Candidate{
		{Route: candidateRoute(7, "openai")},
		{Route: candidateRoute(8, "openai")},
	}}
	recorder.Record(context.Background(), RoutingDecisionTraceInput{
		Request: requestlog.RequestRecord{
			ID: 3, RequestID: "req-admission-skip", RequestedModelID: "openai/gpt",
			IngressProtocol: requestlog.ProtocolOpenAI, Endpoint: requestlog.EndpointResponses,
		},
		RouteID: 3, Mode: "balanced", PoolSize: 2, Plan: plan,
		FallbackOccurred: true,
		FallbackChain: []TransportAttempt{{
			ChannelID: 8, UpstreamEndpoint: requestlog.UpstreamEndpointResponses,
		}},
	})
	if len(store.writes) != 1 {
		t.Fatalf("admission fallback must be stored, writes=%d", len(store.writes))
	}
	var payload tracePayload
	if err := json.Unmarshal(store.writes[0].TracePayload, &payload); err != nil {
		t.Fatalf("decode trace payload: %v", err)
	}
	chain := payload.Attempts
	if len(chain) != 1 || chain[0].ChannelID != 8 || chain[0].UpstreamEndpoint != requestlog.UpstreamEndpointResponses {
		t.Fatalf("fallback chain must contain only the real transport attempt: %+v", chain)
	}
}

func TestRoutingTraceNormalUsesEmptyReasonsAndStructuredPayload(t *testing.T) {
	store := &fakeRoutingTraceStore{}
	recorder := NewRoutingTraceRecorder(store, zap.NewNop())
	recorder.Record(context.Background(), RoutingDecisionTraceInput{
		Request: requestlog.RequestRecord{
			ID: 2, RequestID: "req-normal", RequestedModelID: "openai/gpt",
			IngressProtocol: requestlog.ProtocolOpenAI, Endpoint: requestlog.EndpointResponses,
		},
		RouteID: 3, Mode: "balanced", PoolSize: 1,
		Plan: CandidatePlan{Candidates: []Candidate{{Route: candidateRoute(7, "openai")}}},
	})

	if len(store.writes) != 1 {
		t.Fatalf("expected normal decision to be stored, writes=%d", len(store.writes))
	}
	got := store.writes[0]
	if got.SchemaVersion != routingTraceSchemaVersion {
		t.Fatalf("unexpected normal trace schema=%d", got.SchemaVersion)
	}
	var payload tracePayload
	if err := json.Unmarshal(got.TracePayload, &payload); err != nil {
		t.Fatalf("decode structured trace payload: %v", err)
	}
	if payload.SchemaVersion != routingTraceSchemaVersion || payload.AlgorithmVersion != routingTraceAlgorithmVersion || len(payload.Candidates) != 1 {
		t.Fatalf("structured trace payload is incomplete: %+v", payload)
	}
	if payload.AbnormalReasons == nil || len(payload.AbnormalReasons) != 0 {
		t.Fatalf("normal reasons must be a non-nil empty array, got %#v", payload.AbnormalReasons)
	}
}

func TestRoutingTraceDistinguishesCooldownBypassFromInvalidSticky(t *testing.T) {
	tests := []struct {
		name       string
		exclusion  breakerstore.CandidateSnapshotStatus
		wantReason string
	}{
		{name: "cooldown is a temporary bypass", exclusion: breakerstore.CandidateSnapshotRateLimited, wantReason: "sticky_cooldown_bypass"},
		{name: "breaker open invalidates sticky", exclusion: breakerstore.CandidateSnapshotOpen, wantReason: "sticky_invalid"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeRoutingTraceStore{}
			recorder := NewRoutingTraceRecorder(store, zap.NewNop())
			recorder.Record(context.Background(), RoutingDecisionTraceInput{
				Request: requestlog.RequestRecord{
					ID: 5, RequestID: "req-sticky-exclusion", RequestedModelID: "openai/gpt",
					IngressProtocol: requestlog.ProtocolOpenAI, Endpoint: requestlog.EndpointResponses,
				},
				RouteID: 3, Mode: "balanced", StickyChannelID: 7,
				Plan: CandidatePlan{
					Candidates: []Candidate{{Route: candidateRoute(8, "openai")}},
					Excluded:   []CandidateExclusion{{ChannelID: 7, Reason: string(tc.exclusion)}},
				},
			})

			var payload tracePayload
			if err := json.Unmarshal(store.writes[0].TracePayload, &payload); err != nil {
				t.Fatalf("decode trace payload: %v", err)
			}
			if len(payload.AbnormalReasons) != 1 || payload.AbnormalReasons[0] != tc.wantReason {
				t.Fatalf("abnormal reasons = %#v, want %q", payload.AbnormalReasons, tc.wantReason)
			}
		})
	}
}

func TestRoutingTraceWriteFailureOnlyRecordsFailureMetric(t *testing.T) {
	store := &fakeRoutingTraceStore{err: errors.New("database unavailable")}
	recorder := NewRoutingTraceRecorder(store, zap.NewNop())
	traceMetrics := &fakeRoutingTraceMetrics{}
	recorder.SetMetrics(traceMetrics)

	recorder.Record(context.Background(), RoutingDecisionTraceInput{
		Request: requestlog.RequestRecord{
			ID: 4, RequestID: "req-trace-write-failure", RequestedModelID: "openai/gpt",
			IngressProtocol: requestlog.ProtocolOpenAI, Endpoint: requestlog.EndpointResponses,
		},
		RouteID: 3,
		Plan:    CandidatePlan{Candidates: []Candidate{{Route: candidateRoute(7, "openai")}}},
	})

	if len(store.writes) != 1 {
		t.Fatalf("expected one attempted write, got %d", len(store.writes))
	}
	if len(traceMetrics.results) != 1 || traceMetrics.results[0] != "failed" {
		t.Fatalf("trace persistence failure must be observable: %+v", traceMetrics.results)
	}
}

func TestRoutingTraceIncludesFullPoolExclusionReasons(t *testing.T) {
	runtimeConfigRevision := int64(7)
	store := &fakeDiagnosticRoutingTraceStore{pool: []sqlc.RouteRuntimePoolRow{
		{
			RouteID: 3, Mode: "balanced", RouteStatus: "enabled", ChannelID: 7,
			ChannelStatus: "enabled", ProviderStatus: "enabled", CredentialValid: true,
			HasCredential: true, HasOrigin: true, Protocol: "openai", ModelExists: true,
			ModelStatus: "enabled", BindingStatus: "enabled", HasModelPrice: true, HasChannelCost: true,
		},
		{
			RouteID: 3, Mode: "balanced", RouteStatus: "enabled", ChannelID: 8,
			ChannelStatus: "enabled", ProviderStatus: "enabled", CredentialValid: true,
			HasCredential: true, HasOrigin: true, Protocol: "openai", ModelExists: true,
			ModelStatus: "enabled", BindingStatus: "enabled", HasModelPrice: true, HasChannelCost: true,
		},
	}}
	recorder := NewRoutingTraceRecorder(store, zap.NewNop())
	request := requestlog.RequestRecord{
		ID: 11, RequestID: "req-full-pool", RequestedModelID: "openai/gpt",
		IngressProtocol: requestlog.ProtocolOpenAI, Endpoint: requestlog.EndpointChatCompletions,
	}
	plan := CandidatePlan{
		Candidates: []Candidate{{Route: candidateRoute(7, "openai"), Balance: BalanceScore{
			ProviderID: 21, CandidateOriginRevision: 3, RuntimeOriginRevision: 3,
			OriginRevisionCurrent: true, CandidateProviderStatusRevision: 4,
			RuntimeProviderStatusRevision: 4, ProviderStatusRevisionCurrent: true,
			CandidateChannelConfigRevision: 7, RuntimeChannelConfigRevision: &runtimeConfigRevision,
			ChannelConfigRevisionCurrent: true, CandidateChannelCapacityRevision: 5,
			RuntimeChannelCapacityRevision: 5, ChannelCapacityRevisionCurrent: true,
			RouteRateLimitsRevision:   3,
			GlobalConcurrencyRevision: 2, CircuitBreakerRevision: 6,
			RoutingBalanceRevision: 4, RuntimeControlState: "active", RuntimeRevisionCurrent: true,
			ProviderBreakerState: "closed", ChannelBreakerState: "closed", BreakerStoreAdmission: "normal",
			CostScore: 60, ConcurrencyScore: 50, TTFTScore: 97.95, ErrorScore: 75, PriorityScore: 90,
			CostWeightPct: 25, ConcurrencyWeightPct: 20, TTFTWeightPct: 25,
			ErrorRateWeightPct: 20, PriorityWeightPct: 10,
			CostRatio: 0.4, Priority: 10,
			AvgTTFTMs: 820, TTFTSampleCount: 18, ErrorRatePct: 10, ErrorSampleCount: 20,
			FinalScore: 74.4875,
		}}},
		Excluded: []CandidateExclusion{{ChannelID: 8, RouteIndex: 1, Reason: "capability_unsupported"}},
	}
	recorder.Record(context.Background(), RoutingDecisionTraceInput{
		Request: request, RouteID: 3, Mode: "balanced", Plan: plan, ForceReasons: []string{"test_abnormal"},
	})
	if len(store.writes) != 1 || store.writes[0].PoolSize != 2 {
		t.Fatalf("expected one full-pool trace: %+v", store.writes)
	}
	var payload tracePayload
	if err := json.Unmarshal(store.writes[0].TracePayload, &payload); err != nil {
		t.Fatalf("decode trace payload: %v", err)
	}
	scores := payload.Candidates
	if len(scores) != 2 || !scores[0].Eligible || scores[1].Eligible || scores[1].ExcludedReason != "capability_unsupported" {
		t.Fatalf("unexpected full-pool diagnostics: %+v", scores)
	}
	if scores[0].ProviderID != 21 || !scores[0].ProviderStatusRevisionCurrent ||
		scores[0].RuntimeChannelConfigRevision == nil || *scores[0].RuntimeChannelConfigRevision != 7 ||
		scores[0].RouteRateLimitsRevision != 3 ||
		scores[0].CircuitBreakerRevision != 6 ||
		scores[0].ErrorSampleCount != 20 || scores[0].TTFTSampleCount != 18 ||
		scores[0].CostRatio != 0.4 || scores[0].BreakerStoreAdmission != "normal" {
		t.Fatalf("P4 runtime facts missing from trace: %+v", scores[0])
	}
	// 五项评分与权重必须完整进入 trace（§7.8 展示要求）。
	if scores[0].CostScore != 60 || scores[0].ConcurrencyScore != 50 || scores[0].ErrorScore != 75 ||
		scores[0].PriorityScore != 90 || scores[0].AvgTTFTMs != 820 || scores[0].ErrorRatePct != 10 ||
		scores[0].CostWeightPct != 25 || scores[0].ConcurrencyWeightPct != 20 ||
		scores[0].TTFTWeightPct != 25 || scores[0].ErrorRateWeightPct != 20 || scores[0].PriorityWeightPct != 10 {
		t.Fatalf("five-metric scoring facts missing from trace: %+v", scores[0])
	}
}

// TestRoutingTraceWritesStickyAudit 冻结 §10.12：sticky 审计字段必须原样进入 trace，
// 且 Unbound 写成 SQL NULL 而不是 0（0 会被误读成「绑定到 channel 0」）。
func TestRoutingTraceWritesStickyAudit(t *testing.T) {
	request := requestlog.RequestRecord{
		ID: 21, RequestID: "req-sticky-audit", RequestedModelID: "openai/gpt",
		IngressProtocol: requestlog.ProtocolOpenAI, Endpoint: requestlog.EndpointChatCompletions,
	}

	t.Run("clear then bind records both endpoints", func(t *testing.T) {
		store := &fakeRoutingTraceStore{}
		recorder := NewRoutingTraceRecorder(store, zap.NewNop())
		recorder.Record(context.Background(), RoutingDecisionTraceInput{
			Request: request, RouteID: 3, Mode: "balanced",
			Plan:            CandidatePlan{Candidates: []Candidate{{Route: candidateRoute(9, "openai")}}},
			StickyChannelID: 7,
			Sticky: StickyAudit{
				KeyPresent: true, BeforeChannelID: 7, BeforeVersion: 111,
				Action: StickyActionBindIfAbsent, Reason: "complete_success",
				AfterChannelID: 9, AfterVersion: 222,
			},
		})
		if len(store.writes) != 1 {
			t.Fatalf("expected one trace write, got %d", len(store.writes))
		}
		got := store.writes[0]
		if !got.StickyKeyPresent ||
			!got.StickyBeforeChannelID.Valid || got.StickyBeforeChannelID.Int64 != 7 ||
			!got.StickyBeforeVersion.Valid || got.StickyBeforeVersion.Int64 != 111 ||
			!got.StickyAction.Valid || got.StickyAction.String != string(StickyActionBindIfAbsent) ||
			!got.StickyReason.Valid || got.StickyReason.String != "complete_success" ||
			!got.StickyAfterChannelID.Valid || got.StickyAfterChannelID.Int64 != 9 ||
			!got.StickyAfterVersion.Valid || got.StickyAfterVersion.Int64 != 222 {
			t.Fatalf("sticky audit did not reach the trace: %+v", got)
		}
	})

	t.Run("unbound endpoints stay NULL", func(t *testing.T) {
		store := &fakeRoutingTraceStore{}
		recorder := NewRoutingTraceRecorder(store, zap.NewNop())
		recorder.Record(context.Background(), RoutingDecisionTraceInput{
			Request: request, RouteID: 3, Mode: "balanced",
			Plan: CandidatePlan{Candidates: []Candidate{{Route: candidateRoute(9, "openai")}}},
			Sticky: StickyAudit{
				KeyPresent: true, Action: StickyActionMiss,
			},
		})
		got := store.writes[0]
		if got.StickyBeforeChannelID.Valid || got.StickyBeforeVersion.Valid ||
			got.StickyAfterChannelID.Valid || got.StickyAfterVersion.Valid {
			t.Fatalf("unbound endpoints must be NULL, not 0: %+v", got)
		}
		if got.StickyReason.Valid {
			t.Fatalf("empty reason must be NULL: %+v", got.StickyReason)
		}
		if !got.StickyAction.Valid || got.StickyAction.String != string(StickyActionMiss) {
			t.Fatalf("miss action must be recorded: %+v", got.StickyAction)
		}
	})

	t.Run("sticky disabled request", func(t *testing.T) {
		store := &fakeRoutingTraceStore{}
		recorder := NewRoutingTraceRecorder(store, zap.NewNop())
		recorder.Record(context.Background(), RoutingDecisionTraceInput{
			Request: request, RouteID: 3, Mode: "fixed",
			Plan:   CandidatePlan{Candidates: []Candidate{{Route: candidateRoute(9, "openai")}}},
			Sticky: StickyAudit{Action: StickyActionDisabled},
		})
		got := store.writes[0]
		if got.StickyKeyPresent {
			t.Fatalf("disabled sticky must not claim a key: %+v", got)
		}
		if !got.StickyAction.Valid || got.StickyAction.String != string(StickyActionDisabled) {
			t.Fatalf("disabled action must be recorded: %+v", got.StickyAction)
		}
	})
}
