package chatcompletions

import (
	"errors"
	"testing"

	gatewayapi "github.com/ThankCat/unio-gateway/internal/app/gatewayapi/openai/chatcompletions"
	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	chatcompletionsadapter "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/chatcompletions"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/lifecycle"
)

// 纯熔断器单测（ChannelCircuitBreaker / IsChannelFaultError）在 lifecycle/breaker_test.go；
// 本文件只保留熔断在 chat 编排 fallback 链路中的集成行为。

// fakeChannelBreaker 是 gateway 集成测试用的熔断器替身。
type fakeChannelBreaker struct {
	denied    map[string]bool
	successes []string
	failures  []string
}

func (f *fakeChannelBreaker) Allow(channelKey string) bool {
	return !f.denied[channelKey]
}

func (f *fakeChannelBreaker) Available(channelKey string) bool {
	return !f.denied[channelKey]
}

func (f *fakeChannelBreaker) HealthScore(channelKey string) float64 {
	if f.denied[channelKey] {
		return 1
	}
	return 0
}

func (f *fakeChannelBreaker) RecordSuccess(channelKey string) {
	f.successes = append(f.successes, channelKey)
}

func (f *fakeChannelBreaker) RecordFailure(channelKey string) {
	f.failures = append(f.failures, channelKey)
}

func newChatCompletionServiceWithBreaker(registry AdapterRegistry, router ChatRouter, breaker lifecycle.ChannelBreaker) *ChatCompletionService {
	return NewChatCompletionService(
		router,
		registry,
		passthroughCandidatePreparer{inputTokens: 1},
		lifecycle.ProviderErrorClassifier{},
		newFakeRequestLogService(),
		newChatCompletionSettlementForTest(),
		&fakeChatAuthorizer{authorization: lifecycle.ChatAuthorization{ReservationID: 1}},
		nil,
		breaker,
	)
}

func TestChatCompletionSkipsOpenChannel(t *testing.T) {
	breaker := &fakeChannelBreaker{denied: map[string]bool{"123": true}}
	fakeAdapter := &fakeChatAdapter{chatResp: chatResponse("ok")}
	router := &fakeChatRouter{plan: routePlan(
		routeCandidate("openai", 123, "gpt-4.1"),
		routeCandidate("openai", 456, "gpt-4.1"),
	)}
	registry := &fakeAdapterRegistry{
		chatAdapters: map[string]chatcompletionsadapter.ChatAdapter{"openai": fakeAdapter},
	}
	service := newChatCompletionServiceWithBreaker(registry, router, breaker)

	if _, err := service.CreateChatCompletion(contextWithPrincipal(42), chatRequest()); err != nil {
		t.Fatalf("CreateChatCompletion returned err: %v", err)
	}

	// 熔断的 channel 123 被跳过，只对放行的 channel 456 发起一次上游调用。
	if fakeAdapter.chatCalled != 1 {
		t.Fatalf("expected adapter to be called once, got %d", fakeAdapter.chatCalled)
	}
	if fakeAdapter.ch.ID != 456 {
		t.Fatalf("expected only the allowed channel 456 to be used, got %d", fakeAdapter.ch.ID)
	}
	if len(breaker.successes) != 1 || breaker.successes[0] != "456" {
		t.Fatalf("expected success recorded for channel 456, got %#v", breaker.successes)
	}
}

func TestChatCompletionRecordsChannelFailure(t *testing.T) {
	breaker := &fakeChannelBreaker{}
	upstreamErr := adapter.NewUpstreamError(
		adapter.UpstreamErrorServer,
		adapter.UpstreamMetadata{StatusCode: 502},
		failure.New(failure.CodeAdapterUpstreamStatus),
	)
	fakeAdapter := &fakeChatAdapter{chatErr: upstreamErr}
	router := &fakeChatRouter{plan: routePlan(routeCandidate("openai", 123, "gpt-4.1"))}
	registry := &fakeAdapterRegistry{
		chatAdapters: map[string]chatcompletionsadapter.ChatAdapter{"openai": fakeAdapter},
	}
	service := newChatCompletionServiceWithBreaker(registry, router, breaker)

	if _, err := service.CreateChatCompletion(contextWithPrincipal(42), chatRequest()); err == nil {
		t.Fatal("expected error from failing upstream")
	}

	if len(breaker.failures) != 1 || breaker.failures[0] != "123" {
		t.Fatalf("expected one channel failure recorded for 123, got %#v", breaker.failures)
	}
	if len(breaker.successes) != 0 {
		t.Fatalf("expected no successes, got %#v", breaker.successes)
	}
}

