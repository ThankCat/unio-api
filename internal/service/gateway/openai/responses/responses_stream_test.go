package responses

import (
	"encoding/json"
	"testing"

	gatewayapi "github.com/ThankCat/unio-api/internal/app/gatewayapi/openai/responses"
	"github.com/ThankCat/unio-api/internal/core/adapter"
	"github.com/ThankCat/unio-api/internal/core/adapter/openai"
)

// collectEvents 驱动 encoder 并收集发出的命名事件，供断言序列/形状。
func collectEvents(t *testing.T, drive func(enc *streamEncoder) error) []gatewayapi.ResponsesStreamEvent {
	t.Helper()
	var events []gatewayapi.ResponsesStreamEvent
	enc := newStreamEncoder(
		gatewayapi.ResponsesRequest{Model: "unio-deepseek"},
		"resp_fixed",
		1700000000,
		func(ev gatewayapi.ResponsesStreamEvent) error {
			events = append(events, ev)
			return nil
		},
	)
	if err := drive(enc); err != nil {
		t.Fatalf("drive encoder: %v", err)
	}
	return events
}

func eventTypes(events []gatewayapi.ResponsesStreamEvent) []string {
	types := make([]string, len(events))
	for i, ev := range events {
		types[i] = ev.Type
	}
	return types
}

func TestStreamEncoder_ReasoningThenTextHappyPath(t *testing.T) {
	events := collectEvents(t, func(enc *streamEncoder) error {
		if err := enc.Handle(openai.ChatStreamChunk{ReasoningContent: strptr("think ")}); err != nil {
			return err
		}
		if err := enc.Handle(openai.ChatStreamChunk{ReasoningContent: strptr("more")}); err != nil {
			return err
		}
		if err := enc.Handle(openai.ChatStreamChunk{Content: "hello "}); err != nil {
			return err
		}
		if err := enc.Handle(openai.ChatStreamChunk{Content: "world"}); err != nil {
			return err
		}
		return enc.Complete("stop", &adapter.ChatUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15})
	})

	want := []string{
		gatewayapi.EventResponseCreated,
		gatewayapi.EventOutputItemAdded,    // reasoning
		gatewayapi.EventReasoningTextDelta, // "think "
		gatewayapi.EventReasoningTextDelta, // "more"
		gatewayapi.EventOutputItemAdded,    // message
		gatewayapi.EventOutputTextDelta,    // "hello "
		gatewayapi.EventOutputTextDelta,    // "world"
		gatewayapi.EventOutputItemDone,     // reasoning (index 0)
		gatewayapi.EventOutputItemDone,     // message (index 1)
		gatewayapi.EventResponseCompleted,
	}
	if got := eventTypes(events); !equalStrings(got, want) {
		t.Fatalf("event sequence mismatch:\n got=%v\nwant=%v", got, want)
	}

	// sequence_number 单调从 0 递增。
	for i, ev := range events {
		if ev.SequenceNumber != int64(i) {
			t.Fatalf("event[%d] sequence_number=%d, want %d", i, ev.SequenceNumber, i)
		}
	}

	created := events[0]
	if created.Response == nil || created.Response.Status != "in_progress" || len(created.Response.Output) != 0 {
		t.Fatalf("response.created snapshot wrong: %+v", created.Response)
	}
	if created.Response.ID != "resp_fixed" || created.Response.Model != "unio-deepseek" || created.Response.CreatedAt != 1700000000 {
		t.Fatalf("response.created identity wrong: %+v", created.Response)
	}

	// reasoning item.added 索引 0、message item.added 索引 1。
	if got := derefInt(events[1].OutputIndex); got != 0 {
		t.Fatalf("reasoning output_index=%d, want 0", got)
	}
	if got := derefInt(events[4].OutputIndex); got != 1 {
		t.Fatalf("message output_index=%d, want 1", got)
	}

	// reasoning output_item.done 携带全量 reasoning_text。
	reasoningDone := events[7]
	if reasoningDone.Item == nil || reasoningDone.Item.Type != "reasoning" {
		t.Fatalf("expected reasoning item.done, got %+v", reasoningDone.Item)
	}
	if len(reasoningDone.Item.Content) != 1 || reasoningDone.Item.Content[0].Type != "reasoning_text" || reasoningDone.Item.Content[0].Text != "think more" {
		t.Fatalf("reasoning done content wrong: %+v", reasoningDone.Item.Content)
	}

	// message output_item.done 携带全量 output_text 且 status=completed。
	messageDone := events[8]
	if messageDone.Item == nil || messageDone.Item.Type != "message" || messageDone.Item.Status != "completed" {
		t.Fatalf("expected message item.done completed, got %+v", messageDone.Item)
	}
	if len(messageDone.Item.Content) != 1 || messageDone.Item.Content[0].Text != "hello world" {
		t.Fatalf("message done content wrong: %+v", messageDone.Item.Content)
	}

	// response.completed 携带全量 output（顺序 reasoning→message）与 usage。
	completed := events[9]
	if completed.Response == nil || completed.Response.Status != "completed" {
		t.Fatalf("expected response.completed, got %+v", completed.Response)
	}
	if len(completed.Response.Output) != 2 || completed.Response.Output[0].Type != "reasoning" || completed.Response.Output[1].Type != "message" {
		t.Fatalf("completed output order wrong: %+v", completed.Response.Output)
	}
	if completed.Response.Usage == nil || completed.Response.Usage.InputTokens != 10 || completed.Response.Usage.OutputTokens != 5 {
		t.Fatalf("completed usage wrong: %+v", completed.Response.Usage)
	}
}

