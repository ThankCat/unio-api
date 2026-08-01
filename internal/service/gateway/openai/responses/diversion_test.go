package responses

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	gatewayapi "github.com/ThankCat/unio-gateway/internal/app/gatewayapi/openai/responses"
	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	chatcompletionsadapter "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/chatcompletions"
	responsesadapter "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/responses"
	"github.com/ThankCat/unio-gateway/internal/core/channel"
	"github.com/ThankCat/unio-gateway/internal/core/requestlog"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/core/usage"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/lifecycle"
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

func TestEncodeUpstreamResponsesBodyPreservesOutputLimitPresence(t *testing.T) {
	for _, tc := range []struct {
		name      string
		body      string
		wantValue string
	}{
		{name: "omitted", body: `{"model":"gpt-5.5","input":"hello"}`},
		{name: "explicit", body: `{"model":"gpt-5.5","input":"hello","max_output_tokens":4097}`, wantValue: "4097"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := decodeRequest(t, tc.body)
			encoded, err := encodeUpstreamResponsesBody(req, "gpt-5.5-upstream", false)
			if err != nil {
				t.Fatalf("encode upstream Responses/Compact body: %v", err)
			}
			var wire map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &wire); err != nil {
				t.Fatalf("decode upstream Responses/Compact body: %v", err)
			}
			value, present := wire["max_output_tokens"]
			if tc.wantValue == "" && present {
				t.Fatalf("omitted max_output_tokens was injected as %s", value)
			}
			if tc.wantValue != "" && (!present || string(value) != tc.wantValue) {
				t.Fatalf("explicit max_output_tokens=%s, want %s", value, tc.wantValue)
			}
		})
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

	result, err := svc.CreateResponse(ctxWithPrincipal(), directRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	resp := result.Response
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

	result, err := svc.CreateResponse(ctxWithPrincipal(), instructionsRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	resp := result.Response
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

type fakeBridgeStreamChatAdapter struct {
	called int
	chunks []chatcompletionsadapter.ChatStreamChunk
	facts  *adapter.ResponseFacts
	err    error
}

func (a *fakeBridgeStreamChatAdapter) StreamChatCompletions(
	ctx context.Context,
	_ channel.Runtime,
	_ chatcompletionsadapter.ChatRequest,
	emit func(chatcompletionsadapter.ChatStreamChunk) error,
) (adapter.StreamOutcome, error) {
	a.called++
	adapter.MarkTransportStarted(ctx)
	adapter.MarkRequestWritten(ctx, nil)
	adapter.MarkResponseHeadersReceived(ctx, adapter.UpstreamMetadata{StatusCode: http.StatusOK})
	for _, chunk := range a.chunks {
		if chatcompletionsadapter.FirstTokenPayload(chunk) != "" {
			adapter.MarkFirstTokenEligible(ctx)
		}
		if err := emit(chunk); err != nil {
			return adapter.StreamOutcome{Facts: a.facts}, err
		}
	}
	return adapter.StreamOutcome{Facts: a.facts}, a.err
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
// 上游 SSE 命名事件原文透传给客户（仅 response.model 回显改写为客户请求名），response.completed 在
// settlement 后原样交付且不重复，settlement 落直传 facts。
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
	terminalAfterSettlement := false
	err := svc.StreamResponse(ctxWithPrincipal(), directRequest(), func(ev gatewayapi.ResponsesStreamEvent) error {
		events = append(events, ev)
		if ev.Type == "response.completed" {
			terminalAfterSettlement = len(settlement.params) == 1
		}
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
	if !terminalAfterSettlement {
		t.Fatal("response.completed must be delivered after durable settlement")
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

func TestStreamResponse_DirectPartialSettlementCountsVisibleText(t *testing.T) {
	directStream := &fakeStreamResponsesAdapter{
		chunks: []responsesadapter.StreamChunk{
			{EventType: "response.created", Data: json.RawMessage(`{"type":"response.created","response":{"id":"resp_1","model":"gpt-5.5-up","status":"in_progress"}}`)},
			{EventType: "response.output_text.delta", Data: json.RawMessage(`{"type":"response.output_text.delta","delta":"partial visible answer"}`)},
			{
				EventType:    "response.completed",
				Data:         json.RawMessage(`{"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.5-up","status":"completed"}}`),
				ResponseID:   "resp_1",
				FinishReason: "completed",
			},
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
	terminalAfterSettlement := false
	err := svc.StreamResponse(ctxWithPrincipal(), directRequest(), func(ev gatewayapi.ResponsesStreamEvent) error {
		events = append(events, ev)
		if ev.Type == "response.completed" {
			terminalAfterSettlement = len(settlement.params) == 1
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected partial settlement without error, got %v", err)
	}
	if len(settlement.params) != 1 {
		t.Fatalf("expected one partial settlement, got %d", len(settlement.params))
	}
	facts := settlement.params[0].Facts
	if facts.UsageSource != usage.SourcePartialStreamEstimate {
		t.Fatalf("usage source = %q, want partial_stream_estimate", facts.UsageSource)
	}
	if facts.Finish.RawReason != lifecycle.PartialReasonFinalUsageMissing {
		t.Fatalf("finish reason = %q, want %q", facts.Finish.RawReason, lifecycle.PartialReasonFinalUsageMissing)
	}
	if !facts.Usage.OutputTokensTotal.IsKnown() || facts.Usage.OutputTokensTotal.Value <= 0 {
		t.Fatalf("expected estimated output tokens > 0, got %#v", facts.Usage.OutputTokensTotal)
	}
	if got := eventTypes(events); len(got) != 3 || got[0] != "response.created" || got[2] != "response.completed" {
		t.Fatalf("event sequence = %v, want created, delta, completed", got)
	}
	if !terminalAfterSettlement {
		t.Fatal("usage-missing response.completed must be delivered after partial settlement")
	}
}

func TestStreamResponse_DirectZeroOutputSettlesWithoutGatewayFirstToken(t *testing.T) {
	finalUsage := adapter.ChatUsage{PromptTokens: 2, CompletionTokens: 0, TotalTokens: 2}
	directStream := &fakeStreamResponsesAdapter{
		chunks: []responsesadapter.StreamChunk{
			{EventType: "response.created", Data: json.RawMessage(`{"type":"response.created","response":{"id":"resp_zero","model":"gpt-5.5-up","status":"in_progress"}}`)},
			{
				EventType:    "response.completed",
				Data:         json.RawMessage(`{"type":"response.completed","response":{"id":"resp_zero","model":"gpt-5.5-up","status":"completed","usage":{"input_tokens":2,"output_tokens":0,"total_tokens":2}}}`),
				ResponseID:   "resp_zero",
				FinishReason: "completed",
				Usage:        &finalUsage,
			},
		},
		facts: &adapter.ResponseFacts{
			UpstreamProtocol:    "openai",
			UpstreamResponseID:  "resp_zero",
			UpstreamModel:       "gpt-5.5-up",
			Finish:              adapter.FinishFacts{Class: adapter.FinishStop, RawReason: "completed"},
			Usage:               finalUsage.ToUsageFacts(),
			UsageSource:         usage.SourceUpstreamStream,
			UsageMappingVersion: "responses.v1",
		},
	}
	registry := &fakeRegistry{
		streamResponsesAdapters: map[string]responsesadapter.StreamResponsesAdapter{"openai": directStream},
	}
	settlement := &fakeSettlement{}
	svc := newServiceForTest(
		&fakeRouter{plan: routing.ChatRoutePlan{Candidates: []routing.ChatRouteCandidate{candidate("openai", 1, "gpt-5.5-up")}}},
		registry,
		settlement,
		&fakeAuthorizer{},
		newFakeRequestLog(),
	)

	var events []gatewayapi.ResponsesStreamEvent
	err := svc.StreamResponse(ctxWithPrincipal(), directRequest(), func(ev gatewayapi.ResponsesStreamEvent) error {
		events = append(events, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected zero-output stream error: %v", err)
	}
	if len(settlement.params) != 1 {
		t.Fatalf("settlement calls = %d, want 1", len(settlement.params))
	}
	if settlement.params[0].GatewayFirstTokenAt != nil {
		t.Fatalf("zero-output GatewayFirstTokenAt = %v, want nil", settlement.params[0].GatewayFirstTokenAt)
	}
	if got := eventTypes(events); len(got) != 2 || got[0] != "response.created" || got[1] != "response.completed" {
		t.Fatalf("zero-output events = %v, want created, completed", got)
	}
}

func TestStreamResponse_PreludeLimitFallsBackWithoutLeakingFirstCandidate(t *testing.T) {
	prelude := make([]responsesadapter.StreamChunk, 65)
	for i := range prelude {
		prelude[i] = responsesadapter.StreamChunk{
			EventType: "response.created",
			Data:      json.RawMessage(`{"type":"response.created","response":{"id":"resp_first","status":"in_progress"}}`),
		}
	}
	first := &fakeStreamResponsesAdapter{chunks: prelude}
	usageFacts := adapter.ChatUsage{PromptTokens: 2, CompletionTokens: 1, TotalTokens: 3}
	second := &fakeStreamResponsesAdapter{
		chunks: []responsesadapter.StreamChunk{
			{EventType: "response.created", Data: json.RawMessage(`{"type":"response.created","response":{"id":"resp_second","status":"in_progress"}}`)},
			{EventType: "response.output_text.delta", Data: json.RawMessage(`{"type":"response.output_text.delta","delta":"ok"}`)},
			{EventType: "response.completed", Data: json.RawMessage(`{"type":"response.completed","response":{"id":"resp_second","status":"completed"}}`), ResponseID: "resp_second", FinishReason: "completed", Usage: &usageFacts},
		},
		facts: &adapter.ResponseFacts{
			UpstreamProtocol:    "openai",
			UpstreamResponseID:  "resp_second",
			UpstreamModel:       "gpt-second",
			Finish:              adapter.FinishFacts{Class: adapter.FinishStop, RawReason: "completed"},
			Usage:               usageFacts.ToUsageFacts(),
			UsageSource:         usage.SourceUpstreamStream,
			UsageMappingVersion: "responses.v1",
		},
	}
	registry := &fakeRegistry{streamResponsesAdapters: map[string]responsesadapter.StreamResponsesAdapter{
		"first": first, "second": second,
	}}
	router := &fakeRouter{plan: routing.ChatRoutePlan{Candidates: []routing.ChatRouteCandidate{
		candidate("first", 1, "gpt-first"),
		candidate("second", 2, "gpt-second"),
	}}}
	settlement := &fakeSettlement{}
	svc := NewResponsesService(
		router, registry, passthroughPreparer{}, lifecycle.ProviderErrorClassifier{},
		newFakeRequestLog(), settlement, &fakeAuthorizer{}, nil, nil,
	)

	var events []gatewayapi.ResponsesStreamEvent
	err := svc.StreamResponse(ctxWithPrincipal(), directRequest(), func(event gatewayapi.ResponsesStreamEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatalf("prelude fallback: %v", err)
	}
	if first.called != 1 || second.called != 1 {
		t.Fatalf("adapter calls first=%d second=%d, want 1/1", first.called, second.called)
	}
	if len(events) != 3 || events[0].Type != "response.created" || events[1].Type != "response.output_text.delta" {
		t.Fatalf("client events leaked first candidate: %v", eventTypes(events))
	}
	created, err := json.Marshal(events[0])
	if err != nil {
		t.Fatalf("marshal created: %v", err)
	}
	var createdEnvelope struct {
		Response struct {
			ID string `json:"id"`
		} `json:"response"`
	}
	if err := json.Unmarshal(created, &createdEnvelope); err != nil || createdEnvelope.Response.ID != "resp_second" {
		t.Fatalf("unexpected created event after fallback: body=%s err=%v", created, err)
	}
	if len(settlement.params) != 1 || settlement.params[0].Facts.UpstreamResponseID != "resp_second" {
		t.Fatalf("settlement did not use fallback candidate: %+v", settlement.params)
	}
}

func TestStreamResponse_BridgeRefusalIsDeliveredBeforePartialSettlementTerminal(t *testing.T) {
	bridge := &fakeBridgeStreamChatAdapter{
		chunks: []chatcompletionsadapter.ChatStreamChunk{{
			ID: "chatcmpl_refusal", Model: "deepseek-v4", Refusal: strptr("refused"),
		}},
	}
	registry := &fakeRegistry{
		streamAdapters: map[string]chatcompletionsadapter.StreamChatAdapter{"deepseek": bridge},
		tokenizers:     map[string]chatcompletionsadapter.ChatInputTokenizer{"deepseek": fakeTokenizer{}},
	}
	router := &fakeRouter{plan: routing.ChatRoutePlan{Candidates: []routing.ChatRouteCandidate{
		candidate("deepseek", 1, "deepseek-v4"),
	}}}
	settlement := &fakeSettlement{}
	svc := newServiceForTest(router, registry, settlement, &fakeAuthorizer{}, newFakeRequestLog())

	var events []gatewayapi.ResponsesStreamEvent
	err := svc.StreamResponse(ctxWithPrincipal(), directRequest(), func(event gatewayapi.ResponsesStreamEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatalf("stream refusal: %v", err)
	}
	if bridge.called != 1 || len(settlement.params) != 1 {
		t.Fatalf("bridge calls=%d settlements=%d, want 1/1", bridge.called, len(settlement.params))
	}
	refusalIndex, completedIndex := -1, -1
	for i, event := range events {
		switch event.Type {
		case gatewayapi.EventRefusalDelta:
			if event.Delta != "refused" {
				t.Fatalf("refusal delta = %q, want refused", event.Delta)
			}
			refusalIndex = i
		case gatewayapi.EventResponseCompleted:
			completedIndex = i
		}
	}
	if refusalIndex < 0 || completedIndex < 0 || refusalIndex >= completedIndex {
		t.Fatalf("bridge refusal/terminal sequence = %v", eventTypes(events))
	}
	facts := settlement.params[0].Facts
	if facts.UsageSource != usage.SourcePartialStreamEstimate ||
		!facts.Usage.OutputTokensTotal.IsKnown() || facts.Usage.OutputTokensTotal.Value <= 0 {
		t.Fatalf("partial refusal settlement facts = %#v", facts)
	}
}

type alwaysRetryClassifier struct{}

func (alwaysRetryClassifier) IsRetryable(error) bool { return true }

func TestStreamResponse_BridgeTokenWriteFailureDoesNotFallbackOrCharge(t *testing.T) {
	first := &fakeBridgeStreamChatAdapter{
		chunks: []chatcompletionsadapter.ChatStreamChunk{{
			ID: "chatcmpl_first", Model: "deepseek-v4", Content: "hello",
		}},
	}
	second := &fakeBridgeStreamChatAdapter{
		chunks: []chatcompletionsadapter.ChatStreamChunk{{
			ID: "chatcmpl_second", Model: "deepseek-v4-backup", Content: "backup",
		}},
	}
	registry := &fakeRegistry{
		streamAdapters: map[string]chatcompletionsadapter.StreamChatAdapter{
			"first": first, "second": second,
		},
		tokenizers: map[string]chatcompletionsadapter.ChatInputTokenizer{
			"first": fakeTokenizer{}, "second": fakeTokenizer{},
		},
	}
	requestLog := newFakeRequestLog()
	settlement := &fakeSettlement{}
	authorizer := &fakeAuthorizer{}
	svc := NewResponsesService(
		&fakeRouter{plan: routing.ChatRoutePlan{Candidates: []routing.ChatRouteCandidate{
			candidate("first", 1, "deepseek-v4"),
			candidate("second", 2, "deepseek-v4-backup"),
		}}},
		registry,
		passthroughPreparer{},
		alwaysRetryClassifier{},
		requestLog,
		settlement,
		authorizer,
		nil,
		nil,
	)

	writeErr := errors.New("client rejected first token frame")
	var delivered []string
	tokenWriteAttempted := false
	err := svc.StreamResponse(ctxWithPrincipal(), directRequest(), func(event gatewayapi.ResponsesStreamEvent) error {
		if event.Type == gatewayapi.EventOutputTextDelta {
			tokenWriteAttempted = true
			return writeErr
		}
		delivered = append(delivered, event.Type)
		return nil
	})

	if !errors.Is(err, writeErr) {
		t.Fatalf("stream error = %v, want client write error", err)
	}
	if !tokenWriteAttempted || len(delivered) == 0 || delivered[0] != gatewayapi.EventResponseCreated {
		t.Fatalf("successful prelude=%v token_write_attempted=%v", delivered, tokenWriteAttempted)
	}
	if first.called != 1 || second.called != 0 {
		t.Fatalf("adapter calls first=%d second=%d, want 1/0 after customer frame delivery", first.called, second.called)
	}
	if len(settlement.params) != 0 {
		t.Fatalf("settlement calls = %d, want 0 before a Token is delivered", len(settlement.params))
	}
	if authorizer.releaseCount != 1 {
		t.Fatalf("authorization releases = %d, want 1", authorizer.releaseCount)
	}
	if got := requestLog.gatewayFirstTokens.Load(); got != 0 {
		t.Fatalf("Gateway first-token writes = %d, want 0", got)
	}
	if len(requestLog.deliveryInterrupted) != 1 {
		t.Fatalf("delivery interrupted writes = %v, want one", requestLog.deliveryInterrupted)
	}
}
