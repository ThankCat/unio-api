package gateway

import (
	"testing"
	"time"

	"github.com/ThankCat/unio-api/internal/app/gatewayapi"
	"github.com/ThankCat/unio-api/internal/core/adapter"
	"github.com/ThankCat/unio-api/internal/platform/observability/metrics"
)

// fakeMetricsRecorder 捕获 gateway 上报的业务指标调用，供传播测试断言。
type fakeMetricsRecorder struct {
	chatRequests []chatRequestMetric
	routing      []routingMetric
	upstream     []upstreamMetric
	settlements  []metrics.SettlementOutcome
	streamEvents []metrics.StreamEvent
}

type chatRequestMetric struct {
	stream  bool
	outcome metrics.ChatOutcome
}

type routingMetric struct {
	provider string
	channel  string
	model    string
}

type upstreamMetric struct {
	provider      string
	channel       string
	success       bool
	errorCategory string
}

func (r *fakeMetricsRecorder) IncChatRequest(stream bool, outcome metrics.ChatOutcome) {
	r.chatRequests = append(r.chatRequests, chatRequestMetric{stream: stream, outcome: outcome})
}

func (r *fakeMetricsRecorder) IncRoutingSelected(provider string, channel string, model string) {
	r.routing = append(r.routing, routingMetric{provider: provider, channel: channel, model: model})
}

func (r *fakeMetricsRecorder) ObserveUpstream(provider string, channel string, success bool, errorCategory string, _ time.Duration) {
	r.upstream = append(r.upstream, upstreamMetric{provider: provider, channel: channel, success: success, errorCategory: errorCategory})
}

func (r *fakeMetricsRecorder) IncSettlement(outcome metrics.SettlementOutcome) {
	r.settlements = append(r.settlements, outcome)
}

func (r *fakeMetricsRecorder) IncStreamEvent(event metrics.StreamEvent) {
	r.streamEvents = append(r.streamEvents, event)
}

func TestChatCompletionServiceRecordsSuccessMetrics(t *testing.T) {
	recorder := &fakeMetricsRecorder{}
	fakeAdapter := &fakeChatAdapter{chatResp: chatResponse("ok")}
	router := &fakeChatRouter{plan: routePlan(routeCandidate("openai", 123, "gpt-4.1"))}
	registry := &fakeAdapterRegistry{
		chatAdapters: map[string]adapter.ChatAdapter{"openai": fakeAdapter},
	}
	service := NewChatCompletionService(
		router,
		registry,
		ProviderErrorClassifier{},
		newFakeRequestLogService(),
		newChatCompletionSettlementForTest(),
		&fakeChatAuthorizer{authorization: ChatAuthorization{ReservationID: 1}},
		recorder,
		nil,
	)

	if _, err := service.CreateChatCompletion(contextWithPrincipal(42), chatRequest()); err != nil {
		t.Fatalf("CreateChatCompletion returned err: %v", err)
	}

	if len(recorder.chatRequests) != 1 || recorder.chatRequests[0] != (chatRequestMetric{stream: false, outcome: metrics.ChatOutcomeSuccess}) {
		t.Fatalf("expected one non-stream success chat request metric, got %#v", recorder.chatRequests)
	}
	if len(recorder.upstream) != 1 || !recorder.upstream[0].success {
		t.Fatalf("expected one successful upstream metric, got %#v", recorder.upstream)
	}
	if recorder.upstream[0].provider != "9123" || recorder.upstream[0].channel != "123" {
		t.Fatalf("unexpected upstream labels: %#v", recorder.upstream[0])
	}
	if len(recorder.routing) != 1 || recorder.routing[0] != (routingMetric{provider: "9123", channel: "123", model: "openai/gpt-4.1"}) {
		t.Fatalf("expected one routing selected metric, got %#v", recorder.routing)
	}
	if len(recorder.settlements) != 1 || recorder.settlements[0] != metrics.SettlementOutcomeSuccess {
		t.Fatalf("expected one success settlement metric, got %#v", recorder.settlements)
	}
}

func TestChatCompletionServiceRecordsStreamSuccessMetrics(t *testing.T) {
	recorder := &fakeMetricsRecorder{}
	streamAdapter := &fakeChatAdapter{streamResp: []adapter.ChatStreamChunk{
		{ID: "c", Model: "gpt-4.1", Role: "assistant", Content: "hi"},
		streamUsageChunk("gpt-4.1"),
	}}
	router := &fakeChatRouter{plan: routePlan(routeCandidate("openai", 123, "gpt-4.1"))}
	registry := &fakeAdapterRegistry{
		streamChatAdapters: map[string]adapter.StreamChatAdapter{"openai": streamAdapter},
	}
	service := NewChatCompletionService(
		router,
		registry,
		ProviderErrorClassifier{},
		newFakeRequestLogService(),
		newChatCompletionSettlementForTest(),
		&fakeChatAuthorizer{authorization: ChatAuthorization{ReservationID: 1}},
		recorder,
		nil,
	)

	err := service.StreamChatCompletion(contextWithPrincipal(42), chatRequest(), func(gatewayapi.ChatCompletionStreamResponse) error {
		return nil
	})
	if err != nil {
		t.Fatalf("StreamChatCompletion returned err: %v", err)
	}

	if len(recorder.chatRequests) != 1 || recorder.chatRequests[0] != (chatRequestMetric{stream: true, outcome: metrics.ChatOutcomeSuccess}) {
		t.Fatalf("expected one stream success chat request metric, got %#v", recorder.chatRequests)
	}
	if !containsStreamEvent(recorder.streamEvents, metrics.StreamEventStarted) {
		t.Fatalf("expected stream started event, got %#v", recorder.streamEvents)
	}
	if !containsStreamEvent(recorder.streamEvents, metrics.StreamEventCompleted) {
		t.Fatalf("expected stream completed event, got %#v", recorder.streamEvents)
	}
	if len(recorder.settlements) != 1 || recorder.settlements[0] != metrics.SettlementOutcomeSuccess {
		t.Fatalf("expected one success settlement metric, got %#v", recorder.settlements)
	}
}

func containsStreamEvent(events []metrics.StreamEvent, want metrics.StreamEvent) bool {
	for _, event := range events {
		if event == want {
			return true
		}
	}

	return false
}
