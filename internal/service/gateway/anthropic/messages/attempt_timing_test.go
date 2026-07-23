package messages

import (
	"testing"

	messagesadapter "github.com/ThankCat/unio-gateway/internal/core/adapter/anthropic/messages"
)

func TestMessagesFirstTokenEligibleUsesApplicationEvents(t *testing.T) {
	tests := []struct {
		eventType string
		want      bool
	}{
		{eventType: "message_start", want: true},
		{eventType: "content_block_delta", want: true},
		{eventType: "content_block_start", want: false},
		{eventType: "content_block_stop", want: false},
		{eventType: "message_delta", want: false},
		{eventType: "message_stop", want: false},
		{eventType: "ping", want: false},
		{eventType: "error", want: false},
		{eventType: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.eventType, func(t *testing.T) {
			meta := messagesStreamChunkMeta(messagesadapter.MessageStreamEvent{Type: tt.eventType})
			if meta.FirstTokenEligible != tt.want {
				t.Fatalf("FirstTokenEligible = %v, want %v", meta.FirstTokenEligible, tt.want)
			}
		})
	}
}
