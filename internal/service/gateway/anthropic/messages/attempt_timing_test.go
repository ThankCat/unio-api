package messages

import (
	"encoding/json"
	"testing"

	messagesadapter "github.com/ThankCat/unio-gateway/internal/core/adapter/anthropic/messages"
)

// TestMessagesStreamChunkMetaWiresFirstTokenPayload 验证 meta 映射把 adapter 的权威判定
// 同时接到 FirstTokenEligible 和 VisibleText 上——两者必须同源，否则会出现
// 「算了首字但 partial settlement 不计这段文本」或反之的错位。
//
// 完整的协议判定矩阵由 adapter 层 TestFirstTokenPayloadMatrix 覆盖，这里只验证接线。
func TestMessagesStreamChunkMetaWiresFirstTokenPayload(t *testing.T) {
	tests := []struct {
		name            string
		event           messagesadapter.MessageStreamEvent
		wantEligible    bool
		wantVisibleText string
	}{
		{
			name: "text delta carries both facts",
			event: messagesadapter.MessageStreamEvent{
				Type: "content_block_delta",
				Data: json.RawMessage(`{"delta":{"type":"text_delta","text":"hello"}}`),
			},
			wantEligible:    true,
			wantVisibleText: "hello",
		},
		{
			name:  "message_start is a prelude frame",
			event: messagesadapter.MessageStreamEvent{Type: "message_start", Data: json.RawMessage(`{"message":{"id":"msg_1"}}`)},
		},
		{name: "ping is not a first token", event: messagesadapter.MessageStreamEvent{Type: "ping"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meta := messagesStreamChunkMeta(tt.event)
			if meta.FirstTokenEligible != tt.wantEligible {
				t.Fatalf("FirstTokenEligible = %v, want %v", meta.FirstTokenEligible, tt.wantEligible)
			}
			if meta.VisibleText != tt.wantVisibleText {
				t.Fatalf("VisibleText = %q, want %q", meta.VisibleText, tt.wantVisibleText)
			}
		})
	}
}