func TestChatCompletionReleasesAuthorizationWhenBreakerRaceDeniesAllCandidates(t *testing.T) {
	breaker := &fakeChannelBreaker{denied: map[string]bool{"123": true}}
	fakeAdapter := &fakeChatAdapter{chatResp: chatResponse("should not call")}
	requestLog := newFakeRequestLogService()
	authorizer := &fakeChatAuthorizer{
		authorization: lifecycle.ChatAuthorization{ReservationID: 8899},
	}
	service := NewChatCompletionService(
		&fakeChatRouter{plan: routePlan(routeCandidate("openai", 123, "gpt-4.1"))},
		&fakeAdapterRegistry{
			chatAdapters: map[string]chatcompletionsadapter.ChatAdapter{"openai": fakeAdapter},
		},
		passthroughCandidatePreparer{inputTokens: 1},
		lifecycle.ProviderErrorClassifier{},
		requestLog,
		newChatCompletionSettlementForTest(),
		authorizer,
		nil,
		breaker,
	)

	_, err := service.CreateChatCompletion(contextWithPrincipal(42), chatRequest())
	if !errors.Is(err, routing.ErrNoAvailableChannel) {
		t.Fatalf("expected ErrNoAvailableChannel, got %v", err)
	}
	if fakeAdapter.chatCalled != 0 {
		t.Fatalf("expected no adapter call after breaker race, got %d", fakeAdapter.chatCalled)
	}
	if len(requestLog.createAttempts) != 0 {
		t.Fatalf("expected no attempt after breaker race, got %d", len(requestLog.createAttempts))
	}
	if len(authorizer.authorizeParams) != 1 {
		t.Fatalf("expected one request authorization, got %d", len(authorizer.authorizeParams))
	}
	if len(authorizer.releaseParams) != 1 || authorizer.releaseParams[0].ReservationID != 8899 {
		t.Fatalf("expected reservation 8899 to be released, got %#v", authorizer.releaseParams)
	}
}

func TestStreamChatCompletionReleasesAuthorizationWhenBreakerRaceDeniesAllCandidates(t *testing.T) {
	breaker := &fakeChannelBreaker{denied: map[string]bool{"123": true}}
	fakeAdapter := &fakeChatAdapter{
		streamResp: []chatcompletionsadapter.ChatStreamChunk{streamUsageChunk("gpt-4.1")},
	}
	requestLog := newFakeRequestLogService()
	authorizer := &fakeChatAuthorizer{
		authorization: lifecycle.ChatAuthorization{ReservationID: 9909},
	}
	service := NewChatCompletionService(
		&fakeChatRouter{plan: routePlan(routeCandidate("openai", 123, "gpt-4.1"))},
		&fakeAdapterRegistry{
			streamChatAdapters: map[string]chatcompletionsadapter.StreamChatAdapter{"openai": fakeAdapter},
		},
		passthroughCandidatePreparer{inputTokens: 1},
		lifecycle.ProviderErrorClassifier{},
		requestLog,
		newChatCompletionSettlementForTest(),
		authorizer,
		nil,
		breaker,
	)

	err := service.StreamChatCompletion(contextWithPrincipal(42), chatRequest(), func(chunk gatewayapi.ChatCompletionStreamResponse) error {
		t.Fatalf("expected no stream chunk after breaker race, got %#v", chunk)
		return nil
	})
	if !errors.Is(err, routing.ErrNoAvailableChannel) {
		t.Fatalf("expected ErrNoAvailableChannel, got %v", err)
	}
	if fakeAdapter.streamCalled != 0 {
		t.Fatalf("expected no stream adapter call after breaker race, got %d", fakeAdapter.streamCalled)
	}
	if len(requestLog.createAttempts) != 0 {
		t.Fatalf("expected no stream attempt after breaker race, got %d", len(requestLog.createAttempts))
	}
	if len(authorizer.authorizeParams) != 1 {
		t.Fatalf("expected one stream authorization, got %d", len(authorizer.authorizeParams))
	}
	if len(authorizer.releaseParams) != 1 || authorizer.releaseParams[0].ReservationID != 9909 {
		t.Fatalf("expected reservation 9909 to be released, got %#v", authorizer.releaseParams)
	}
}
