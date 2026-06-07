package openai

import (
	"encoding/json"
	"testing"
)

func TestBuildChatCompletionRequestBodyMergesExtensions(t *testing.T) {
	body, err := buildChatCompletionRequestBody(ChatRequest{
		Model: "deepseek-v4-flash",
		Messages: []ChatMessage{
			{Role: "user", Content: jsonContent("hi")},
		},
		Extensions: map[string]json.RawMessage{
			"thinking": json.RawMessage(`{"type":"enabled"}`),
		},
	}, false)
	if err != nil {
		t.Fatalf("build body: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if _, ok := raw["thinking"]; !ok {
		t.Fatal("expected thinking extension merged into wire body")
	}
}

func TestBuildChatCompletionRequestBodyForwardsToolsAndReasoningHistory(t *testing.T) {
	reasoning := "prior-thought"
	body, err := buildChatCompletionRequestBody(ChatRequest{
		Model: "deepseek-v4-flash",
		Messages: []ChatMessage{
			{Role: "user", Content: jsonContent("hi")},
			{
				Role:             "assistant",
				Content:          jsonContent(""),
				ReasoningContent: &reasoning,
				ToolCalls: []ChatToolCall{{
					ID:   "call_1",
					Type: "function",
					Function: ChatToolCallFunction{
						Name:      "get_weather",
						Arguments: "{}",
					},
				}},
			},
			{Role: "tool", ToolCallID: strPtr("call_1"), Content: jsonContent(`{"temp":20}`)},
		},
		Tools: []ChatTool{{
			Type: "function",
			Function: ChatFunctionTool{
				Name:       "get_weather",
				Parameters: json.RawMessage(`{"type":"object"}`),
			},
		}},
		ToolChoice: json.RawMessage(`"auto"`),
		ResponseFormat: &ChatResponseFormat{
			Type: "json_object",
		},
	}, false)
	if err != nil {
		t.Fatalf("build body: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if _, ok := raw["tools"]; !ok {
		t.Fatal("expected tools in wire body")
	}
	if _, ok := raw["tool_choice"]; !ok {
		t.Fatal("expected tool_choice in wire body")
	}
	if _, ok := raw["response_format"]; !ok {
		t.Fatal("expected response_format in wire body")
	}

	var messages []map[string]json.RawMessage
	if err := json.Unmarshal(raw["messages"], &messages); err != nil {
		t.Fatalf("unmarshal messages: %v", err)
	}
	if len(messages) != 3 {
		t.Fatalf("got %d messages, want 3", len(messages))
	}
	if _, ok := messages[1]["reasoning_content"]; !ok {
		t.Fatal("expected assistant reasoning_content in wire messages")
	}
	if _, ok := messages[1]["tool_calls"]; !ok {
		t.Fatal("expected assistant tool_calls in wire messages")
	}
}

func strPtr(s string) *string { return &s }
