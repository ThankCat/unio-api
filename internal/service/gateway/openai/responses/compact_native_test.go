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
	"github.com/ThankCat/unio-api/internal/core/routing"
	"github.com/ThankCat/unio-api/internal/platform/failure"
)

// fakeCompactAdapter 是原生 /responses/compact 直传 adapter 的测试替身：记录上送请求体并返回预置原文或错误。
type fakeCompactAdapter struct {
	called  int
	gotBody json.RawMessage
	resp    *responsesadapter.Response
	err     error
}

func (a *fakeCompactAdapter) CompactResponse(_ context.Context, _ channel.Runtime, req responsesadapter.Request) (*responsesadapter.Response, error) {
	a.called++
	a.gotBody = req.Body
	if a.err != nil {
		return nil, a.err
	}
	return a.resp, nil
}

// nativeCompactResponse 构造一个含 compaction item + encrypted_content + usage 的上游压缩响应原文。
func nativeCompactResponse() *responsesadapter.Response {
	raw := json.RawMessage(`{"id":"resp_compact","object":"response","model":"gpt-5.5-upstream","output":[{"type":"compaction","encrypted_content":"enc-blob"}],"usage":{"input_tokens":40,"output_tokens":6,"total_tokens":46}}`)
	meta := adapter.UpstreamMetadata{StatusCode: 200, RequestID: "req-compact"}
	return &responsesadapter.Response{
		Raw:        raw,
		ResponseID: "resp_compact",
		Model:      "gpt-5.5-upstream",
		Usage:      adapter.ChatUsage{PromptTokens: 40, CompletionTokens: 6, TotalTokens: 46},
		Upstream:   meta,
		Facts: adapter.ResponseFacts{
			UpstreamProtocol:    "openai",
			UpstreamResponseID:  "resp_compact",
			UpstreamModel:       "gpt-5.5-upstream",
			Finish:              adapter.FinishFacts{Class: adapter.FinishStop, RawReason: "stop"},
			UsageMappingVersion: "chatcompletionsadapter.responses.v1",
			Metadata:            meta,
		},
	}
}

func compactNativeRequest() gatewayapi.ResponsesRequest {
	instructions := "compact please"
	text := "long history to compact"
	return gatewayapi.ResponsesRequest{
		Model:        "gpt-5.5",
		Instructions: &instructions,
		Input:        gatewayapi.ResponsesInput{Text: &text},
	}
}

