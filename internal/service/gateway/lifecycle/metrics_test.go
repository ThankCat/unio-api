package lifecycle

import (
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/core/channel"
	"github.com/ThankCat/unio-gateway/internal/core/requestlog"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	observabilitymetrics "github.com/ThankCat/unio-gateway/internal/platform/observability/metrics"
)

type routingMetricsSpy struct {
	weights                 map[string]float64
	breakerStates           map[string]string
	skips                   []string
	channelRevisionMismatch int
	statusRevisionMismatch  int
	timings                 []timingObservation
	providerFailures        []string
	channelFailures         []string
}

type timingObservation struct {
	providerID string
	channelID  string
	protocol   string
	endpoint   string
	mode       string
	total      time.Duration
	ttft       *time.Duration
}

func (s *routingMetricsSpy) IncChatRequest(bool, observabilitymetrics.ChatOutcome) {}
func (s *routingMetricsSpy) IncRoutingSelected(string, string, string)             {}
func (s *routingMetricsSpy) ObserveUpstream(string, string, bool, string, time.Duration) {
}
func (s *routingMetricsSpy) IncSettlement(observabilitymetrics.SettlementOutcome) {}
func (s *routingMetricsSpy) IncStreamEvent(observabilitymetrics.StreamEvent)      {}
func (s *routingMetricsSpy) IncPartialSettlement(string)                          {}
func (s *routingMetricsSpy) IncRetryableFallback(string)                          {}
func (s *routingMetricsSpy) IncZeroPriceServed(string, string, string)            {}
func (s *routingMetricsSpy) IncRoutingSkip(string)                                {}
func (s *routingMetricsSpy) ObserveRoutingCapacityWait(time.Duration)             {}

func (s *routingMetricsSpy) ObserveRoutingBalance(string, string, int, int, float64) {}
func (s *routingMetricsSpy) IncRoutingBalanceSelected(string, string)                {}
func (s *routingMetricsSpy) IncRoutingBalanceFallback(string, string)                {}
func (s *routingMetricsSpy) IncRoutingCapacityRead(string)                           {}
func (s *routingMetricsSpy) IncRoutingMarginGuard(string)                            {}
func (s *routingMetricsSpy) SetBalancedFinalWeight(route, channel string, weight float64) {
	if s.weights == nil {
		s.weights = map[string]float64{}
	}
	s.weights[route+"/"+channel] = weight
}
func (s *routingMetricsSpy) SetBreakerState(scope, id, state string) {
	if s.breakerStates == nil {
		s.breakerStates = map[string]string{}
	}
	s.breakerStates[scope+"/"+id] = state
}
func (s *routingMetricsSpy) IncBreakerSkip(scope, reason string) {
	s.skips = append(s.skips, scope+"/"+reason)
}
func (s *routingMetricsSpy) IncChannelConfigRevisionMismatch(string) {
	s.channelRevisionMismatch++
}
func (s *routingMetricsSpy) IncProviderStatusRevisionMismatch(string) {
	s.statusRevisionMismatch++
}
func (s *routingMetricsSpy) ObserveUpstreamTiming(providerID, channelID, protocol, endpoint, mode string, total time.Duration, ttft *time.Duration) {
	s.timings = append(s.timings, timingObservation{
		providerID: providerID, channelID: channelID,
		protocol: protocol, endpoint: endpoint, mode: mode, total: total, ttft: ttft,
	})
}
func (s *routingMetricsSpy) IncProviderFailure(originID, category string) {
	s.providerFailures = append(s.providerFailures, originID+"/"+category)
}
func (s *routingMetricsSpy) IncChannelFailure(channelID, category string) {
	s.channelFailures = append(s.channelFailures, channelID+"/"+category)
}

