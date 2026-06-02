package messages

import (
	"encoding/json"
	"testing"
)

func strptr(s string) *string { return &s }

func TestMessageResponseMarshalsNativeAnthropicShape(t *testing.T) {
	resp := MessageResponse{
		ID:    "msg_1",
		Type:  "message",
		Role:  "assistant",
		Model: "claude-sonnet-4",
		Content: []json.RawMessage{
			json.RawMessage(`{"type":"text","text":"hi"}`),
		},
		StopReason: strptr("end_turn"),
		Usage:      MessageUsage{InputTokens: 5, OutputTokens: 3},
	}

	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got map[string]json.RawMessage
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if string(got["type"]) != `"message"` || string(got["role"]) != `"assistant"` {
		t.Fatalf("type/role = %s/%s", got["type"], got["role"])
	}
	if string(got["stop_reason"]) != `"end_turn"` {
		t.Fatalf("stop_reason = %s", got["stop_reason"])
	}
	// nil StopSequence 必须输出 null（Anthropic 语义），不能省略。
	if string(got["stop_sequence"]) != "null" {
		t.Fatalf("stop_sequence = %s, want null", got["stop_sequence"])
	}

	var usage map[string]json.RawMessage
	if err := json.Unmarshal(got["usage"], &usage); err != nil {
		t.Fatalf("unmarshal usage: %v", err)
	}
	if string(usage["input_tokens"]) != "5" || string(usage["output_tokens"]) != "3" {
		t.Fatalf("usage tokens = %s/%s", usage["input_tokens"], usage["output_tokens"])
	}
	// 未设置的 cache / server tool 维度应省略，避免伪造 0。
	if _, ok := usage["cache_read_input_tokens"]; ok {
		t.Fatal("expected unset cache_read_input_tokens to be omitted")
	}
}

func TestMessageUsageIncludesSetCacheAndServerToolDimensions(t *testing.T) {
	five := 5
	one := 1
	usage := MessageUsage{
		InputTokens:          10,
		CacheReadInputTokens: &five,
		CacheCreation: &CacheCreation{
			Ephemeral5mInputTokens: &five,
		},
		OutputTokens:        7,
		OutputTokensDetails: &OutputTokensDetails{ThinkingTokens: &one},
		ServerToolUse:       &ServerToolUse{WebSearchRequests: &one},
	}

	raw, err := json.Marshal(usage)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got map[string]json.RawMessage
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if string(got["cache_read_input_tokens"]) != "5" {
		t.Fatalf("cache_read = %s", got["cache_read_input_tokens"])
	}
	if _, ok := got["cache_creation"]; !ok {
		t.Fatal("expected cache_creation present")
	}
	if _, ok := got["server_tool_use"]; !ok {
		t.Fatal("expected server_tool_use present")
	}
}