func TestStreamEncoder_ToolCallAccumulation(t *testing.T) {
	first, _ := json.Marshal([]map[string]any{{
		"index": 0, "id": "call_1", "type": "function",
		"function": map[string]any{"name": "exec_command", "arguments": `{"cmd":`},
	}})
	second, _ := json.Marshal([]map[string]any{{
		"index":    0,
		"function": map[string]any{"arguments": `"ls"}`},
	}})

	events := collectEvents(t, func(enc *streamEncoder) error {
		if err := enc.Handle(openai.ChatStreamChunk{ToolCalls: first}); err != nil {
			return err
		}
		if err := enc.Handle(openai.ChatStreamChunk{ToolCalls: second}); err != nil {
			return err
		}
		return enc.Complete("tool_calls", &adapter.ChatUsage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2})
	})

	want := []string{
		gatewayapi.EventResponseCreated,
		gatewayapi.EventOutputItemAdded,       // function_call
		gatewayapi.EventFunctionCallArgsDelta, // {"cmd":
		gatewayapi.EventFunctionCallArgsDelta, // "ls"}
		gatewayapi.EventOutputItemDone,        // function_call
		gatewayapi.EventResponseCompleted,
	}
	if got := eventTypes(events); !equalStrings(got, want) {
		t.Fatalf("tool event sequence mismatch:\n got=%v\nwant=%v", got, want)
	}

	done := events[4]
	if done.Item == nil || done.Item.Type != "function_call" {
		t.Fatalf("expected function_call done, got %+v", done.Item)
	}
	if done.Item.CallID != "call_1" || done.Item.Name != "exec_command" || done.Item.Arguments != `{"cmd":"ls"}` {
		t.Fatalf("function_call done wrong: call_id=%q name=%q args=%q", done.Item.CallID, done.Item.Name, done.Item.Arguments)
	}
	if done.Item.Status != "completed" {
		t.Fatalf("function_call done status=%q, want completed", done.Item.Status)
	}
}

func TestStreamEncoder_MCPNamespaceSplitInStream(t *testing.T) {
	raw, _ := json.Marshal([]map[string]any{{
		"index": 0, "id": "call_x", "type": "function",
		"function": map[string]any{"name": "mcp__github__search", "arguments": `{}`},
	}})

	events := collectEvents(t, func(enc *streamEncoder) error {
		if err := enc.Handle(openai.ChatStreamChunk{ToolCalls: raw}); err != nil {
			return err
		}
		return enc.Complete("tool_calls", &adapter.ChatUsage{TotalTokens: 1})
	})

	added := events[1]
	if added.Item == nil || added.Item.Name != "search" {
		t.Fatalf("expected item.added name=search, got %+v", added.Item)
	}
	done := events[len(events)-2]
	if done.Item == nil || done.Item.Name != "search" || done.Item.Namespace != "mcp__github__" {
		t.Fatalf("expected namespace split on done, got name=%q namespace=%q", done.Item.Name, done.Item.Namespace)
	}
}

func TestStreamEncoder_EmptyStreamStillEmitsCreatedAndCompleted(t *testing.T) {
	events := collectEvents(t, func(enc *streamEncoder) error {
		return enc.Complete("stop", &adapter.ChatUsage{TotalTokens: 0})
	})
	want := []string{gatewayapi.EventResponseCreated, gatewayapi.EventResponseCompleted}
	if got := eventTypes(events); !equalStrings(got, want) {
		t.Fatalf("empty stream sequence=%v, want %v", got, want)
	}
	if events[1].Response == nil || len(events[1].Response.Output) != 0 {
		t.Fatalf("empty completed output should be empty, got %+v", events[1].Response)
	}
}

func TestStreamEncoder_LengthFinishYieldsIncomplete(t *testing.T) {
	events := collectEvents(t, func(enc *streamEncoder) error {
		if err := enc.Handle(openai.ChatStreamChunk{Content: "partial"}); err != nil {
			return err
		}
		return enc.Complete("length", &adapter.ChatUsage{TotalTokens: 3})
	})

	last := events[len(events)-1]
	if last.Type != gatewayapi.EventResponseIncomplete {
		t.Fatalf("expected response.incomplete terminal event, got %q", last.Type)
	}
	if last.Response == nil || last.Response.Status != "incomplete" {
		t.Fatalf("expected status incomplete, got %+v", last.Response)
	}
	if last.Response.IncompleteDetails == nil || last.Response.IncompleteDetails.Reason != "max_output_tokens" {
		t.Fatalf("expected incomplete reason max_output_tokens, got %+v", last.Response.IncompleteDetails)
	}
}

