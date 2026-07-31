package responses

import (
	"encoding/json"
	"testing"

	chatcompletionsadapter "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/chatcompletions"
	responsesadapter "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/responses"
)

// TestResponsesCarrierMetaWiresFirstTokenPayload 验证两类候选（直传 Responses 事件、桥接 chat chunk）
// 都把 adapter 的权威判定同时接到 FirstTokenEligible 和 VisibleText 上。
//
// 完整协议矩阵由两个 adapter 包的 TestFirstTokenPayloadMatrix 覆盖，这里只验证载体接线，
// 重点是「混合候选池里两类载体的首字口径必须一致」。
func TestResponsesCarrierMetaWiresFirstTokenPayload(t *testing.T) {
	tests := []struct {
		name            string
		carrier         responsesStreamCarrier
		wantEligible    bool
		wantVisibleText string
	}{
		{
			name: "direct output text delta",
			carrier: responsesStreamCarrier{direct: &responsesadapter.StreamChunk{
				EventType: "response.output_text.delta",
				Data:      json.RawMessage(`{"delta":"hello"}`),
			}},
			wantEligible:    true,
			wantVisibleText: "hello",
		},
		{
			name: "direct response.created is a prelude frame",
			carrier: responsesStreamCarrier{direct: &responsesadapter.StreamChunk{
				EventType: "response.created",
				Data:      json.RawMessage(`{"response":{"id":"resp_1"}}`),
			}},
		},
		{
			name:            "bridged chat content",
			carrier:         responsesStreamCarrier{chat: &chatcompletionsadapter.ChatStreamChunk{Content: "hi"}},
			wantEligible:    true,
			wantVisibleText: "hi",
		},
		{
			name:    "bridged chat role-only is a prelude frame",
			carrier: responsesStreamCarrier{chat: &chatcompletionsadapter.ChatStreamChunk{Role: "assistant"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meta := responsesStreamCarrierMeta(tt.carrier)
			if meta.FirstTokenEligible != tt.wantEligible {
				t.Fatalf("FirstTokenEligible = %v, want %v", meta.FirstTokenEligible, tt.wantEligible)
			}
			if meta.VisibleText != tt.wantVisibleText {
				t.Fatalf("VisibleText = %q, want %q", meta.VisibleText, tt.wantVisibleText)
			}
		})
	}
}
