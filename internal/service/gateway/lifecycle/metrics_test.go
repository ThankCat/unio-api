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

type p4RoutingMetricsSpy struct {
	weights                 map[string]float64
	breakerStates           map[string]string
	skips                   []string
	channelRevisionMismatch int
	statusRevisionMismatch  int
	timings                 []p4TimingObservation
	endpointFailures        []string
	channelFailures         []string
}

type p4TimingObservation struct {
	providerID string
	endpointID string
	channelID  string
	protocol   string
	operation  string
	mode       string
	total      time.Duration
	ttft       *time.Duration
}

func (s *p4RoutingMetricsSpy) IncChatRequest(bool, observabilitymetrics.ChatOutcome) {}
func (s *p4RoutingMetricsSpy) IncRoutingSelected(string, string, string)             {}
func (s *p4RoutingMetricsSpy) ObserveUpstream(string, string, bool, string, time.Duration) {
}
func (s *p4RoutingMetricsSpy) IncSettlement(observabilitymetrics.SettlementOutcome) {}
func (s *p4RoutingMetricsSpy) IncStreamEvent(observabilitymetrics.StreamEvent)      {}
func (s *p4RoutingMetricsSpy) IncPartialSettlement(string)                          {}
func (s *p4RoutingMetricsSpy) IncRetryableFallback(string)                          {}
func (s *p4RoutingMetricsSpy) IncZeroPriceServed(string, string, string)            {}
func (s *p4RoutingMetricsSpy) IncRoutingSkip(string)                                {}
func (s *p4RoutingMetricsSpy) ObserveRoutingHeadWait(time.Duration)                 {}

func (s *p4RoutingMetricsSpy) ObserveRoutingBalance(string, string, int, int, float64) {}
func (s *p4RoutingMetricsSpy) IncRoutingBalanceSelected(string, string)                {}
func (s *p4RoutingMetricsSpy) IncRoutingBalanceFallback(string, string)                {}
func (s *p4RoutingMetricsSpy) IncRoutingCapacityRead(string)                           {}
func (s *p4RoutingMetricsSpy) IncRoutingMarginGuard(string)                            {}
func (s *p4RoutingMetricsSpy) SetBalancedFinalWeight(route, channel string, weight float64) {
	if s.weights == nil {
		s.weights = map[string]float64{}
	}
	s.weights[route+"/"+channel] = weight
}
func (s *p4RoutingMetricsSpy) SetBreakerState(scope, id, state string) {
	if s.breakerStates == nil {
		s.breakerStates = map[string]string{}
	}
	s.breakerStates[scope+"/"+id] = state
}
func (s *p4RoutingMetricsSpy) IncBreakerSkip(scope, reason string) {
	s.skips = append(s.skips, scope+"/"+reason)
}
func (s *p4RoutingMetricsSpy) IncChannelConfigRevisionMismatch(string) {
	s.channelRevisionMismatch++
}
func (s *p4RoutingMetricsSpy) IncEndpointStatusRevisionMismatch(string) {
	s.statusRevisionMismatch++
}
func (s *p4RoutingMetricsSpy) ObserveUpstreamTiming(providerID, endpointID, channelID, protocol, operation, mode string, total time.Duration, ttft *time.Duration) {
	s.timings = append(s.timings, p4TimingObservation{
		providerID: providerID, endpointID: endpointID, channelID: channelID,
		protocol: protocol, operation: operation, mode: mode, total: total, ttft: ttft,
	})
}
func (s *p4RoutingMetricsSpy) IncEndpointFailure(endpointID, category string) {
	s.endpointFailures = append(s.endpointFailures, endpointID+"/"+category)
}
func (s *p4RoutingMetricsSpy) IncChannelFailure(channelID, category string) {
	s.channelFailures = append(s.channelFailures, channelID+"/"+category)
}

