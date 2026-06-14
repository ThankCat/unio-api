package responses

import (
	"context"
	"encoding/json"
	"testing"

	gatewayapi "github.com/ThankCat/unio-api/internal/app/gatewayapi/openai/responses"
	"github.com/ThankCat/unio-api/internal/core/adapter"
	chatcompletionsadapter "github.com/ThankCat/unio-api/internal/core/adapter/openai/chatcompletions"
	responsesadapter "github.com/ThankCat/unio-api/internal/core/adapter/openai/responses"
	"github.com/ThankCat/unio-api/internal/core/channel"
	"github.com/ThankCat/unio-api/internal/core/requestlog"
	"github.com/ThankCat/unio-api/internal/core/routing"
	"github.com/ThankCat/unio-api/internal/core/usage"
)

// fakeResponsesAdapter 是 responses 直传 adapter 的测试替身：记录上送请求体并返回预置原文响应。
type fakeResponsesAdapter struct {
	called  int
	gotBody json.RawMessage
	resp    *responsesadapter.Response
	err     error
}

func (a *fakeResponsesAdapter) CreateResponse(_ context.Context, _ channel.Runtime, req responsesadapter.Request) (*responsesadapter.Response, error) {
	a.called++
	a.gotBody = req.Body
	if a.err != nil {
		return nil, a.err
	}
	return a.resp, nil
}

func directResponse() *responsesadapter.Response {
	raw := json.RawMessage(`{"id":"resp_up","object":"response","status":"completed","model":"gpt-5.5-upstream","output":[],"usage":{"input_tokens":11,"output_tokens":5,"total_tokens":16}}`)
	meta := adapter.UpstreamMetadata{StatusCode: 200, RequestID: "req-up"}
	return &responsesadapter.Response{
		Raw:        raw,
		ResponseID: "resp_up",
		Model:      "gpt-5.5-upstream",
		Usage:      adapter.ChatUsage{PromptTokens: 11, CompletionTokens: 5, TotalTokens: 16},
		Upstream:   meta,
		Facts: adapter.ResponseFacts{
			UpstreamProtocol:    "openai",
			UpstreamResponseID:  "resp_up",
			UpstreamModel:       "gpt-5.5-upstream",
			Finish:              adapter.FinishFacts{Class: adapter.FinishStop, RawReason: "stop"},
			UsageMappingVersion: "chatcompletionsadapter.responses.v1",
			Metadata:            meta,
		},
	}
}

func directRequest() gatewayapi.ResponsesRequest {
	text := "hello"
	return gatewayapi.ResponsesRequest{
		Model: "gpt-5.5",
		Input: gatewayapi.ResponsesInput{Text: &text},
	}
}

