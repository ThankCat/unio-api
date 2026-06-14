package chatcompletions

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

// TestBuildChatCompletionRequestBodyFaithfulOfficialFields 验证 base 是忠实官方基线（路线 C 去方言化）：
// max_tokens 与 max_completion_tokens 各自独立原样输出、互不塌缩；developer role 原样透传不映射为 system。
func TestBuildChatCompletionRequestBodyFaithfulOfficialFields(t *testing.T) {
	maxTokens := 10
	maxCompletionTokens := 20
	body, err := buildChatCompletionRequestBody(ChatRequest{
		Model: "gpt-5.5",
		Messages: []ChatMessage{
			{Role: "developer", Content: jsonContent("be terse")},
			{Role: "user", Content: jsonContent("hi")},
		},
		MaxTokens:           &maxTokens,
		MaxCompletionTokens: &maxCompletionTokens,
	}, false)
	if err != nil {
		t.Fatalf("build body: %v", err)
	}

	var raw struct {
		MaxTokens           *int `json:"max_tokens"`
		MaxCompletionTokens *int `json:"max_completion_tokens"`
		Messages            []struct {
			Role string `json:"role"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if raw.MaxTokens == nil || *raw.MaxTokens != 10 {
		t.Fatalf("wire max_tokens = %v, want 10 (faithful, no collapse)", raw.MaxTokens)
	}
	if raw.MaxCompletionTokens == nil || *raw.MaxCompletionTokens != 20 {
		t.Fatalf("wire max_completion_tokens = %v, want 20 (faithful, no collapse)", raw.MaxCompletionTokens)
	}
	if raw.Messages[0].Role != "developer" {
		t.Fatalf("wire messages[0].role = %q, want developer (faithful, no system collapse)", raw.Messages[0].Role)
	}
}

// TestBuildChatCompletionRequestBodyOmitsAbsentMaxTokenFields 验证两个 max token 字段缺省时都不进 wire。
func TestBuildChatCompletionRequestBodyOmitsAbsentMaxTokenFields(t *testing.T) {
	body, err := buildChatCompletionRequestBody(ChatRequest{
		Model:    "gpt-5.5",
		Messages: []ChatMessage{{Role: "user", Content: jsonContent("hi")}},
	}, false)
	if err != nil {
		t.Fatalf("build body: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := raw["max_tokens"]; ok {
		t.Fatal("expected max_tokens omitted when absent")
	}
	if _, ok := raw["max_completion_tokens"]; ok {
		t.Fatal("expected max_completion_tokens omitted when absent")
	}
}

func strPtr(s string) *string { return &s }