func TestRecordRoutingPlanPublishesWeightsAndBreakerFacts(t *testing.T) {
	metrics := &routingMetricsSpy{}
	lifecycle := &RequestLifecycle{metrics: metrics}
	lifecycle.recordRoutingPlan(RoutingDecisionTraceInput{
		RouteID:  31,
		Mode:     "balanced",
		PoolSize: 2,
		Plan: CandidatePlan{
			Candidates: []Candidate{{
				Route: routing.ChatRouteCandidate{
					ProviderID: 23,
					Channel:    routingChannel(17),
				},
				Balance: BalanceScore{FinalScore: 0.75, ProviderBreakerState: "closed", ChannelBreakerState: "closed"},
			}},
			Excluded: []CandidateExclusion{{
				ChannelID: 19,
				Reason:    "stale_config_revision",
				Route: routing.ChatRouteCandidate{
					ProviderID: 29,
					Channel:    routingChannel(19),
				},
				Balance: BalanceScore{ProviderBreakerState: "open", ChannelBreakerState: "closed"},
			}},
		},
	})

	if metrics.weights["31/17"] != 0.75 || metrics.weights["31/19"] != 0 {
		t.Fatalf("weights = %#v", metrics.weights)
	}
	if metrics.breakerStates["provider/23"] != "closed" ||
		metrics.breakerStates["channel/17"] != "closed" ||
		metrics.breakerStates["provider/29"] != "open" {
		t.Fatalf("breaker states = %#v", metrics.breakerStates)
	}
	if len(metrics.skips) != 1 || metrics.skips[0] != "provider/stale_config_revision" {
		t.Fatalf("skips = %#v", metrics.skips)
	}
	if metrics.channelRevisionMismatch != 1 || metrics.statusRevisionMismatch != 0 {
		t.Fatalf("revision mismatches = channel:%d status:%d", metrics.channelRevisionMismatch, metrics.statusRevisionMismatch)
	}
}

func TestRecordAttemptRuntimeMetricsSeparatesStreamTTFTAndTotalDuration(t *testing.T) {
	metrics := &routingMetricsSpy{}
	lifecycle := &RequestLifecycle{metrics: metrics}
	started := time.Unix(100, 0)
	firstToken := started.Add(250 * time.Millisecond)
	completed := started.Add(2 * time.Second)
	candidate := routing.ChatRouteCandidate{
		ProviderID: 23,
		Protocol:   "openai",
		Channel:    routingChannel(17),
	}
	lifecycle.RecordAttemptRuntimeMetrics(
		candidate,
		requestlog.UpstreamEndpointResponses,
		true,
		AttemptTimingFacts{UpstreamStartedAt: &started, UpstreamFirstTokenAt: &firstToken, UpstreamCompletedAt: &completed},
		breakerstore.FinishOutcome{
			ProviderEvidence: breakerstore.ProviderEvidenceHTTP500,
			ChannelOutcome:   breakerstore.OutcomeEligibleFailure,
		},
		adapter.NewUpstreamError(adapter.UpstreamErrorServer, adapter.UpstreamMetadata{}, nil),
	)
	if len(metrics.timings) != 1 {
		t.Fatalf("timings=%v", metrics.timings)
	}
	got := metrics.timings[0]
	if got.providerID != "23" || got.channelID != "17" ||
		got.protocol != "openai" || got.endpoint != "responses" || got.mode != "stream" ||
		got.total != 2*time.Second || got.ttft == nil || *got.ttft != 250*time.Millisecond {
		t.Fatalf("timing=%+v", got)
	}
	if len(metrics.providerFailures) != 1 || metrics.providerFailures[0] != "23/http_500" ||
		len(metrics.channelFailures) != 1 || metrics.channelFailures[0] != "17/server_error" {
		t.Fatalf("origin=%v channel=%v", metrics.providerFailures, metrics.channelFailures)
	}

	metrics.timings = nil
	lifecycle.RecordAttemptRuntimeMetrics(
		candidate,
		requestlog.UpstreamEndpointChatCompletions,
		false,
		AttemptTimingFacts{UpstreamStartedAt: &started, UpstreamCompletedAt: &completed},
		breakerstore.FinishOutcome{},
		nil,
	)
	if len(metrics.timings) != 1 || metrics.timings[0].ttft != nil || metrics.timings[0].mode != "non_stream" {
		t.Fatalf("non-stream timings=%+v", metrics.timings)
	}
}

func routingChannel(id int64) channel.Runtime {
	return channel.Runtime{ID: id}
}
