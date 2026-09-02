package chatbridge

import (
	"encoding/json"
	"testing"

	chatcompletions "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/chatcompletions"
	responses "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/responses"
)

func rawString(t *testing.T, v string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

// 请求映射：system→instructions、tool 结果→function_call_output、assistant tool_calls→function_call、
// max_completion_tokens→max_output_tokens、store 恒 false。
func TestBuildResponsesBodyMapsChatShapes(t *testing.T) {
	maxCompletion := 256
	req := chatcompletions.ChatRequest{
		Model: "gpt-5.5",
		Messages: []chatcompletions.ChatMessage{
			{Role: "system", Content: rawString(t, "You are helpful.")},
			{Role: "user", Content: rawString(t, "call the tool")},
			{Role: "assistant", ToolCalls: []chatcompletions.ChatToolCall{{
				ID: "call_1", Type: "function",
				Function: chatcompletions.ChatToolCallFunction{Name: "lookup", Arguments: `{"q":"x"}`},
			}}},
			{Role: "tool", ToolCallID: strPtr("call_1"), Content: rawString(t, "result-42")},
		},
		MaxCompletionTokens: &maxCompletion,
		Tools: []chatcompletions.ChatTool{{
			Type: "function",
			Function: chatcompletions.ChatFunctionTool{
				Name: "lookup", Description: "look things up",
				Parameters: json.RawMessage(`{"type":"object"}`),
			},
		}},
		ToolChoice: json.RawMessage(`"auto"`),
	}
	raw, err := buildResponsesBody(req, true)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["instructions"] != "You are helpful." {
		t.Fatalf("instructions = %v", body["instructions"])
	}
	if body["store"] != false || body["stream"] != true {
		t.Fatalf("store/stream = %v/%v", body["store"], body["stream"])
	}
	if body["max_output_tokens"] != float64(256) {
		t.Fatalf("max_output_tokens = %v", body["max_output_tokens"])
	}
	input := body["input"].([]any)
	if len(input) != 3 {
		t.Fatalf("input items = %d, want 3 (user + function_call + function_call_output)", len(input))
	}
	call := input[1].(map[string]any)
	if call["type"] != "function_call" || call["call_id"] != "call_1" || call["name"] != "lookup" {
		t.Fatalf("function_call item = %v", call)
	}
	output := input[2].(map[string]any)
	if output["type"] != "function_call_output" || output["call_id"] != "call_1" || output["output"] != "result-42" {
		t.Fatalf("function_call_output item = %v", output)
	}
	tools := body["tools"].([]any)
	tool := tools[0].(map[string]any)
	if tool["type"] != "function" || tool["name"] != "lookup" {
		t.Fatalf("tool = %v", tool)
	}
	if body["tool_choice"] != "auto" {
		t.Fatalf("tool_choice = %v", body["tool_choice"])
	}
}

// 具名 tool_choice 的形态转换：chat 的嵌套 function → responses 的平铺 name。
func TestToolChoiceNamedFunction(t *testing.T) {
	choice, ok := toolChoiceFromChat(json.RawMessage(`{"type":"function","function":{"name":"lookup"}}`))
	if !ok {
		t.Fatal("named function tool_choice must map")
	}
	doc := choice.(map[string]any)
	if doc["type"] != "function" || doc["name"] != "lookup" {
		t.Fatalf("mapped choice = %v", doc)
	}
}

// 响应映射：output_text 拼接、function_call → tool_calls（finish_reason=tool_calls）、
// Facts 原样透传（settlement 消费 responses 侧事实）。
func TestChatResponseFromResponses(t *testing.T) {
	raw := []byte(`{
		"id": "resp_1", "model": "gpt-5.5", "status": "completed", "created_at": 1788000000,
		"service_tier": "default",
		"output": [
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello "},{"type":"output_text","text":"world"}]},
			{"type":"function_call","call_id":"call_9","name":"lookup","arguments":"{\"q\":1}"}
		]
	}`)
	resp := &responses.Response{Raw: raw, ResponseID: "resp_1", Model: "gpt-5.5"}
	out, err := chatResponseFromResponses(resp)
	if err != nil {
		t.Fatalf("map: %v", err)
	}
	if out.Content != "hello world" {
		t.Fatalf("content = %q", out.Content)
	}
	if len(out.ToolCalls) != 1 || out.ToolCalls[0].ID != "call_9" || out.ToolCalls[0].Function.Name != "lookup" {
		t.Fatalf("tool calls = %+v", out.ToolCalls)
	}
	if out.FinishReason != "tool_calls" {
		t.Fatalf("finish = %q", out.FinishReason)
	}
	if out.Created != 1788000000 || out.ServiceTier == nil || *out.ServiceTier != "default" {
		t.Fatalf("created/tier = %d/%v", out.Created, out.ServiceTier)
	}
}

// 流式翻译的权威首字（ADR-0017）：created/in_progress 不产出 chunk，
// 首个 output_text.delta 才是第一个含内容的 chunk；工具调用增量按 index 稳定对应。
func TestStreamTranslatorAuthoritativeFirstToken(t *testing.T) {
	var chunks []chatcompletions.ChatStreamChunk
	translator := newStreamTranslator("gpt-5.5", func(chunk chatcompletions.ChatStreamChunk) error {
		chunks = append(chunks, chunk)
		return nil
	})

	feed := func(eventType, data string) {
		t.Helper()
		if err := translator.consume(responses.StreamChunk{EventType: eventType, Data: json.RawMessage(data)}); err != nil {
			t.Fatalf("consume %s: %v", eventType, err)
		}
	}
	feed("response.created", `{"type":"response.created","response":{"id":"resp_s1","status":"in_progress"}}`)
	feed("response.in_progress", `{"type":"response.in_progress","response":{"id":"resp_s1"}}`)
	if len(chunks) != 0 {
		t.Fatalf("lifecycle events must not emit chunks (authoritative first token), got %d", len(chunks))
	}
	feed("response.output_text.delta", `{"type":"response.output_text.delta","item_id":"msg_1","delta":"po"}`)
	feed("response.output_text.delta", `{"type":"response.output_text.delta","item_id":"msg_1","delta":"ng"}`)
	feed("response.output_item.added", `{"type":"response.output_item.added","item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"lookup"}}`)
	feed("response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":"{\"q\":"}`)
	feed("response.completed", `{"type":"response.completed","response":{"id":"resp_s1","status":"completed"}}`)

	if len(chunks) != 5 {
		t.Fatalf("chunks = %d, want 5", len(chunks))
	}
	if chunks[0].Content != "po" || chunks[0].Role != "assistant" {
		t.Fatalf("first chunk = %+v", chunks[0])
	}
	if chunks[1].Content != "ng" || chunks[1].Role != "" {
		t.Fatalf("second chunk must not repeat role: %+v", chunks[1])
	}
	var started []map[string]any
	if err := json.Unmarshal(chunks[2].ToolCalls, &started); err != nil || started[0]["id"] != "call_1" {
		t.Fatalf("tool call start chunk = %s", chunks[2].ToolCalls)
	}
	var deltas []map[string]any
	if err := json.Unmarshal(chunks[3].ToolCalls, &deltas); err != nil || deltas[0]["index"] != float64(0) {
		t.Fatalf("tool call delta chunk = %s", chunks[3].ToolCalls)
	}
	if chunks[4].FinishReason == nil || *chunks[4].FinishReason != "tool_calls" {
		t.Fatalf("terminal chunk = %+v", chunks[4])
	}
}

func strPtr(v string) *string { return &v }
