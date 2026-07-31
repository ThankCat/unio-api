package messages

import (
	"encoding/json"
	"testing"
)

// TestFirstTokenPayloadMatrix 冻结 Anthropic Messages 的权威首字矩阵。
//
// `message_start` 与空 `content_block_start` 是协议前导帧：它们不能提前停止首字计时，
// 否则「只回 message_start 然后静默」的渠道会被当成已产出首字。
func TestFirstTokenPayloadMatrix(t *testing.T) {
	eligible := []struct {
		name  string
		event MessageStreamEvent
		want  string
	}{
		{
			name: "text delta",
			event: MessageStreamEvent{
				Type: "content_block_delta",
				Data: json.RawMessage(`{"delta":{"type":"text_delta","text":"hello"}}`),
			},
			want: "hello",
		},
		{
			name: "single space delta is a real generated token",
			event: MessageStreamEvent{
				Type: "content_block_delta",
				Data: json.RawMessage(`{"delta":{"type":"text_delta","text":" "}}`),
			},
			want: " ",
		},
		{
			name: "thinking delta",
			event: MessageStreamEvent{
				Type: "content_block_delta",
				Data: json.RawMessage(`{"delta":{"type":"thinking_delta","thinking":"reason"}}`),
			},
			want: "reason",
		},
		{
			name: "input json delta",
			event: MessageStreamEvent{
				Type: "content_block_delta",
				Data: json.RawMessage(`{"delta":{"type":"input_json_delta","partial_json":"{\"q\":1}"}}`),
			},
			want: `{"q":1}`,
		},
		{
			name: "content block start with text",
			event: MessageStreamEvent{
				Type: "content_block_start",
				Data: json.RawMessage(`{"content_block":{"type":"text","text":"hi"}}`),
			},
			want: "hi",
		},
		{
			name: "tool use block carrying a name",
			event: MessageStreamEvent{
				Type: "content_block_start",
				Data: json.RawMessage(`{"content_block":{"type":"tool_use","name":"lookup"}}`),
			},
			want: "lookup",
		},
	}
	for _, tc := range eligible {
		t.Run("eligible/"+tc.name, func(t *testing.T) {
			if got := FirstTokenPayload(tc.event); got != tc.want {
				t.Fatalf("payload = %q, want %q", got, tc.want)
			}
		})
	}

	notEligible := []struct {
		name  string
		event MessageStreamEvent
	}{
		{
			name:  "message start",
			event: MessageStreamEvent{Type: "message_start", Data: json.RawMessage(`{"message":{"id":"msg_1"}}`)},
		},
		{
			name: "empty content block start",
			event: MessageStreamEvent{
				Type: "content_block_start",
				Data: json.RawMessage(`{"content_block":{"type":"text","text":""}}`),
			},
		},
		{name: "ping", event: MessageStreamEvent{Type: "ping"}},
		{
			name: "signature only delta",
			event: MessageStreamEvent{
				Type: "content_block_delta",
				Data: json.RawMessage(`{"delta":{"type":"signature_delta","signature":"abc"}}`),
			},
		},
		{
			name: "message delta with usage",
			event: MessageStreamEvent{
				Type: "message_delta",
				Data: json.RawMessage(`{"usage":{"output_tokens":3}}`),
			},
		},
		{name: "content block stop", event: MessageStreamEvent{Type: "content_block_stop"}},
		{name: "message stop", event: MessageStreamEvent{Type: "message_stop"}},
		{name: "error", event: MessageStreamEvent{Type: "error", Data: json.RawMessage(`{"error":{"type":"overloaded"}}`)}},
		{
			name: "empty text delta",
			event: MessageStreamEvent{
				Type: "content_block_delta",
				Data: json.RawMessage(`{"delta":{"type":"text_delta","text":""}}`),
			},
		},
		{name: "malformed data", event: MessageStreamEvent{Type: "content_block_delta", Data: json.RawMessage(`not-json`)}},
		{name: "empty event", event: MessageStreamEvent{}},
	}
	for _, tc := range notEligible {
		t.Run("not_eligible/"+tc.name, func(t *testing.T) {
			if got := FirstTokenPayload(tc.event); got != "" {
				t.Fatalf("payload = %q, want empty (must not qualify as first token)", got)
			}
		})
	}
}