// TestCompactHistory_NativePassthrough 验证：候选 adapter 注册了原生 compact 能力时走 NativeCompact——
// 透传上游 /responses/compact，响应原文返回（仅顶层 model 回显改写为客户请求名），chat 摘要零触达，
// settlement 落原生 facts。
func TestCompactHistory_NativePassthrough(t *testing.T) {
	compactAdapter := &fakeCompactAdapter{resp: nativeCompactResponse()}
	// chat 适配器同时存在但不应被触达（原生 compact 命中，不落 synthetic）。
	chatAdapter := &fakeChatAdapter{resp: okChatResponse()}
	registry := &fakeRegistry{
		adapters:                 map[string]chatcompletionsadapter.ChatAdapter{"openai": chatAdapter},
		tokenizers:               map[string]chatcompletionsadapter.ChatInputTokenizer{"openai": fakeTokenizer{}},
		responsesCompactAdapters: map[string]responsesadapter.ResponsesCompactAdapter{"openai": compactAdapter},
	}
	router := &fakeRouter{plan: routing.ChatRoutePlan{Candidates: []routing.ChatRouteCandidate{candidate("openai", 1, "gpt-5.5-upstream")}}}
	settlement := &fakeSettlement{}

	svc := newServiceForTest(router, registry, settlement, &fakeAuthorizer{}, newFakeRequestLog())

	resp, err := svc.CompactHistory(ctxWithPrincipal(), compactNativeRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 原生 compact 命中一次，synthetic chat 完全未触达。
	if compactAdapter.called != 1 {
		t.Fatalf("expected native compact called once, got %d", compactAdapter.called)
	}
	if chatAdapter.req.Model != "" {
		t.Fatalf("synthetic chat must not be invoked for native compact, got model %q", chatAdapter.req.Model)
	}

	// 上送上游请求体 model 改写为 upstream model。
	var upBody map[string]json.RawMessage
	if err := json.Unmarshal(compactAdapter.gotBody, &upBody); err != nil {
		t.Fatalf("decode upstream body: %v", err)
	}
	if string(upBody["model"]) != `"gpt-5.5-upstream"` {
		t.Fatalf("upstream model = %s, want \"gpt-5.5-upstream\"", upBody["model"])
	}

	// 客户响应：上游原文透传，仅顶层 model 回显改写为客户请求名；compaction item 原样保留。
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
	output, ok := got["output"].([]any)
	if !ok || len(output) != 1 {
		t.Fatalf("expected single passthrough output item, got %v", got["output"])
	}
	item, _ := output[0].(map[string]any)
	if item["type"] != "compaction" || item["encrypted_content"] != "enc-blob" {
		t.Fatalf("native compaction item lost in passthrough: %v", item)
	}

	// settlement 落原生 ResponseFacts。
	if len(settlement.params) != 1 {
		t.Fatalf("expected 1 settlement, got %d", len(settlement.params))
	}
}

// TestCompactHistory_NativeFallbackToSynthetic 验证 Q2：原生 compact 命中「上游不支持」时自动回落 Synthetic——
// 同一候选改走 chat 摘要，输出包成单条 assistant message，仍走一次 settlement（不中断 Codex）。
func TestCompactHistory_NativeFallbackToSynthetic(t *testing.T) {
	compactAdapter := &fakeCompactAdapter{err: failure.Wrap(
		failure.CodeAdapterRequestUnsupported,
		responsesadapter.ErrCompactUnsupported,
		failure.WithMessage("simulated upstream 404"),
	)}
	chatAdapter := &fakeChatAdapter{resp: okChatResponse()}
	registry := &fakeRegistry{
		adapters:                 map[string]chatcompletionsadapter.ChatAdapter{"openai": chatAdapter},
		tokenizers:               map[string]chatcompletionsadapter.ChatInputTokenizer{"openai": fakeTokenizer{}},
		responsesCompactAdapters: map[string]responsesadapter.ResponsesCompactAdapter{"openai": compactAdapter},
	}
	router := &fakeRouter{plan: routing.ChatRoutePlan{Candidates: []routing.ChatRouteCandidate{candidate("openai", 1, "gpt-5.5-upstream")}}}
	settlement := &fakeSettlement{}

	svc := newServiceForTest(router, registry, settlement, &fakeAuthorizer{}, newFakeRequestLog())

	resp, err := svc.CompactHistory(ctxWithPrincipal(), compactNativeRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 原生先尝试一次（失败），随后回落 synthetic chat（用 upstream model）。
	if compactAdapter.called != 1 {
		t.Fatalf("expected native compact attempted once, got %d", compactAdapter.called)
	}
	if chatAdapter.req.Model != "gpt-5.5-upstream" {
		t.Fatalf("expected synthetic fallback chat with upstream model, got %q", chatAdapter.req.Model)
	}

	// synthetic 输出：单条 assistant message 承载摘要。
	if len(resp.Output) != 1 || resp.Output[0].Type != "message" ||
		len(resp.Output[0].Content) != 1 || resp.Output[0].Content[0].Text != "hi there" {
		t.Fatalf("unexpected synthetic compaction output: %+v", resp.Output)
	}
	if len(settlement.params) != 1 {
		t.Fatalf("expected 1 settlement, got %d", len(settlement.params))
	}
}

// TestCompactHistory_NativeMissingUsageRecordsRiskExposure 验证 P0-3：原生 compact 返回 2xx 但缺可计费 usage
// 时，绝不静默回落 synthetic 白嫖；而是不再调用 chat、记 risk_exposure（账务异常释放）并向客户返回错误。
func TestCompactHistory_NativeMissingUsageRecordsRiskExposure(t *testing.T) {
	missingUsageErr := adapter.NewUpstreamError(
		adapter.UpstreamErrorServer,
		adapter.UpstreamMetadata{StatusCode: 200, RequestID: "req-missing-usage"},
		failure.Wrap(
			failure.CodeAdapterInvalidResponse,
			responsesadapter.ErrCompactMissingUsage,
			failure.WithMessage("simulated 200 without usage"),
		),
	)
	compactAdapter := &fakeCompactAdapter{err: missingUsageErr}
	chatAdapter := &fakeChatAdapter{resp: okChatResponse()}
	registry := &fakeRegistry{
		adapters:                 map[string]chatcompletionsadapter.ChatAdapter{"openai": chatAdapter},
		tokenizers:               map[string]chatcompletionsadapter.ChatInputTokenizer{"openai": fakeTokenizer{}},
		responsesCompactAdapters: map[string]responsesadapter.ResponsesCompactAdapter{"openai": compactAdapter},
	}
	router := &fakeRouter{plan: routing.ChatRoutePlan{Candidates: []routing.ChatRouteCandidate{candidate("openai", 1, "gpt-5.5-upstream")}}}
	authorizer := &fakeAuthorizer{}
	settlement := &fakeSettlement{}

	svc := newServiceForTest(router, registry, settlement, authorizer, newFakeRequestLog())
	// 即便回落开关打开，缺 usage 也不得回落（开关只管真 404/405）。
	svc.compactNativeFallback = true

	_, err := svc.CompactHistory(ctxWithPrincipal(), compactNativeRequest())
	if err == nil {
		t.Fatal("expected error when native compact returns 2xx without usage")
	}

	// 原生尝试一次；synthetic chat 绝不触达（避免双调上游、只收一次费白嫖）。
	if compactAdapter.called != 1 {
		t.Fatalf("expected native compact attempted once, got %d", compactAdapter.called)
	}
	if chatAdapter.req.Model != "" {
		t.Fatalf("synthetic must not run on missing-usage (no freeloading), got model %q", chatAdapter.req.Model)
	}

	// 不结算（无可靠 usage 不向用户扣费）。
	if len(settlement.params) != 0 {
		t.Fatalf("expected no settlement on missing-usage, got %d", len(settlement.params))
	}

	// 记一条 risk_exposure（账务异常释放），保留「平台可能承担成本」审计事实。
	if len(authorizer.billingExceptions) != 1 {
		t.Fatalf("expected exactly one billing-exception (risk_exposure) release, got %d", len(authorizer.billingExceptions))
	}
	if authorizer.billingExceptions[0].ReasonCode != "responses_compact_missing_usage" {
		t.Fatalf("unexpected risk_exposure reason code: %q", authorizer.billingExceptions[0].ReasonCode)
	}
	// 普通释放不应发生（走的是账务异常释放）。
	if authorizer.releaseCount != 0 {
		t.Fatalf("expected no plain release on missing-usage, got %d", authorizer.releaseCount)
	}
}

// TestCompactHistory_NativeFallbackDisabled 验证：关闭回落开关时，原生「不支持」错误直接上抛为请求失败，
// 不静默回落 synthetic（运营可显式关闭回落）。
func TestCompactHistory_NativeFallbackDisabled(t *testing.T) {
	compactAdapter := &fakeCompactAdapter{err: failure.Wrap(
		failure.CodeAdapterRequestUnsupported,
		responsesadapter.ErrCompactUnsupported,
		failure.WithMessage("simulated upstream 404"),
	)}
	chatAdapter := &fakeChatAdapter{resp: okChatResponse()}
	registry := &fakeRegistry{
		adapters:                 map[string]chatcompletionsadapter.ChatAdapter{"openai": chatAdapter},
		tokenizers:               map[string]chatcompletionsadapter.ChatInputTokenizer{"openai": fakeTokenizer{}},
		responsesCompactAdapters: map[string]responsesadapter.ResponsesCompactAdapter{"openai": compactAdapter},
	}
	router := &fakeRouter{plan: routing.ChatRoutePlan{Candidates: []routing.ChatRouteCandidate{candidate("openai", 1, "gpt-5.5-upstream")}}}

	svc := newServiceForTest(router, registry, &fakeSettlement{}, &fakeAuthorizer{}, newFakeRequestLog())
	svc.compactNativeFallback = false

	if _, err := svc.CompactHistory(ctxWithPrincipal(), compactNativeRequest()); err == nil {
		t.Fatal("expected error when native compact unsupported and fallback disabled")
	}
	if chatAdapter.req.Model != "" {
		t.Fatalf("synthetic must not run when fallback disabled, got model %q", chatAdapter.req.Model)
	}
}
