package responses

import (
	"errors"
	"testing"

	gatewayapi "github.com/ThankCat/unio-api/internal/app/gatewayapi/openai/responses"
	chatcompletionsadapter "github.com/ThankCat/unio-api/internal/core/adapter/openai/chatcompletions"
	"github.com/ThankCat/unio-api/internal/core/routing"
)

func TestCompactHistory_HappyPath(t *testing.T) {
	chatAdapter := &fakeChatAdapter{resp: okChatResponse()}
	registry := &fakeRegistry{
		adapters:   map[string]chatcompletionsadapter.ChatAdapter{"deepseek": chatAdapter},
		tokenizers: map[string]chatcompletionsadapter.ChatInputTokenizer{"deepseek": fakeTokenizer{}},
	}
	router := &fakeRouter{plan: routing.ChatRoutePlan{Candidates: []routing.ChatRouteCandidate{candidate("deepseek", 1, "deepseek-v4-flash")}}}
	settlement := &fakeSettlement{}
	requestLog := newFakeRequestLog()

	svc := newServiceForTest(router, registry, settlement, &fakeAuthorizer{}, requestLog)

	resp, err := svc.CompactHistory(ctxWithPrincipal(), instructionsRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// compact 是一次可计费上游调用：走完整 settlement，operation 仍为 responses。
	if len(settlement.params) != 1 {
		t.Fatalf("expected compact to settle once, got %d", len(settlement.params))
	}
	if len(requestLog.createRequests) != 1 || requestLog.createRequests[0].Operation != "responses" {
		t.Fatalf("expected one responses request record, got %+v", requestLog.createRequests)
	}

	// 摘要包成单条 assistant message 的 output_text。
	if len(resp.Output) != 1 {
		t.Fatalf("expected single compaction output item, got %+v", resp.Output)
	}
	item := resp.Output[0]
	if item.Type != "message" || item.Role != "assistant" || item.Status != "completed" {
		t.Fatalf("unexpected compaction item: %+v", item)
	}
	if len(item.Content) != 1 || item.Content[0].Type != "output_text" || item.Content[0].Text != "hi there" {
		t.Fatalf("unexpected compaction content: %+v", item.Content)
	}
}

func TestCompactHistory_InjectsDefaultInstruction(t *testing.T) {
	chatAdapter := &fakeChatAdapter{resp: okChatResponse()}
	registry := &fakeRegistry{
		adapters:   map[string]chatcompletionsadapter.ChatAdapter{"deepseek": chatAdapter},
		tokenizers: map[string]chatcompletionsadapter.ChatInputTokenizer{"deepseek": fakeTokenizer{}},
	}
	router := &fakeRouter{plan: routing.ChatRoutePlan{Candidates: []routing.ChatRouteCandidate{candidate("deepseek", 1, "deepseek-v4-flash")}}}

	svc := newServiceForTest(router, registry, &fakeSettlement{}, &fakeAuthorizer{}, newFakeRequestLog())

	text := "earlier conversation to compact"
	req := gatewayapi.ResponsesRequest{Model: "unio-deepseek", Input: gatewayapi.ResponsesInput{Text: &text}}

	if _, err := svc.CompactHistory(ctxWithPrincipal(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 缺 instructions 时注入默认压缩指令为首条 system，避免模型续写而非压缩。
	if len(chatAdapter.req.Messages) == 0 || chatAdapter.req.Messages[0].Role != "system" {
		t.Fatalf("expected injected system compaction instruction, got %+v", chatAdapter.req.Messages)
	}
}

func TestCountInputTokens_LocalEstimate(t *testing.T) {
	chatAdapter := &fakeChatAdapter{resp: okChatResponse()}
	registry := &fakeRegistry{
		adapters:   map[string]chatcompletionsadapter.ChatAdapter{"deepseek": chatAdapter},
		tokenizers: map[string]chatcompletionsadapter.ChatInputTokenizer{"deepseek": fakeTokenizer{}},
	}
	router := &fakeRouter{plan: routing.ChatRoutePlan{Candidates: []routing.ChatRouteCandidate{candidate("deepseek", 1, "deepseek-v4-flash")}}}
	settlement := &fakeSettlement{}
	requestLog := newFakeRequestLog()

	svc := newServiceForTest(router, registry, settlement, &fakeAuthorizer{}, requestLog)

	resp, err := svc.CountInputTokens(ctxWithPrincipal(), instructionsRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.InputTokens != 16 || resp.Object != "response.input_tokens" {
		t.Fatalf("unexpected input token count: %+v", resp)
	}

	// 本地估算：不调上游、不计费、不写请求审计。
	if chatAdapter.req.Model != "" {
		t.Fatalf("expected no upstream call, adapter saw %+v", chatAdapter.req)
	}
	if len(settlement.params) != 0 {
		t.Fatalf("expected no settlement, got %d", len(settlement.params))
	}
	if len(requestLog.createRequests) != 0 {
		t.Fatalf("expected no request record for input_tokens, got %d", len(requestLog.createRequests))
	}
}

func TestCountInputTokens_RoutingError(t *testing.T) {
	registry := &fakeRegistry{tokenizers: map[string]chatcompletionsadapter.ChatInputTokenizer{"deepseek": fakeTokenizer{}}}
	router := &fakeRouter{err: errors.New("model not found")}

	svc := newServiceForTest(router, registry, &fakeSettlement{}, &fakeAuthorizer{}, newFakeRequestLog())

	if _, err := svc.CountInputTokens(ctxWithPrincipal(), instructionsRequest()); err == nil {
		t.Fatal("expected routing error to propagate")
	}
}
