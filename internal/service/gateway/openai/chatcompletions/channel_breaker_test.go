package gateway

import (
	"errors"
	"testing"
	"time"

	"github.com/ThankCat/unio-api/internal/core/adapter"
	"github.com/ThankCat/unio-api/internal/platform/failure"
)

// newTestBreaker 创建一个使用可控时钟的熔断器。
func newTestBreaker(cfg ChannelCircuitBreakerConfig) (*ChannelCircuitBreaker, *time.Time) {
	b := NewChannelCircuitBreaker(cfg)
	clock := time.Now()
	b.now = func() time.Time { return clock }

	return b, &clock
}

func TestChannelCircuitBreakerTripsAfterThreshold(t *testing.T) {
	b, _ := newTestBreaker(ChannelCircuitBreakerConfig{
		Window:       time.Minute,
		MinRequests:  4,
		FailureRatio: 0.5,
		OpenDuration: 10 * time.Second,
	})

	key := "1"
	// 未达到 MinRequests 时不熔断。
	b.RecordFailure(key)
	b.RecordFailure(key)
	b.RecordFailure(key)
	if !b.Allow(key) {
		t.Fatal("breaker should stay closed below MinRequests")
	}

	// 第 4 次失败：total=4 >= MinRequests，ratio=1.0 >= 0.5 → 熔断。
	b.RecordFailure(key)
	if b.Allow(key) {
		t.Fatal("breaker should be open after crossing failure threshold")
	}
}

func TestChannelCircuitBreakerHalfOpenRecovers(t *testing.T) {
	b, clock := newTestBreaker(ChannelCircuitBreakerConfig{
		Window:       time.Minute,
		MinRequests:  2,
		FailureRatio: 0.5,
		OpenDuration: 10 * time.Second,
	})

	key := "1"
	b.RecordFailure(key)
	b.RecordFailure(key)
	if b.Allow(key) {
		t.Fatal("breaker should be open")
	}

	// 冷却未到，仍然拒绝。
	*clock = clock.Add(5 * time.Second)
	if b.Allow(key) {
		t.Fatal("breaker should stay open within cooldown")
	}

	// 冷却到达，放行一次半开探测，且并发探测被拒绝。
	*clock = clock.Add(6 * time.Second)
	if !b.Allow(key) {
		t.Fatal("breaker should allow a half-open probe after cooldown")
	}
	if b.Allow(key) {
		t.Fatal("breaker should deny concurrent half-open probe")
	}

	// 探测成功 → 恢复闭合。
	b.RecordSuccess(key)
	if !b.Allow(key) {
		t.Fatal("breaker should close after successful probe")
	}
}

func TestChannelCircuitBreakerReopensOnProbeFailure(t *testing.T) {
	b, clock := newTestBreaker(ChannelCircuitBreakerConfig{
		Window:       time.Minute,
		MinRequests:  2,
		FailureRatio: 0.5,
		OpenDuration: 10 * time.Second,
	})

	key := "1"
	b.RecordFailure(key)
	b.RecordFailure(key)

	*clock = clock.Add(11 * time.Second)
	if !b.Allow(key) {
		t.Fatal("breaker should allow a half-open probe")
	}

	// 探测失败 → 重新熔断。
	b.RecordFailure(key)
	if b.Allow(key) {
		t.Fatal("breaker should re-open after a failed probe")
	}
}

func TestChannelCircuitBreakerWindowResetsCounts(t *testing.T) {
	b, clock := newTestBreaker(ChannelCircuitBreakerConfig{
		Window:       time.Minute,
		MinRequests:  3,
		FailureRatio: 0.9,
		OpenDuration: 10 * time.Second,
	})

	key := "1"
	b.RecordFailure(key)
	b.RecordFailure(key)

	// 跨过窗口后计数清零，旧失败不再累计触发熔断。
	*clock = clock.Add(2 * time.Minute)
	b.RecordFailure(key)
	if !b.Allow(key) {
		t.Fatal("breaker should stay closed after window reset clears old failures")
	}
}

func TestIsChannelFaultError(t *testing.T) {
	faulty := []adapter.UpstreamErrorCategory{
		adapter.UpstreamErrorTimeout,
		adapter.UpstreamErrorServer,
		adapter.UpstreamErrorRateLimit,
		adapter.UpstreamErrorAuth,
		adapter.UpstreamErrorPermission,
	}
	for _, category := range faulty {
		err := adapter.NewUpstreamError(category, adapter.UpstreamMetadata{}, failure.New(failure.CodeAdapterUpstreamStatus))
		if !isChannelFaultError(err) {
			t.Errorf("category %q should be channel fault", category)
		}
	}

	notFaulty := []error{
		nil,
		adapter.NewUpstreamError(adapter.UpstreamErrorBadRequest, adapter.UpstreamMetadata{}, failure.New(failure.CodeAdapterUpstreamStatus)),
		adapter.NewUpstreamError(adapter.UpstreamErrorCanceled, adapter.UpstreamMetadata{}, failure.New(failure.CodeAdapterUpstreamStatus)),
		errors.New("non-upstream error"),
	}
	for _, err := range notFaulty {
		if isChannelFaultError(err) {
			t.Errorf("error %v should not be channel fault", err)
		}
	}
}

// fakeChannelBreaker 是 gateway 集成测试用的熔断器替身。
type fakeChannelBreaker struct {
	denied    map[string]bool
	successes []string
	failures  []string
}

func (f *fakeChannelBreaker) Allow(channelKey string) bool {
	return !f.denied[channelKey]
}

func (f *fakeChannelBreaker) RecordSuccess(channelKey string) {
	f.successes = append(f.successes, channelKey)
}

func (f *fakeChannelBreaker) RecordFailure(channelKey string) {
	f.failures = append(f.failures, channelKey)
}

func newChatCompletionServiceWithBreaker(registry AdapterRegistry, router ChatRouter, breaker ChannelBreaker) *ChatCompletionService {
	return NewChatCompletionService(
		router,
		registry,
		ProviderErrorClassifier{},
		newFakeRequestLogService(),
		newChatCompletionSettlementForTest(),
		&fakeChatAuthorizer{authorization: ChatAuthorization{ReservationID: 1}},
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
		chatAdapters: map[string]adapter.ChatAdapter{"openai": fakeAdapter},
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
		chatAdapters: map[string]adapter.ChatAdapter{"openai": fakeAdapter},
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
