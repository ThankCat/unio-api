package chatcompletions

import (
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/ThankCat/unio-api/internal/core/adapter/openai"
)

// TestChatCompletionServiceCreatesSpanHierarchy 验证非流式成功请求产生 gateway/routing/adapter/settlement span，
// 且它们同属一条 trace。
func TestChatCompletionServiceCreatesSpanHierarchy(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
	t.Cleanup(func() {
		otel.SetTracerProvider(sdktrace.NewTracerProvider())
	})

	fakeAdapter := &fakeChatAdapter{chatResp: chatResponse("ok")}
	router := &fakeChatRouter{plan: routePlan(routeCandidate("openai", 123, "gpt-4.1"))}
	registry := &fakeAdapterRegistry{
		chatAdapters: map[string]openai.ChatAdapter{"openai": fakeAdapter},
	}
	service := NewChatCompletionService(
		router,
		registry,
		passthroughCandidatePreparer{inputTokens: 1},
		ProviderErrorClassifier{},
		newFakeRequestLogService(),
		newChatCompletionSettlementForTest(),
		&fakeChatAuthorizer{authorization: ChatAuthorization{ReservationID: 1}},
		nil,
		nil,
	)

	if _, err := service.CreateChatCompletion(contextWithPrincipal(42), chatRequest()); err != nil {
		t.Fatalf("CreateChatCompletion returned err: %v", err)
	}

	spans := recorder.Ended()
	names := make(map[string]bool)
	traceIDs := make(map[string]bool)
	for _, span := range spans {
		names[span.Name()] = true
		traceIDs[span.SpanContext().TraceID().String()] = true
	}

	for _, want := range []string{"gateway.chat_completion", "gateway.routing", "adapter.chat_completions", "gateway.settlement"} {
		if !names[want] {
			t.Errorf("missing span %q (got %v)", want, names)
		}
	}

	if len(traceIDs) != 1 {
		t.Fatalf("expected all spans to share one trace, got %d trace ids", len(traceIDs))
	}
}
