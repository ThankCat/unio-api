package chatcompletions

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/app/gatewayapi"
	gatewayopenai "github.com/ThankCat/unio-gateway/internal/app/gatewayapi/openai/chatcompletions"
	"github.com/ThankCat/unio-gateway/internal/core/auth"
)

// TestOpenAISDKBlackboxHTTPNonStream 通过 HTTP handler 验证 OpenAI SDK 形状的非流式请求（TASK-9.12）。
func TestOpenAISDKBlackboxHTTPNonStream(t *testing.T) {
	service, upstream := newSDKBlackboxHTTPService(t, false)
	defer upstream.server.Close()

	handler := newSDKBlackboxHandler(service)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{
		"model": "deepseek/deepseek-v4-pro",
		"messages": [{"role": "user", "content": "hello from sdk"}],
		"temperature": 0.7
	}`))
	req.Header.Set("Authorization", "Bearer unio_sk_test")
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var body gatewayopenai.ChatCompletionResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Choices[0].Message.ContentString() != "sdk-answer" {
		t.Fatalf("got content %q, want sdk-answer", body.Choices[0].Message.ContentString())
	}
	if body.Choices[0].Message.ReasoningContent == nil || *body.Choices[0].Message.ReasoningContent != "sdk-thought" {
		t.Fatalf("got reasoning %+v, want sdk-thought", body.Choices[0].Message.ReasoningContent)
	}
}

// TestOpenAISDKBlackboxHTTPStreamIncludeUsage 通过 HTTP handler 验证 SDK 流式 + include_usage（TASK-9.12）。
func TestOpenAISDKBlackboxHTTPStreamIncludeUsage(t *testing.T) {
	service, upstream := newSDKBlackboxHTTPService(t, true)
	defer upstream.server.Close()

	handler := newSDKBlackboxHandler(service)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{
		"model": "deepseek/deepseek-v4-pro",
		"messages": [{"role": "user", "content": "stream please"}],
		"stream": true,
		"stream_options": {"include_usage": true}
	}`))
	req.Header.Set("Authorization", "Bearer unio_sk_test")
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	if !strings.Contains(body, "data: ") {
		t.Fatalf("expected SSE data events, got %q", body)
	}
	if !strings.Contains(body, `"usage":null`) {
		t.Fatal("expected intermediate chunk with usage:null")
	}
	if !strings.Contains(body, `"total_tokens":26`) {
		t.Fatal("expected final usage chunk with total_tokens=26")
	}
	if !strings.Contains(body, "data: [DONE]") {
		t.Fatal("expected SSE [DONE]")
	}
}

func newSDKBlackboxHTTPService(t *testing.T, stream bool) (*ChatCompletionService, *mockUpstream) {
	t.Helper()

	upstream := newMockUpstream(t, func(w http.ResponseWriter) {
		if stream {
			writeDeepSeekStreamEvents(t, w, []string{
				`data: {"id":"chatcmpl-deepseek","object":"chat.completion.chunk","created":1710000000,"model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"role":"assistant","content":"stream"},"finish_reason":null}],"usage":null}` + "\n\n",
				`data: {"id":"chatcmpl-deepseek","object":"chat.completion.chunk","created":1710000001,"model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"content":""},"finish_reason":"stop"}],"usage":` + deepseekUsageJSON() + `}` + "\n\n",
				"data: [DONE]\n\n",
			})
			return
		}

		encodeUpstreamNonStreamResponse(t, w, map[string]any{
			"role":              "assistant",
			"content":           "sdk-answer",
			"reasoning_content": "sdk-thought",
		}, "stop")
	})

	service, _ := newParityService(t, upstream)
	return service, upstream
}

type sdkBlackboxAuthenticator struct{}

func (sdkBlackboxAuthenticator) AuthenticateAPIKey(_ context.Context, _ string) (*auth.APIKeyPrincipal, error) {
	return &auth.APIKeyPrincipal{APIKeyID: 1, UserID: 42, KeyPrefix: "unio_sk_test"}, nil
}

func newSDKBlackboxHandler(service *ChatCompletionService) http.Handler {
	return gatewayapi.NewRouter(gatewayapi.RouterDeps{
		Logger:                zap.NewNop(),
		APIKeyAuthenticator:   sdkBlackboxAuthenticator{},
		ChatCompletionService: service,
	})
}
