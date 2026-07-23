package lifecycle

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/ThankCat/unio-gateway/internal/core/requestlog"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"go.uber.org/zap"
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
}

type fakeRoutingTraceMetrics struct {
	results []string
}

func (m *fakeRoutingTraceMetrics) IncRoutingTraceWrite(result string) {
	m.results = append(m.results, result)
}

func (s *fakeRoutingTraceStore) UpsertRoutingDecisionTrace(_ context.Context, in sqlc.UpsertRoutingDecisionTraceParams) error {
	s.writes = append(s.writes, in)
	return nil
}

func TestRoutingTraceRecorderSamplesNormalAndAlwaysWritesFallback(t *testing.T) {
	store := &fakeRoutingTraceStore{}
	recorder := NewRoutingTraceRecorder(store, zap.NewNop())
	traceMetrics := &fakeRoutingTraceMetrics{}
	recorder.SetMetrics(traceMetrics)
	recorder.SetSampleRate(0)
	request := requestlog.RequestRecord{
		ID: 1, RequestID: "req-unsampled", RequestedModelID: "openai/gpt",
		IngressProtocol: requestlog.ProtocolOpenAI, Operation: requestlog.OperationChatCompletions,
	}
	plan := CandidatePlan{Candidates: []Candidate{{Route: candidateRoute(7, "openai"), Balance: BalanceScore{CapacityScore: 0.5, RoutingFactor: 0.8, Weight: 0.4}}}}

	recorder.Record(context.Background(), RoutingDecisionTraceInput{Request: request, RouteID: 3, Mode: "balanced", PoolSize: 1, Plan: plan})
	if len(store.writes) != 0 {
		t.Fatal("unsampled normal decision must not be stored")
	}
	if len(traceMetrics.results) != 1 || traceMetrics.results[0] != "sampled_out" {
		t.Fatalf("unexpected sampled-out metrics: %+v", traceMetrics.results)
	}

	plan.Candidates = append(plan.Candidates, Candidate{Route: candidateRoute(8, "openai")})
	recorder.Record(context.Background(), RoutingDecisionTraceInput{
		Request: request, RouteID: 3, Mode: "balanced", PoolSize: 2, Plan: plan,
		FallbackOccurred: true,
		FallbackChain: []TransportAttempt{
			{ChannelID: 7, UpstreamOperation: requestlog.UpstreamOperationResponsesCompact},
			{ChannelID: 7, UpstreamOperation: requestlog.UpstreamOperationChatCompletions},
		},
	})
	if len(store.writes) != 1 {
		t.Fatalf("fallback must be stored regardless of sample rate, writes=%d", len(store.writes))
	}
	if len(traceMetrics.results) != 2 || traceMetrics.results[1] != "success" {
		t.Fatalf("unexpected trace write metrics: %+v", traceMetrics.results)
	}
	got := store.writes[0]
	if !got.Abnormal || len(got.AbnormalReasons) != 1 || got.AbnormalReasons[0] != "fallback" {
		t.Fatalf("unexpected fallback trace: %+v", got)
	}
	if got.AlgorithmVersion != "balanced_v3_cost" {
		t.Fatalf("algorithm version = %q, want balanced_v3_cost", got.AlgorithmVersion)
	}
	var chain []TransportAttempt
	if err := json.Unmarshal(got.FallbackChain, &chain); err != nil {
		t.Fatalf("decode fallback chain: %v", err)
	}
	if len(chain) != 2 || chain[0].ChannelID != 7 || chain[1].ChannelID != 7 ||
		chain[0].UpstreamOperation != requestlog.UpstreamOperationResponsesCompact ||
		chain[1].UpstreamOperation != requestlog.UpstreamOperationChatCompletions {
		t.Fatalf("same-channel transport attempts lost from trace: %+v", chain)
	}
}