// TestStreamEncoder_ReasoningCarrierWhenRequested 验证流式 reasoning output_item.done 在客户请求
// reasoning.encrypted_content 时携带可解码的回放载体（U1，Codex 走流式以 done 事件为权威）。
func TestStreamEncoder_ReasoningCarrierWhenRequested(t *testing.T) {
	var events []gatewayapi.ResponsesStreamEvent
	enc := newStreamEncoder(
		gatewayapi.ResponsesRequest{Model: "m", Include: []string{"reasoning.encrypted_content"}},
		"resp_x", 0,
		func(ev gatewayapi.ResponsesStreamEvent) error { events = append(events, ev); return nil },
	)
	if err := enc.Handle(openai.ChatStreamChunk{ReasoningContent: strptr("alpha")}); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if err := enc.Handle(openai.ChatStreamChunk{ReasoningContent: strptr(" beta")}); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if err := enc.Complete("tool_calls", nil); err != nil {
		t.Fatalf("complete: %v", err)
	}

	var reasoningDone *gatewayapi.ResponseOutputItem
	for i := range events {
		if events[i].Type == gatewayapi.EventOutputItemDone && events[i].Item != nil && events[i].Item.Type == "reasoning" {
			reasoningDone = events[i].Item
		}
	}
	if reasoningDone == nil {
		t.Fatal("expected reasoning output_item.done event")
	}
	if reasoningDone.EncryptedContent == nil {
		t.Fatal("expected encrypted_content carrier on streamed reasoning done")
	}
	decoded, ok := decodeReasoningCarrier(*reasoningDone.EncryptedContent)
	if !ok || decoded != "alpha beta" {
		t.Fatalf("carrier decode failed: ok=%v decoded=%q", ok, decoded)
	}
}

func TestStreamEncoder_StartedReflectsFirstEmit(t *testing.T) {
	enc := newStreamEncoder(gatewayapi.ResponsesRequest{Model: "m"}, "resp_1", 0, func(gatewayapi.ResponsesStreamEvent) error { return nil })
	if enc.Started() {
		t.Fatal("encoder should not be started before any chunk")
	}
	if err := enc.Handle(openai.ChatStreamChunk{Content: "x"}); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if !enc.Started() {
		t.Fatal("encoder should be started after first content chunk")
	}
}

// TestStreamEncoder_RefusalSurfacedInMessageItem 验证上游 refusal 增量被累积进 message item，
// 并随 output_item.done / response.completed 以 refusal content part 下发（与非流式映射一致，不丢内容过滤信息）。
func TestStreamEncoder_RefusalAfterText(t *testing.T) {
	events := collectEvents(t, func(enc *streamEncoder) error {
		if err := enc.Handle(openai.ChatStreamChunk{Content: "partial "}); err != nil {
			return err
		}
		if err := enc.Handle(openai.ChatStreamChunk{Refusal: strptr("I cannot ")}); err != nil {
			return err
		}
		if err := enc.Handle(openai.ChatStreamChunk{Refusal: strptr("help with that.")}); err != nil {
			return err
		}
		return enc.Complete("content_filter", &adapter.ChatUsage{TotalTokens: 4})
	})

	var messageDone *gatewayapi.ResponseOutputItem
	for i := range events {
		if events[i].Type == gatewayapi.EventOutputItemDone && events[i].Item != nil && events[i].Item.Type == "message" {
			messageDone = events[i].Item
		}
	}
	if messageDone == nil {
		t.Fatal("expected message output_item.done event")
	}
	var gotText, gotRefusal string
	for _, part := range messageDone.Content {
		switch part.Type {
		case "output_text":
			gotText = part.Text
		case "refusal":
			gotRefusal = part.Refusal
		}
	}
	if gotText != "partial " {
		t.Fatalf("message output_text = %q, want %q", gotText, "partial ")
	}
	if gotRefusal != "I cannot help with that." {
		t.Fatalf("message refusal = %q, want %q", gotRefusal, "I cannot help with that.")
	}
}

// TestStreamEncoder_RefusalOnlyCreatesMessageItem 验证仅有 refusal（无文本）时仍创建 message item 并下发 refusal。
func TestStreamEncoder_RefusalOnly(t *testing.T) {
	events := collectEvents(t, func(enc *streamEncoder) error {
		if err := enc.Handle(openai.ChatStreamChunk{Refusal: strptr("refused")}); err != nil {
			return err
		}
		return enc.Complete("content_filter", &adapter.ChatUsage{TotalTokens: 1})
	})

	var messageDone *gatewayapi.ResponseOutputItem
	for i := range events {
		if events[i].Type == gatewayapi.EventOutputItemDone && events[i].Item != nil && events[i].Item.Type == "message" {
			messageDone = events[i].Item
		}
	}
	if messageDone == nil {
		t.Fatal("expected message output_item.done event for refusal-only stream")
	}
	if len(messageDone.Content) != 1 || messageDone.Content[0].Type != "refusal" || messageDone.Content[0].Refusal != "refused" {
		t.Fatalf("refusal-only content wrong: %+v", messageDone.Content)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func derefInt(p *int) int {
	if p == nil {
		return -1
	}
	return *p
}
