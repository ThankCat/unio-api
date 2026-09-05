package responses

import (
	"encoding/json"
	"testing"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	chatcompletionsadapter "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/chatcompletions"
	responsesadapter "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/responses"
)

// TestResponsesCarrierMetaWiresFirstTokenPayload 验证两类候选（直传 Responses 事件、桥接 chat chunk）
// 都把 adapter 的两个正交判定接到三个事实上：Progress（结构性进展）、VisibleContent/VisibleText
// （可见内容，结算计量）、FirstTokenEligible（按 TTFT 口径派生）。
//
// 完整协议矩阵由两个 adapter 包的判定测试覆盖，这里只验证载体接线，重点是「混合候选池里两类载体的口径必须一致」，
// 以及 reasoning item 事件（Codex 思考阶段）算进展但不算可见内容。
func TestResponsesCarrierMetaWiresFirstTokenPayload(t *testing.T) {
	t.Cleanup(func() { adapter.SetTTFTMode(adapter.DefaultTTFTMode) })
	tests := []struct {
		name            string
		carrier         responsesStreamCarrier
		wantProgress    bool
		wantVisible     bool
		wantVisibleText string
	}{
		{
			name: "direct output text delta",
			carrier: responsesStreamCarrier{direct: &responsesadapter.StreamChunk{
				EventType: "response.output_text.delta",
				Data:      json.RawMessage(`{"delta":"hello"}`),
			}},
			wantProgress:    true,
			wantVisible:     true,
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
			name: "direct reasoning item done is progress without visible content",
			carrier: responsesStreamCarrier{direct: &responsesadapter.StreamChunk{
				EventType: "response.output_item.done",
				Data:      json.RawMessage(`{"item":{"type":"reasoning","encrypted_content":"gAAAA","summary":[]}}`),
			}},
			wantProgress: true,
		},
		{
			name: "direct success terminal is never progress (delivered by Finish after settlement)",
			carrier: responsesStreamCarrier{direct: &responsesadapter.StreamChunk{
				EventType: "response.completed",
				Data:      json.RawMessage(`{"response":{"id":"resp_1","status":"completed"}}`),
			}},
		},
		{
			name:            "bridged chat content",
			carrier:         responsesStreamCarrier{chat: &chatcompletionsadapter.ChatStreamChunk{Content: "hi"}},
			wantProgress:    true,
			wantVisible:     true,
			wantVisibleText: "hi",
		},
		{
			name:         "bridged chat role-only is progress without visible content",
			carrier:      responsesStreamCarrier{chat: &chatcompletionsadapter.ChatStreamChunk{Role: "assistant"}},
			wantProgress: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adapter.SetTTFTMode(adapter.TTFTModeSemantic)
			meta := responsesStreamCarrierMeta(tt.carrier)
			if meta.Progress != tt.wantProgress {
				t.Fatalf("Progress = %v, want %v", meta.Progress, tt.wantProgress)
			}
			if meta.VisibleContent != tt.wantVisible {
				t.Fatalf("VisibleContent = %v, want %v", meta.VisibleContent, tt.wantVisible)
			}
			if meta.VisibleText != tt.wantVisibleText {
				t.Fatalf("VisibleText = %q, want %q", meta.VisibleText, tt.wantVisibleText)
			}
			if meta.FirstTokenEligible != tt.wantProgress {
				t.Fatalf("semantic FirstTokenEligible = %v, want %v", meta.FirstTokenEligible, tt.wantProgress)
			}

			adapter.SetTTFTMode(adapter.TTFTModeVisible)
			meta = responsesStreamCarrierMeta(tt.carrier)
			if meta.FirstTokenEligible != tt.wantVisible {
				t.Fatalf("visible FirstTokenEligible = %v, want %v", meta.FirstTokenEligible, tt.wantVisible)
			}
		})
	}
}