func TestRoutingTraceFallbackChainUsesActualTransportAfterAdmissionSkip(t *testing.T) {
	store := &fakeRoutingTraceStore{}
	recorder := NewRoutingTraceRecorder(store, zap.NewNop())
	recorder.SetSampleRate(0)
	plan := CandidatePlan{Candidates: []Candidate{
		{Route: candidateRoute(7, "openai")},
		{Route: candidateRoute(8, "openai")},
	}}
	recorder.Record(context.Background(), RoutingDecisionTraceInput{
		Request: requestlog.RequestRecord{
			ID: 3, RequestID: "req-admission-skip", RequestedModelID: "openai/gpt",
			IngressProtocol: requestlog.ProtocolOpenAI, Operation: requestlog.OperationResponses,
		},
		RouteID: 3, Mode: "balanced", PoolSize: 2, Plan: plan,
		FallbackOccurred: true,
		FallbackChain: []TransportAttempt{{
			ChannelID: 8, UpstreamOperation: requestlog.UpstreamOperationResponses,
		}},
	})
	if len(store.writes) != 1 {
		t.Fatalf("admission fallback must be stored, writes=%d", len(store.writes))
	}
	var chain []TransportAttempt
	if err := json.Unmarshal(store.writes[0].FallbackChain, &chain); err != nil {
		t.Fatalf("decode fallback chain: %v", err)
	}
	if len(chain) != 1 || chain[0].ChannelID != 8 || chain[0].UpstreamOperation != requestlog.UpstreamOperationResponses {
		t.Fatalf("fallback chain must contain only the real transport attempt: %+v", chain)
	}
}

func TestRoutingTraceStableSampling(t *testing.T) {
	var sampledID, skippedID string
	for i := 0; i < 10000 && (sampledID == "" || skippedID == ""); i++ {
		id := fmt.Sprintf("req-%d", i)
		if routingTraceSampled(id, 500) {
			sampledID = id
		} else {
			skippedID = id
		}
	}
	if sampledID == "" || skippedID == "" {
		t.Fatal("expected both sampled and skipped stable hash buckets")
	}
	if !routingTraceSampled(sampledID, 500) || routingTraceSampled(skippedID, 500) {
		t.Fatal("sampling decision must be stable for the same request id")
	}
}

func TestRoutingTraceSampledNormalUsesEmptyReasons(t *testing.T) {
	store := &fakeRoutingTraceStore{}
	recorder := NewRoutingTraceRecorder(store, zap.NewNop())
	recorder.SetSampleRate(1)
	recorder.Record(context.Background(), RoutingDecisionTraceInput{
		Request: requestlog.RequestRecord{
			ID: 2, RequestID: "req-sampled-normal", RequestedModelID: "openai/gpt",
			IngressProtocol: requestlog.ProtocolOpenAI, Operation: requestlog.OperationResponses,
		},
		RouteID: 3, Mode: "balanced", PoolSize: 1,
		Plan: CandidatePlan{Candidates: []Candidate{{Route: candidateRoute(7, "openai")}}},
	})

	if len(store.writes) != 1 {
		t.Fatalf("expected sampled normal decision to be stored, writes=%d", len(store.writes))
	}
	got := store.writes[0]
	if got.Abnormal || !got.Sampled {
		t.Fatalf("unexpected sampled normal flags: abnormal=%v sampled=%v", got.Abnormal, got.Sampled)
	}
	if got.AbnormalReasons == nil || len(got.AbnormalReasons) != 0 {
		t.Fatalf("normal reasons must be a non-nil empty array, got %#v", got.AbnormalReasons)
	}
}

