package chatcompletions

import (
	"strings"
	"testing"

	chatcompletionsadapter "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/chatcompletions"
	"github.com/ThankCat/unio-gateway/internal/core/sessionhint"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/lifecycle"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/openai/chatcompletions/dto"
)

// TestCreateChatCompletionPassesUpstreamAffinityToAdapter 验证非流式 chat 编排把 Sticky 同源会话键与
// 客户 API Key 身份经 ctx 交给 adapter（反向桥接到号池时 Codex wire 据此派生上游会话身份）：
// 显式 prompt_cache_key 原值直达；无显式信号时落内容派生哈希。
func TestCreateChatCompletionPassesUpstreamAffinityToAdapter(t *testing.T) {
	run := func(t *testing.T, req dto.ChatCompletionRequest) sessionhint.UpstreamAffinity {
		t.Helper()
		fakeAdapter := &fakeChatAdapter{chatResp: chatResponse("adapter response")}
		router := &fakeChatRouter{plan: routePlan(routeCandidate("codex", 123, "gpt-5.5"))}
		registry := &fakeAdapterRegistry{chatAdapters: map[string]chatcompletionsadapter.ChatAdapter{"codex": fakeAdapter}}
		service := newChatCompletionServiceForTestWithAuthorizer(router, registry, nil, newFakeRequestLogService(), newChatCompletionSettlementForTest(), &fakeChatAuthorizer{
			authorization: lifecycle.ChatAuthorization{ReservationID: 7788},
		})
		if _, err := service.CreateChatCompletion(contextWithPrincipal(42), req); err != nil {
			t.Fatalf("CreateChatCompletion: %v", err)
		}
		if fakeAdapter.chatCalled != 1 {
			t.Fatalf("adapter called %d times, want 1", fakeAdapter.chatCalled)
		}
		return fakeAdapter.gotAffinity
	}

	explicit := chatRequest()
	key := "01a06086-5391-7d71-a82e-e5638a243ec4"
	explicit.PromptCacheKey = &key
	if got := run(t, explicit); got != (sessionhint.UpstreamAffinity{SessionKey: key, APIKeyID: 1}) {
		t.Fatalf("explicit prompt_cache_key affinity = %+v, want key %q for api key 1", got, key)
	}

	derived := run(t, chatRequest())
	if derived.APIKeyID != 1 || !strings.HasPrefix(derived.SessionKey, "content:") {
		t.Fatalf("content-derived affinity = %+v, want content hash for api key 1", derived)
	}
}

// TestStreamChatCompletionPassesUpstreamAffinityToAdapter 验证流式 chat 编排同样经 ctx 下发会话亲和事实。
func TestStreamChatCompletionPassesUpstreamAffinityToAdapter(t *testing.T) {
	fakeAdapter := &fakeChatAdapter{
		streamResp: []chatcompletionsadapter.ChatStreamChunk{
			{ID: "chatcmpl_mock", Model: "gpt-5.5", Role: "assistant", Content: "mock response"},
			streamUsageChunk("gpt-5.5"),
		},
	}
	router := &fakeChatRouter{plan: routePlan(routeCandidate("codex", 123, "gpt-5.5"))}
	registry := &fakeAdapterRegistry{streamChatAdapters: map[string]chatcompletionsadapter.StreamChatAdapter{"codex": fakeAdapter}}
	service := newChatCompletionServiceForTestWithAuthorizer(router, registry, nil, newFakeRequestLogService(), newChatCompletionSettlementForTest(), &fakeChatAuthorizer{
		authorization: lifecycle.ChatAuthorization{ReservationID: 8820},
	})

	req := chatRequest()
	key := "01a06086-5391-7d71-a82e-e5638a243ec4"
	req.PromptCacheKey = &key
	if err := service.StreamChatCompletion(contextWithPrincipal(42), req, func(dto.ChatCompletionStreamResponse) error { return nil }); err != nil {
		t.Fatalf("StreamChatCompletion: %v", err)
	}
	if fakeAdapter.streamCalled != 1 {
		t.Fatalf("stream adapter called %d times, want 1", fakeAdapter.streamCalled)
	}
	if got := fakeAdapter.gotAffinity; got != (sessionhint.UpstreamAffinity{SessionKey: key, APIKeyID: 1}) {
		t.Fatalf("stream affinity = %+v, want key %q for api key 1", got, key)
	}
}
