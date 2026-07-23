package responses

import (
	"encoding/json"
	"testing"

	gatewayapi "github.com/ThankCat/unio-gateway/internal/app/gatewayapi/openai/responses"
	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	chatcompletionsadapter "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/chatcompletions"
	responsesadapter "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/responses"
)

func TestDirectResponsesFirstTokenEligibleUsesApplicationEvents(t *testing.T) {
	tests := []struct {
		eventType string
		want      bool
	}{
		{eventType: gatewayapi.EventResponseCreated, want: true},
		{eventType: gatewayapi.EventOutputTextDelta, want: true},
		{eventType: gatewayapi.EventReasoningTextDelta, want: true},
		{eventType: gatewayapi.EventReasoningSummaryTextDelta, want: true},
		{eventType: gatewayapi.EventFunctionCallArgsDelta, want: true},
		{eventType: gatewayapi.EventResponseInProgress, want: false},
		{eventType: gatewayapi.EventResponseCompleted, want: false},
		{eventType: gatewayapi.EventResponseFailed, want: false},
		{eventType: "error", want: false},
		{eventType: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.eventType, func(t *testing.T) {
			meta := responsesStreamCarrierMeta(responsesStreamCarrier{
				direct: &responsesadapter.StreamChunk{EventType: tt.eventType},
			})
			if meta.FirstTokenEligible != tt.want {
				t.Fatalf("FirstTokenEligible = %v, want %v", meta.FirstTokenEligible, tt.want)
			}
		})
	}
}

func TestResponsesChatBridgeFirstTokenEligibleMatchesChatOutput(t *testing.T) {
	finish := "stop"
	tests := []struct {
		name  string
		chunk chatcompletionsadapter.ChatStreamChunk
		want  bool
	}{
		{name: "role", chunk: chatcompletionsadapter.ChatStreamChunk{Role: "assistant"}, want: true},
		{name: "content", chunk: chatcompletionsadapter.ChatStreamChunk{Content: "hello"}, want: true},
		{name: "tool call", chunk: chatcompletionsadapter.ChatStreamChunk{ToolCalls: json.RawMessage(`[{"index":0}]`)}, want: true},
		{name: "finish only", chunk: chatcompletionsadapter.ChatStreamChunk{FinishReason: &finish}, want: false},
		{name: "usage only", chunk: chatcompletionsadapter.ChatStreamChunk{Usage: &adapter.ChatUsage{TotalTokens: 1}}, want: false},
		{name: "empty", chunk: chatcompletionsadapter.ChatStreamChunk{}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meta := responsesStreamCarrierMeta(responsesStreamCarrier{chat: &tt.chunk})
			if meta.FirstTokenEligible != tt.want {
				t.Fatalf("FirstTokenEligible = %v, want %v", meta.FirstTokenEligible, tt.want)
			}
		})
	}
}
