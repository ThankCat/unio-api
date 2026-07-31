package responses

import (
	"encoding/json"
	"testing"
)

// TestFirstTokenPayloadMatrix 冻结 OpenAI Responses 的权威首字矩阵。
//
// 关键点：判定看的是 delta 负载而不是事件类型。`response.output_text.delta` 带空 delta 不算首字，
// 否则一个「秒回 created 再静默」的上游会绕过首字保护。
func TestFirstTokenPayloadMatrix(t *testing.T) {
	eligible := []struct {
		name  string
		chunk StreamChunk
		want  string
	}{
		{
			name:  "output text delta",
			chunk: StreamChunk{EventType: eventOutputTextDelta, Data: json.RawMessage(`{"delta":"hello"}`)},
			want:  "hello",
		},
		{
			name:  "single space delta is a real generated token",
			chunk: StreamChunk{EventType: eventOutputTextDelta, Data: json.RawMessage(`{"delta":" "}`)},
			want:  " ",
		},
		{
			name:  "reasoning text delta",
			chunk: StreamChunk{EventType: eventReasoningTextDelta, Data: json.RawMessage(`{"delta":"think"}`)},
			want:  "think",
		},
		{
			name:  "reasoning summary text delta",
			chunk: StreamChunk{EventType: eventReasoningSummaryTextDelta, Data: json.RawMessage(`{"delta":"sum"}`)},
			want:  "sum",
		},
		{
			name:  "refusal delta",
			chunk: StreamChunk{EventType: eventRefusalDelta, Data: json.RawMessage(`{"delta":"no"}`)},
			want:  "no",
		},
		{
			name:  "function call arguments delta",
			chunk: StreamChunk{EventType: eventFunctionCallArgsDelta, Data: json.RawMessage(`{"delta":"{\"q\":1}"}`)},
			want:  `{"q":1}`,
		},
		{
			name: "output item added carrying a tool name",
			chunk: StreamChunk{
				EventType: eventOutputItemAdded,
				Data:      json.RawMessage(`{"item":{"type":"function_call","name":"lookup"}}`),
			},
			want: "lookup",
		},
	}
	for _, tc := range eligible {
		t.Run("eligible/"+tc.name, func(t *testing.T) {
			if got := FirstTokenPayload(tc.chunk); got != tc.want {
				t.Fatalf("payload = %q, want %q", got, tc.want)
			}
		})
	}

	notEligible := []struct {
		name  string
		chunk StreamChunk
	}{
		{name: "response created", chunk: StreamChunk{EventType: eventResponseCreated, Data: json.RawMessage(`{"response":{"id":"resp_1"}}`)}},
		{name: "response queued", chunk: StreamChunk{EventType: eventResponseQueued}},
		{name: "response in progress", chunk: StreamChunk{EventType: eventResponseInProgress}},
		{name: "response completed", chunk: StreamChunk{EventType: eventResponseCompleted}},
		{name: "response incomplete", chunk: StreamChunk{EventType: eventResponseIncomplete}},
		{name: "response failed", chunk: StreamChunk{EventType: eventResponseFailed}},
		{name: "error event", chunk: StreamChunk{EventType: eventError, Data: json.RawMessage(`{"code":"x"}`)}},
		{
			name:  "empty output text delta",
			chunk: StreamChunk{EventType: eventOutputTextDelta, Data: json.RawMessage(`{"delta":""}`)},
		},
		{
			name:  "delta event without data",
			chunk: StreamChunk{EventType: eventOutputTextDelta},
		},
		{
			name: "output item added without tool identity",
			chunk: StreamChunk{
				EventType: eventOutputItemAdded,
				Data:      json.RawMessage(`{"item":{"type":"message","id":"msg_1"}}`),
			},
		},
		{name: "unknown event type", chunk: StreamChunk{EventType: "response.something.new"}},
		{name: "empty event type", chunk: StreamChunk{}},
	}
	for _, tc := range notEligible {
		t.Run("not_eligible/"+tc.name, func(t *testing.T) {
			if got := FirstTokenPayload(tc.chunk); got != "" {
				t.Fatalf("payload = %q, want empty (must not qualify as first token)", got)
			}
		})
	}
}
