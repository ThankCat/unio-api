//go:build blackbox

// Package anthropicsdk_test 用真实 anthropic-sdk-go SDK 作为客户端，对完整 unio gateway
// HTTP server 发请求，验证 "客户只改 base_url + api_key 即可 drop-in" 这一核心契约。
//
// 仅在 -tags=blackbox 下编译；生产二进制不会引入 anthropic-sdk-go。
package anthropicsdk_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/ThankCat/unio-gateway/internal/blackbox/sdkfixture"
)

// ANT-SDK-Mock-01：anthropic-sdk-go SDK 通过 unio gateway 调 mock 上游成功的非流式 messages。
//
// 这个用例验证：
//   - SDK 不报错；
//   - 响应 content[0].text 与上游 mock 返回一致；
//   - 响应 usage 字段（input_tokens / output_tokens）按 Anthropic 协议反序列化成功；
//   - upstream mock 收到的 request body 是合法 Anthropic Messages 请求
//     （包含 model + max_tokens + messages + stream=false）。
func TestANTSDKMockNonStreamSucceeds(t *testing.T) {
	var capturedBody []byte
	mock := newMockUpstream(t, func(_ *testing.T, w http.ResponseWriter, _ *http.Request, body []byte) {
		capturedBody = body
		writeMockMessageResponse(w, "msg_mock_1", "ok", 11, 1)
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
		MaxTokens: 16,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock("hello")),
		},
	})
	if err != nil {
		t.Fatalf("anthropic-sdk-go non-stream call failed: %v", err)
	}

	if len(msg.Content) == 0 {
		t.Fatal("expected at least one content block")
	}
	text := msg.Content[0].Text
	if text != "ok" {
		t.Fatalf("unexpected content[0].text: %q", text)
	}
	if msg.Usage.InputTokens != 11 || msg.Usage.OutputTokens != 1 {
		t.Fatalf("unexpected usage: %+v", msg.Usage)
	}

	var upstreamBody map[string]any
	if err := json.Unmarshal(capturedBody, &upstreamBody); err != nil {
		t.Fatalf("upstream body not valid json: %v (raw: %s)", err, string(capturedBody))
	}
	if model, _ := upstreamBody["model"].(string); model != f.ModelID {
		t.Fatalf("expected upstream model %q, got %q", f.ModelID, model)
	}
	if stream, ok := upstreamBody["stream"].(bool); ok && stream {
		t.Fatalf("expected upstream stream=false, got true")
	}
	if msgs, ok := upstreamBody["messages"].([]any); !ok || len(msgs) == 0 {
		t.Fatalf("expected upstream messages array, got %v", upstreamBody["messages"])
	}
	if maxTokens, _ := upstreamBody["max_tokens"].(float64); maxTokens != 16 {
		t.Fatalf("expected upstream max_tokens=16, got %v", upstreamBody["max_tokens"])
	}
}
