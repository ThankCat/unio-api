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

	"github.com/ThankCat/unio-api/internal/blackbox/sdkfixture"
)

// OAI-SDK-Mock-03：非流式 reasoning。
//
// DeepSeek reasoner 非流式 message 同时带 reasoning_content 与 content；
// 验证 SDK 能正常读 content，且 reasoning_content 在 RawJSON ExtraFields 可用。
func TestOAISDKMockNonStreamReasoning(t *testing.T) {
	mock := newMockUpstream(t, func(_ *testing.T, w http.ResponseWriter, _ *http.Request, _ []byte) {
		writeMockChatCompletionWithMessage(w, "chatcmpl-reason-1", map[string]any{
			"role":              "assistant",
			"content":           "the sky is blue because of Rayleigh scattering",
			"reasoning_content": "Let me think step by step.\nFirst, the sun emits white light...",
		}, "stop", 12, 8)
	})
	t.Cleanup(mock.Close)

	f := sdkfixture.Setup(t, sdkfixture.SetupOptions{
		Mode:            sdkfixture.UpstreamMock,
		UpstreamBaseURL: mock.URL + "/v1",
		ModelID:         "deepseek-reasoner",
		UpstreamModel:   "deepseek-reasoner",
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
			openai.UserMessage("why is the sky blue?"),
		},
	})
	if err != nil {
		t.Fatalf("openai-go reasoning non-stream call failed: %v", err)
	}

	if got := resp.Choices[0].Message.Content; got != "the sky is blue because of Rayleigh scattering" {
		t.Fatalf("unexpected message content: %q", got)
	}

	// reasoning_content 在 SDK 没有强类型字段；从 RawJSON ExtraFields 读取。
	rawReasoning := resp.Choices[0].Message.JSON.ExtraFields["reasoning_content"].Raw()
	if rawReasoning == "" || rawReasoning == "null" {
		t.Fatalf("expected reasoning_content in ExtraFields, got %q", rawReasoning)
	}
	var reasoning string
	if err := json.Unmarshal([]byte(rawReasoning), &reasoning); err != nil {
		t.Fatalf("unmarshal reasoning_content: %v", err)
	}
	if reasoning == "" {
		t.Fatal("expected non-empty reasoning_content text")
	}
}