func TestRoutingTraceIncludesFullPoolExclusionReasons(t *testing.T) {
	runtimeConfigRevision := int64(7)
	store := &fakeDiagnosticRoutingTraceStore{pool: []sqlc.RouteRuntimePoolRow{
		{
			RouteID: 3, Mode: "balanced", RouteStatus: "enabled", ChannelID: 7,
			ChannelStatus: "enabled", ProviderStatus: "enabled", CredentialValid: true,
			HasCredential: true, HasBaseUrl: true, Protocol: "openai", ModelExists: true,
			ModelStatus: "enabled", BindingStatus: "enabled", HasModelPrice: true, HasChannelCost: true,
		},
		{
			RouteID: 3, Mode: "balanced", RouteStatus: "enabled", ChannelID: 8,
			ChannelStatus: "enabled", ProviderStatus: "enabled", CredentialValid: true,
			HasCredential: true, HasBaseUrl: true, Protocol: "openai", ModelExists: true,
			ModelStatus: "enabled", BindingStatus: "enabled", HasModelPrice: true, HasChannelCost: true,
		},
	}}
	recorder := NewRoutingTraceRecorder(store, zap.NewNop())
	request := requestlog.RequestRecord{
		ID: 11, RequestID: "req-full-pool", RequestedModelID: "openai/gpt",
		IngressProtocol: requestlog.ProtocolOpenAI, Operation: requestlog.OperationChatCompletions,
	}
	plan := CandidatePlan{
		Candidates: []Candidate{{Route: candidateRoute(7, "openai"), Balance: BalanceScore{
			EndpointID: 21, CandidateEndpointBaseURLRevision: 3, RuntimeEndpointBaseURLRevision: 3,
			EndpointBaseURLRevisionCurrent: true, CandidateEndpointStatusRevision: 4,
			RuntimeEndpointStatusRevision: 4, EndpointStatusRevisionCurrent: true,
			CandidateChannelConfigRevision: 7, RuntimeChannelConfigRevision: &runtimeConfigRevision,
			ChannelConfigRevisionCurrent: true, CandidateChannelAdmissionLimitsRevision: 5,
			RuntimeChannelAdmissionLimitsRevision: 5, ChannelAdmissionLimitsRevisionCurrent: true,
			RouteRateLimitsRevision: 3, ChannelRateLimitsRevision: 7,
			GlobalConcurrencyRevision: 2, CircuitBreakerRevision: 6,
			RoutingBalanceRevision: 4, RuntimeControlState: "active", RuntimeRevisionCurrent: true,
			EndpointBreakerState: "closed", ChannelBreakerState: "closed", BreakerStoreAdmission: "normal",
			CapacityScore: 0.5, ErrorRate: 0.1, ErrorSamples: 20, TTFTEWMAMs: 820,
			TTFTSamples: 18, TTFTSampleSource: "stream_only", RoutingFactor: 0.8,
			CostRatio: 0.4, CostWeight: 0.5, CostFactor: 0.8, Weight: 0.32,
		}}},
		Excluded: []CandidateExclusion{{ChannelID: 8, RouteIndex: 1, Reason: "capability_unsupported"}},
	}
	recorder.Record(context.Background(), RoutingDecisionTraceInput{
		Request: request, RouteID: 3, Mode: "balanced", Plan: plan, ForceReasons: []string{"test_abnormal"},
	})
	if len(store.writes) != 1 || store.writes[0].PoolSize != 2 {
		t.Fatalf("expected one full-pool trace: %+v", store.writes)
	}
	var scores []traceCandidateScore
	if err := json.Unmarshal(store.writes[0].CandidateScores, &scores); err != nil {
		t.Fatalf("decode candidate scores: %v", err)
	}
	if len(scores) != 2 || !scores[0].Eligible || scores[1].Eligible || scores[1].ExcludedReason != "capability_unsupported" {
		t.Fatalf("unexpected full-pool diagnostics: %+v", scores)
	}
	if scores[0].EndpointID != 21 || !scores[0].EndpointStatusRevisionCurrent ||
		scores[0].RuntimeChannelConfigRevision == nil || *scores[0].RuntimeChannelConfigRevision != 7 ||
		scores[0].RouteRateLimitsRevision != 3 || scores[0].ChannelRateLimitsRevision != 7 ||
		scores[0].CircuitBreakerRevision != 6 ||
		scores[0].ErrorSamples != 20 || scores[0].TTFTSamples != 18 ||
		scores[0].CostRatio != 0.4 || scores[0].CostWeight != 0.5 || scores[0].CostFactor != 0.8 ||
		scores[0].BreakerStoreAdmission != "normal" {
		t.Fatalf("P4 runtime facts missing from trace: %+v", scores[0])
	}
}
