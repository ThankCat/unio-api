package chatcompletions

import (
	"encoding/json"
	"testing"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
)

func stringPtr(v string) *string { return &v }

// TestFirstTokenPayloadMatrix 冻结 Chat Completions 的权威首字矩阵。
//
// 判定的是「是否携带会改变最终模型输出的生成负载」，而不是事件类型：协议前导帧（role-only）
// 不能提前停止首字计时、锁死 fallback 或写入 TTFT。
func TestFirstTokenPayloadMatrix(t *testing.T) {
	eligible := []struct {
		name  string
		chunk ChatStreamChunk
		want  string
	}{
		{name: "content", chunk: ChatStreamChunk{Content: "hello"}, want: "hello"},
		{
			name:  "single space content is a real generated token",
			chunk: ChatStreamChunk{Content: " "},
			want:  " ",
		},
		{
			name:  "reasoning content",
			chunk: ChatStreamChunk{ReasoningContent: stringPtr("thinking")},
			want:  "thinking",
		},
		{name: "refusal", chunk: ChatStreamChunk{Refusal: stringPtr("cannot comply")}, want: "cannot comply"},
		{
			name:  "tool call name",
			chunk: ChatStreamChunk{ToolCalls: json.RawMessage(`[{"index":0,"function":{"name":"lookup"}}]`)},
			want:  "lookup",
		},
		{
			name:  "tool call arguments",
			chunk: ChatStreamChunk{ToolCalls: json.RawMessage(`[{"index":0,"function":{"arguments":"{\"q\":1}"}}]`)},
			want:  `{"q":1}`,
		},
		{
			name:  "legacy function call name",
			chunk: ChatStreamChunk{FunctionCall: json.RawMessage(`{"name":"lookup"}`)},
			want:  "lookup",
		},
	}
	for _, tc := range eligible {
		t.Run("eligible/"+tc.name, func(t *testing.T) {
			got := FirstTokenPayload(tc.chunk)
			if got != tc.want {
				t.Fatalf("payload = %q, want %q", got, tc.want)
			}
			if got == "" {
				t.Fatal("a non-empty generated payload must qualify as first token")
			}
		})
	}

	notEligible := []struct {
		name  string
		chunk ChatStreamChunk
	}{
		{name: "empty delta", chunk: ChatStreamChunk{}},
		{name: "role only", chunk: ChatStreamChunk{Role: "assistant"}},
		{name: "id and model only", chunk: ChatStreamChunk{ID: "chatcmpl-1", Model: "gpt-5"}},
		{name: "index only", chunk: ChatStreamChunk{Index: 3}},
		{name: "logprobs only", chunk: ChatStreamChunk{Logprobs: json.RawMessage(`{"content":[]}`)}},
		{name: "finish only", chunk: ChatStreamChunk{FinishReason: stringPtr("stop")}},
		{name: "usage only", chunk: ChatStreamChunk{Usage: &adapter.ChatUsage{TotalTokens: 7}}},
		{
			name:  "tool call without name or arguments",
			chunk: ChatStreamChunk{ToolCalls: json.RawMessage(`[{"index":0}]`)},
		},
		{
			name:  "empty reasoning content",
			chunk: ChatStreamChunk{ReasoningContent: stringPtr("")},
		},
		{name: "malformed tool calls", chunk: ChatStreamChunk{ToolCalls: json.RawMessage(`not-json`)}},
	}
	for _, tc := range notEligible {
		t.Run("not_eligible/"+tc.name, func(t *testing.T) {
			if got := FirstTokenPayload(tc.chunk); got != "" {
				t.Fatalf("payload = %q, want empty (must not qualify as first token)", got)
			}
		})
	}
}