func TestRecordRoutingPlanPublishesP4WeightsAndBreakerFacts(t *testing.T) {
	metrics := &p4RoutingMetricsSpy{}
	lifecycle := &RequestLifecycle{metrics: metrics}
	lifecycle.recordRoutingPlan(RoutingDecisionTraceInput{
		RouteID:  31,
		Mode:     "balanced",
		PoolSize: 2,
		Plan: CandidatePlan{
			Candidates: []Candidate{{
				Route: routing.ChatRouteCandidate{
					ProviderEndpointID: 23,
					Channel:            routingChannel(17),
				},
				Balance: BalanceScore{Weight: 0.75, EndpointBreakerState: "closed", ChannelBreakerState: "closed"},
			}},
			Excluded: []CandidateExclusion{{
				ChannelID: 19,
				Reason:    "stale_config_revision",
				Route: routing.ChatRouteCandidate{
					ProviderEndpointID: 29,
					Channel:            routingChannel(19),
				},
				Balance: BalanceScore{EndpointBreakerState: "open", ChannelBreakerState: "closed"},
			}},
		},
	})

	if metrics.weights["31/17"] != 0.75 || metrics.weights["31/19"] != 0 {
		t.Fatalf("weights = %#v", metrics.weights)
	}
	if metrics.breakerStates["endpoint/23"] != "closed" ||
		metrics.breakerStates["channel/17"] != "closed" ||
		metrics.breakerStates["endpoint/29"] != "open" {
		t.Fatalf("breaker states = %#v", metrics.breakerStates)
	}
	if len(metrics.skips) != 1 || metrics.skips[0] != "endpoint/stale_config_revision" {
		t.Fatalf("skips = %#v", metrics.skips)
	}
	if metrics.channelRevisionMismatch != 1 || metrics.statusRevisionMismatch != 0 {
		t.Fatalf("revision mismatches = channel:%d status:%d", metrics.channelRevisionMismatch, metrics.statusRevisionMismatch)
	}
}

func TestRecordAttemptRuntimeMetricsSeparatesStreamTTFTAndTotalDuration(t *testing.T) {
	metrics := &p4RoutingMetricsSpy{}
	lifecycle := &RequestLifecycle{metrics: metrics}
	started := time.Unix(100, 0)
	firstToken := started.Add(250 * time.Millisecond)
	completed := started.Add(2 * time.Second)
	candidate := routing.ChatRouteCandidate{
		ProviderID:         11,
		ProviderEndpointID: 23,
		Protocol:           "openai",
		Channel:            routingChannel(17),
	}
	lifecycle.RecordAttemptRuntimeMetrics(
		candidate,
		requestlog.UpstreamOperationResponses,
		true,
		AttemptTimingFacts{UpstreamStartedAt: &started, UpstreamFirstTokenAt: &firstToken, UpstreamCompletedAt: &completed},
		breakerstore.FinishOutcome{
			EndpointEvidence: breakerstore.EndpointEvidenceHTTP500,
			ChannelOutcome:   breakerstore.OutcomeEligibleFailure,
		},
		adapter.NewUpstreamError(adapter.UpstreamErrorServer, adapter.UpstreamMetadata{}, nil),
	)
	if len(metrics.timings) != 1 {
		t.Fatalf("timings=%v", metrics.timings)
	}
	got := metrics.timings[0]
	if got.providerID != "11" || got.endpointID != "23" || got.channelID != "17" ||
		got.protocol != "openai" || got.operation != "responses" || got.mode != "stream" ||
		got.total != 2*time.Second || got.ttft == nil || *got.ttft != 250*time.Millisecond {
		t.Fatalf("timing=%+v", got)
	}
	if len(metrics.endpointFailures) != 1 || metrics.endpointFailures[0] != "23/http_500" ||
		len(metrics.channelFailures) != 1 || metrics.channelFailures[0] != "17/server_error" {
		t.Fatalf("endpoint=%v channel=%v", metrics.endpointFailures, metrics.channelFailures)
	}

	metrics.timings = nil
	lifecycle.RecordAttemptRuntimeMetrics(
		candidate,
		requestlog.UpstreamOperationChatCompletions,
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
