//go:build blackbox

package openaisdk_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"

	"github.com/ThankCat/unio-gateway/internal/blackbox/sdkfixture"
)

// OAI-SDK-Mock-06：response_format = json_object 透传到上游。
//
// SDK 设置 ResponseFormat 为 JSON object 模式；验证：
//   - upstream 收到的请求 body 含 response_format.type = "json_object"；
//   - mock 返回合法 JSON 时，SDK 端 message.content 是合法 JSON 字符串。
func TestOAISDKMockResponseFormatJSONObject(t *testing.T) {
	var capturedBody []byte
	mock := newMockUpstream(t, func(_ *testing.T, w http.ResponseWriter, _ *http.Request, body []byte) {
		capturedBody = body
		writeMockChatCompletion(w, "chatcmpl-json-1", `{"name":"Alice","age":30}`, 10, 8)
	})
	t.Cleanup(mock.Close)

	f := sdkfixture.Setup(t, sdkfixture.SetupOptions{
		Mode:            sdkfixture.UpstreamMock,
		UpstreamBaseURL: mock.URL + "/v1",
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
			openai.SystemMessage("respond only with JSON"),
			openai.UserMessage("give me a person object"),
		},
		ResponseFormat: openai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONObject: &openai.ResponseFormatJSONObjectParam{},
		},
	})
	if err != nil {
		t.Fatalf("openai-go json_object call failed: %v", err)
	}

	// SDK 端能读到合法 JSON content。
	var got map[string]any
	if err := json.Unmarshal([]byte(resp.Choices[0].Message.Content), &got); err != nil {
		t.Fatalf("expected JSON content, got %q: %v", resp.Choices[0].Message.Content, err)
	}
	if got["name"] != "Alice" {
		t.Fatalf("unexpected json content: %v", got)
	}

	// 上游请求体应包含 response_format.type = "json_object"。
	var upstream map[string]any
	if err := json.Unmarshal(capturedBody, &upstream); err != nil {
		t.Fatalf("upstream body parse: %v", err)
	}
	rf, _ := upstream["response_format"].(map[string]any)
	if rf == nil {
		t.Fatalf("expected upstream response_format set, got body=%s", string(capturedBody))
	}
	if rf["type"] != "json_object" {
		t.Fatalf("expected upstream response_format.type='json_object', got %v", rf["type"])
	}
}
