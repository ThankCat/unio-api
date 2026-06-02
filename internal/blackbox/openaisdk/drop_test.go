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

// OAI-SDK-Mock-07：DeepSeek 不支持字段在出站前被 Drop（DEC-012）。
//
// SDK 发若干 DeepSeek 不支持的高级字段（store / metadata / service_tier / safety_identifier 等），
// 验证：
//   - gateway 不返回 400；
//   - 上游收到的 body 不包含被 Drop 的字段（保留 Pass 字段如 logprobs 仍出现）；
//   - SDK 端拿到正常响应。
//
// 这是 DEC-012「协议为先 Pass/Adapt/Drop」对真实 SDK 客户的端到端证明。
func TestOAISDKMockDropUnsupportedFields(t *testing.T) {
	var capturedBody []byte
	mock := newMockUpstream(t, func(_ *testing.T, w http.ResponseWriter, _ *http.Request, body []byte) {
		capturedBody = body
		writeMockChatCompletion(w, "chatcmpl-drop-1", "ok", 5, 2)
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
			openai.UserMessage("hello"),
		},
		// 这些字段 DeepSeek 都不支持，全部应被 adapter Drop。
		Store: openai.Bool(true),
		Metadata: map[string]string{
			"user_session": "abc",
		},
		ServiceTier:      openai.ChatCompletionNewParamsServiceTierAuto,
		SafetyIdentifier: openai.String("session-xyz"),
		// logprobs 是保留 Pass 字段（DeepSeek 支持），用于交叉验证 Drop 不会把保留字段误伤。
		Logprobs:    openai.Bool(true),
		TopLogprobs: openai.Int(3),
	})
	if err != nil {
		t.Fatalf("openai-go drop-fields call failed (should not 400): %v", err)
	}
	if resp.Choices[0].Message.Content != "ok" {
		t.Fatalf("unexpected message content: %q", resp.Choices[0].Message.Content)
	}

	var upstream map[string]any
	if err := json.Unmarshal(capturedBody, &upstream); err != nil {
		t.Fatalf("upstream body parse: %v", err)
	}

	droppedFields := []string{"store", "metadata", "service_tier", "safety_identifier"}
	for _, field := range droppedFields {
		if _, ok := upstream[field]; ok {
			t.Errorf("expected upstream body to NOT contain %q (should be dropped), body=%s", field, string(capturedBody))
		}
	}

	for _, field := range []string{"logprobs", "top_logprobs"} {
		if _, ok := upstream[field]; !ok {
			t.Errorf("expected upstream body to contain %q (Pass field, should not be dropped), body=%s", field, string(capturedBody))
		}
	}
}
