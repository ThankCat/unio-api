//go:build blackbox

// Package openaisdk_test 用真实 openai-go SDK 作为客户端，对完整 unio gateway
// HTTP server 发请求，验证 "客户只改 base_url + api_key 即可 drop-in" 这一核心契约。
//
// 仅在 -tags=blackbox 下编译；生产二进制不会引入 openai-go。
package openaisdk_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"

	"github.com/ThankCat/unio-api/internal/blackbox/sdkfixture"
)

// OAI-SDK-Mock-01：openai-go SDK 通过 unio gateway 调 mock 上游成功的非流式 chat completion。
//
// 这个用例不依赖真实 DeepSeek，只验证：
//   - SDK 不报错；
//   - 响应 message.content 与上游 mock 返回一致；
//   - 响应 usage 字段（prompt/completion/total tokens）按 OpenAI 协议反序列化成功；
//   - upstream mock 收到的 request body 是合法 OpenAI Chat Completions 请求
//     （包含 model + messages + stream=false）。
func TestOAISDKMockNonStreamSucceeds(t *testing.T) {
	var capturedBody []byte
	mock := newMockUpstream(t, func(_ *testing.T, w http.ResponseWriter, _ *http.Request, body []byte) {
		capturedBody = body
		writeMockChatCompletion(w, "deepseek-mock", "ok", 6, 1)
	})
	t.Cleanup(mock.Close)

	f := sdkfixture.Setup(t, sdkfixture.SetupOptions{
		Mode:            sdkfixture.UpstreamMock,
		UpstreamBaseURL: mock.URL + "/v1",
		ModelID:         "deepseek-chat",
		UpstreamModel:   "deepseek-chat",
	})

	client := openai.NewClient(
		option.WithBaseURL(f.BaseURL),
		option.WithAPIKey(f.APIKey),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model: openai.ChatModel(f.ModelID),
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("hello"),
		},
	})
	if err != nil {
		t.Fatalf("openai-go non-stream call failed: %v", err)
	}

	if len(resp.Choices) == 0 {
		t.Fatal("expected at least one choice")
	}
	if got := resp.Choices[0].Message.Content; got != "ok" {
		t.Fatalf("unexpected message content: %q", got)
	}
	if resp.Usage.PromptTokens != 6 || resp.Usage.CompletionTokens != 1 || resp.Usage.TotalTokens != 7 {
		t.Fatalf("unexpected usage: %+v", resp.Usage)
	}

	var upstreamBody map[string]any
	if err := json.Unmarshal(capturedBody, &upstreamBody); err != nil {
		t.Fatalf("upstream body not valid json: %v (raw: %s)", err, string(capturedBody))
	}
	if model, _ := upstreamBody["model"].(string); model != "deepseek-chat" {
		t.Fatalf("expected upstream model 'deepseek-chat', got %q", model)
	}
	if stream, ok := upstreamBody["stream"].(bool); ok && stream {
		t.Fatalf("expected upstream stream=false, got true")
	}
	if msgs, ok := upstreamBody["messages"].([]any); !ok || len(msgs) == 0 {
		t.Fatalf("expected upstream messages array, got %v", upstreamBody["messages"])
	}
}