// TestCreateResponse_DirectPassthrough 验证：候选 adapter 注册了 responses 直传能力时，
// 走直传分支——上送上游请求体的 model 改写为 upstream model、stream=false；上游响应体原文透传，
// 仅顶层 model 回显改写为客户请求名；settlement 落直传产出的 ResponseFacts。
func TestCreateResponse_DirectPassthrough(t *testing.T) {
	directAdapter := &fakeResponsesAdapter{resp: directResponse()}
	// chat 适配器同时存在但不应被触达（直传候选不落桥接）。
	chatAdapter := &fakeChatAdapter{resp: okChatResponse()}
	registry := &fakeRegistry{
		adapters:          map[string]chatcompletionsadapter.ChatAdapter{"deepseek": chatAdapter},
		responsesAdapters: map[string]responsesadapter.ResponsesAdapter{"openai": directAdapter},
	}
	router := &fakeRouter{plan: routing.ChatRoutePlan{Candidates: []routing.ChatRouteCandidate{candidate("openai", 1, "gpt-5.5-upstream")}}}
	settlement := &fakeSettlement{}
	authorizer := &fakeAuthorizer{}
	requestLog := newFakeRequestLog()

	svc := newServiceForTest(router, registry, settlement, authorizer, requestLog)

	resp, err := svc.CreateResponse(ctxWithPrincipal(), directRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 直传 adapter 命中一次，桥接 chat adapter 完全未触达。
	if directAdapter.called != 1 {
		t.Fatalf("expected direct adapter called once, got %d", directAdapter.called)
	}
	if chatAdapter.req.Model != "" {
		t.Fatalf("chat bridge adapter must not be invoked for direct candidate, got model %q", chatAdapter.req.Model)
	}

	// 上送上游请求体：model→upstream model，stream=false。
	var upBody map[string]json.RawMessage
	if err := json.Unmarshal(directAdapter.gotBody, &upBody); err != nil {
		t.Fatalf("decode upstream body: %v", err)
	}
	if string(upBody["model"]) != `"gpt-5.5-upstream"` {
		t.Fatalf("upstream model = %s, want \"gpt-5.5-upstream\"", upBody["model"])
	}
	if string(upBody["stream"]) != "false" {
		t.Fatalf("upstream stream = %s, want false", upBody["stream"])
	}

	// settlement 落直传 ResponseFacts（协议无关账务事实）。
	if len(settlement.params) != 1 {
		t.Fatalf("expected 1 settlement, got %d", len(settlement.params))
	}
	if settlement.params[0].ResponseProtocol != requestlog.ProtocolOpenAI {
		t.Fatalf("settlement protocol = %q, want openai", settlement.params[0].ResponseProtocol)
	}

	// 客户响应：上游原文透传，仅顶层 model 回显改写为客户请求名。
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got["model"] != "gpt-5.5" {
		t.Fatalf("client model = %v, want gpt-5.5 (rewritten)", got["model"])
	}
	if got["id"] != "resp_up" || got["status"] != "completed" {
		t.Fatalf("raw passthrough lost fields: %v", got)
	}
}

// TestCreateResponse_DiversionDeepseekToBridge 是分流回归断言：即便在生产 allowDirect=true 路径下，
// chat-only 第三方（deepseek，未注册 responses 直传能力）也必须落到既有 responses→chat 桥接分支——
// 直传 adapter 零触达，行为与 DEC-014 现状一致。
func TestCreateResponse_DiversionDeepseekToBridge(t *testing.T) {
	directAdapter := &fakeResponsesAdapter{resp: directResponse()}
	chatAdapter := &fakeChatAdapter{resp: okChatResponse()}
	// 注册表同时含直传与桥接 adapter，但 deepseek 仅有 chat 能力。
	registry := &fakeRegistry{
		adapters:          map[string]chatcompletionsadapter.ChatAdapter{"deepseek": chatAdapter},
		tokenizers:        map[string]chatcompletionsadapter.ChatInputTokenizer{"deepseek": fakeTokenizer{}},
		responsesAdapters: map[string]responsesadapter.ResponsesAdapter{"openai": directAdapter},
	}
	router := &fakeRouter{plan: routing.ChatRoutePlan{Candidates: []routing.ChatRouteCandidate{candidate("deepseek", 1, "deepseek-v4-flash")}}}
	settlement := &fakeSettlement{}
	authorizer := &fakeAuthorizer{}
	requestLog := newFakeRequestLog()

	svc := newServiceForTest(router, registry, settlement, authorizer, requestLog)

	resp, err := svc.CreateResponse(ctxWithPrincipal(), instructionsRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 直传 adapter 零触达；桥接 chat adapter 命中。
	if directAdapter.called != 0 {
		t.Fatalf("direct adapter must not be invoked for chat-only candidate, called %d", directAdapter.called)
	}
	if chatAdapter.req.Model != "deepseek-v4-flash" {
		t.Fatalf("expected bridge chat adapter invoked with upstream model, got %q", chatAdapter.req.Model)
	}

	// 桥接响应翻译回 Responses 形状（非原文透传）。
	if resp.Object != "response" || resp.Model != "unio-deepseek" || resp.Status != "completed" {
		t.Fatalf("unexpected bridged response envelope: %+v", resp)
	}
}

// fakeStreamResponsesAdapter 是 responses 直传流式 adapter 的测试替身：按序 emit 预置事件并返回 facts。
type fakeStreamResponsesAdapter struct {
	called  int
	gotBody json.RawMessage
	chunks  []responsesadapter.StreamChunk
	facts   *adapter.ResponseFacts
	err     error
}

func (a *fakeStreamResponsesAdapter) StreamResponse(_ context.Context, _ channel.Runtime, req responsesadapter.Request, emit func(responsesadapter.StreamChunk) error) (adapter.StreamOutcome, error) {
	a.called++
	a.gotBody = req.Body
	for _, c := range a.chunks {
		if err := emit(c); err != nil {
			return adapter.StreamOutcome{Facts: a.facts}, err
		}
	}
	return adapter.StreamOutcome{Facts: a.facts}, a.err
}

// TestStreamResponse_DirectPassthrough 是直传流式端到端回归：经真实 RunStreamGeneric 资金关键循环，
// 上游 SSE 命名事件原文透传给客户（仅 response.model 回显改写为客户请求名），response.completed 不被
// 二次补发，settlement 落直传 facts。
func TestStreamResponse_DirectPassthrough(t *testing.T) {
	u := adapter.ChatUsage{PromptTokens: 11, CompletionTokens: 5, TotalTokens: 16}
	directStream := &fakeStreamResponsesAdapter{
		chunks: []responsesadapter.StreamChunk{
			{EventType: "response.created", Data: json.RawMessage(`{"type":"response.created","response":{"id":"resp_1","model":"gpt-5.5-up","status":"in_progress"}}`)},
			{EventType: "response.output_text.delta", Data: json.RawMessage(`{"type":"response.output_text.delta","delta":"hi"}`)},
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
	registry := &fakeRegistry{
		streamResponsesAdapters: map[string]responsesadapter.StreamResponsesAdapter{"openai": directStream},
	}
	router := &fakeRouter{plan: routing.ChatRoutePlan{Candidates: []routing.ChatRouteCandidate{candidate("openai", 1, "gpt-5.5-up")}}}
	settlement := &fakeSettlement{}
	authorizer := &fakeAuthorizer{}
	requestLog := newFakeRequestLog()

	svc := newServiceForTest(router, registry, settlement, authorizer, requestLog)

	var events []gatewayapi.ResponsesStreamEvent
	err := svc.StreamResponse(ctxWithPrincipal(), directRequest(), func(ev gatewayapi.ResponsesStreamEvent) error {
		events = append(events, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if directStream.called != 1 {
		t.Fatalf("expected direct stream adapter called once, got %d", directStream.called)
	}

	// 上送上游请求体 stream=true + model→upstream model。
	var upBody map[string]json.RawMessage
	if err := json.Unmarshal(directStream.gotBody, &upBody); err != nil {
		t.Fatalf("decode upstream body: %v", err)
	}
	if string(upBody["stream"]) != "true" {
		t.Fatalf("upstream stream = %s, want true", upBody["stream"])
	}
	if string(upBody["model"]) != `"gpt-5.5-up"` {
		t.Fatalf("upstream model = %s, want \"gpt-5.5-up\"", upBody["model"])
	}

	// 客户收到的事件即上游事件原文透传：数量一致、不补发收尾帧。
	if len(events) != 3 {
		t.Fatalf("got %d client events, want 3 (verbatim passthrough, no synthesized completed)", len(events))
	}
	if events[0].Type != "response.created" || events[2].Type != "response.completed" {
		t.Fatalf("event sequence = %v, want created..completed", eventTypes(events))
	}

	// response.model 回显改写为客户请求名（gpt-5.5），其余字段原样保留。
	createdData, err := json.Marshal(events[0])
	if err != nil {
		t.Fatalf("marshal created event: %v", err)
	}
	var created struct {
		Response struct {
			ID    string `json:"id"`
			Model string `json:"model"`
		} `json:"response"`
	}
	if err := json.Unmarshal(createdData, &created); err != nil {
		t.Fatalf("decode created event: %v", err)
	}
	if created.Response.Model != "gpt-5.5" {
		t.Fatalf("response.model = %q, want gpt-5.5 (rewritten)", created.Response.Model)
	}
	if created.Response.ID != "resp_1" {
		t.Fatalf("response.id = %q, want resp_1 (preserved)", created.Response.ID)
	}

	// settlement 落直传 facts。
	if len(settlement.params) != 1 {
		t.Fatalf("expected 1 settlement, got %d", len(settlement.params))
	}
	if settlement.params[0].ResponseProtocol != requestlog.ProtocolOpenAI {
		t.Fatalf("settlement protocol = %q, want openai", settlement.params[0].ResponseProtocol)
	}
}
