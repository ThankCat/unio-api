//go:build blackbox

package anthropicsdk_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/ThankCat/unio-api/internal/blackbox/sdkfixture"
)

// ANT-SDK-Mock-03：非流式响应包含 thinking content block 时，
// SDK 能识别为 thinking variant 并暴露 .Thinking / .Signature 文本。
//
// 这条用例验证：
//   - ingress 协议 DTO 把 thinking block 透传给客户；
//   - SDK ContentBlockUnion.AsThinking() 返回的内容与 mock 上游一致；
//   - text block 与 thinking block 共存时索引保持稳定。
func TestANTSDKMockNonStreamThinking(t *testing.T) {
	mock := newMockUpstream(t, func(_ *testing.T, w http.ResponseWriter, _ *http.Request, _ []byte) {
		writeMockMessageResponseRaw(w, map[string]any{
			"id":            "msg_thinking_1",
			"type":          "message",
			"role":          "assistant",
			"model":         "deepseek-v4-flash",
			"stop_reason":   "end_turn",
			"stop_sequence": nil,
			"content": []map[string]any{
				{"type": "thinking", "thinking": "let me think step by step", "signature": "sig-mock-1"},
				{"type": "text", "text": "the answer is 42"},
			},
			"usage": map[string]any{"input_tokens": 9, "output_tokens": 7},
		})
	})
	t.Cleanup(mock.Close)

	f := sdkfixture.Setup(t, sdkfixture.SetupOptions{
		Mode:            sdkfixture.UpstreamMock,
		Protocol:        "anthropic",
		AdapterKey:      "deepseek",
		UpstreamBaseURL: mock.URL,
	})

	client := anthropic.NewClient(
		option.WithBaseURL(f.AnthropicBaseURL),
		option.WithAPIKey(f.APIKey),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	msg, err := client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(f.ModelID),
		MaxTokens: 32,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock("What is 6 * 7?")),
		},
	})
	if err != nil {
		t.Fatalf("non-stream thinking call failed: %v", err)
	}

	if len(msg.Content) != 2 {
		t.Fatalf("expected 2 content blocks, got %d", len(msg.Content))
	}
	if msg.Content[0].Type != "thinking" {
		t.Fatalf("content[0].type = %q, want thinking", msg.Content[0].Type)
	}
	thinking := msg.Content[0].AsThinking()
	if thinking.Thinking != "let me think step by step" {
		t.Fatalf("thinking.thinking = %q", thinking.Thinking)
	}
	if thinking.Signature != "sig-mock-1" {
		t.Fatalf("thinking.signature = %q", thinking.Signature)
	}
	if msg.Content[1].Type != "text" || msg.Content[1].Text != "the answer is 42" {
		t.Fatalf("content[1] = %+v, want text=the answer is 42", msg.Content[1])
	}
}
