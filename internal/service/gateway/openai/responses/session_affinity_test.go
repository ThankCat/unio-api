package responses

import (
	"encoding/json"
	"strings"
	"testing"

	gatewayapi "github.com/ThankCat/unio-gateway/internal/app/gatewayapi/openai/responses"
	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	responsesadapter "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/responses"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/core/sessionhint"
	"github.com/ThankCat/unio-gateway/internal/core/usage"
)

// TestCreateResponse_PassesUpstreamAffinityToAdapter 验证非流式编排把 Sticky 同源的会话键与客户
// API Key 身份经 ctx 交给直传 adapter：显式 prompt_cache_key 原值直达；无显式信号时落内容派生哈希。
func TestCreateResponse_PassesUpstreamAffinityToAdapter(t *testing.T) {
	run := func(t *testing.T, req gatewayapi.ResponsesRequest) sessionhint.UpstreamAffinity {
		t.Helper()
		directAdapter := &fakeResponsesAdapter{resp: directResponse()}
		registry := &fakeRegistry{responsesAdapters: map[string]responsesadapter.ResponsesAdapter{"codex": directAdapter}}
		router := &fakeRouter{plan: routing.ChatRoutePlan{Candidates: []routing.ChatRouteCandidate{candidate("codex", 1, "gpt-5.5-upstream")}}}
		svc := newServiceForTest(router, registry, &fakeSettlement{}, &fakeAuthorizer{}, newFakeRequestLog())
		if _, err := svc.CreateResponse(ctxWithPrincipal(), req); err != nil {
			t.Fatalf("CreateResponse: %v", err)
		}
		if directAdapter.called != 1 {
			t.Fatalf("adapter called %d times, want 1", directAdapter.called)
		}
		return directAdapter.gotAffinity
	}

	explicit := directRequest()
	key := "01a06086-5391-7d71-a82e-e5638a243ec4"
	explicit.PromptCacheKey = &key
	if got := run(t, explicit); got != (sessionhint.UpstreamAffinity{SessionKey: key, APIKeyID: 1}) {
		t.Fatalf("explicit prompt_cache_key affinity = %+v, want key %q for api key 1", got, key)
	}

	// 内容派生兜底按 ingress 解码保留的 input 原文取前缀，故用真实 JSON 解码而非直构 DTO。
	derived := run(t, decodeRequest(t, `{"model":"gpt-5.5","input":"hello"}`))
	if derived.APIKeyID != 1 || !strings.HasPrefix(derived.SessionKey, "content:") {
		t.Fatalf("content-derived affinity = %+v, want content hash for api key 1", derived)
	}
}

// TestStreamResponse_PassesUpstreamAffinityToAdapter 验证流式编排同样把会话亲和事实经 ctx 交给直传 adapter。
func TestStreamResponse_PassesUpstreamAffinityToAdapter(t *testing.T) {
	u := adapter.ChatUsage{PromptTokens: 11, CompletionTokens: 5, TotalTokens: 16}
	directStream := &fakeStreamResponsesAdapter{
		chunks: []responsesadapter.StreamChunk{
			{EventType: "response.created", Data: json.RawMessage(`{"type":"response.created","response":{"id":"resp_1","model":"gpt-5.5-up","status":"in_progress"}}`)},
			{
				EventType:    "response.completed",
				Data:         json.RawMessage(`{"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.5-up","status":"completed","usage":{"input_tokens":11,"output_tokens":5,"total_tokens":16}}}`),
				ResponseID:   "resp_1",
				FinishReason: "completed",
				Usage:        &u,
			},
		},
		facts: &adapter.ResponseFacts{
			UpstreamProtocol:    "openai",
			UpstreamResponseID:  "resp_1",
			UpstreamModel:       "gpt-5.5-up",
			Finish:              adapter.FinishFacts{Class: adapter.FinishStop, RawReason: "completed"},
			Usage:               u.ToUsageFacts(),
			UsageSource:         usage.SourceUpstreamStream,
			UsageMappingVersion: "chatcompletionsadapter.responses.v1",
		},
	}
	registry := &fakeRegistry{streamResponsesAdapters: map[string]responsesadapter.StreamResponsesAdapter{"codex": directStream}}
	router := &fakeRouter{plan: routing.ChatRoutePlan{Candidates: []routing.ChatRouteCandidate{candidate("codex", 1, "gpt-5.5-up")}}}
	svc := newServiceForTest(router, registry, &fakeSettlement{}, &fakeAuthorizer{}, newFakeRequestLog())

	req := directRequest()
	key := "01a06086-5391-7d71-a82e-e5638a243ec4"
	req.PromptCacheKey = &key
	if err := svc.StreamResponse(ctxWithPrincipal(), req, func(gatewayapi.ResponsesStreamEvent) error { return nil }); err != nil {
		t.Fatalf("StreamResponse: %v", err)
	}
	if directStream.called != 1 {
		t.Fatalf("stream adapter called %d times, want 1", directStream.called)
	}
	if got := directStream.gotAffinity; got != (sessionhint.UpstreamAffinity{SessionKey: key, APIKeyID: 1}) {
		t.Fatalf("stream affinity = %+v, want key %q for api key 1", got, key)
	}
}
