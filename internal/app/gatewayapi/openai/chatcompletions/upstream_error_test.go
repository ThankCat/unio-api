package chatcompletions

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ThankCat/unio-api/internal/core/adapter"
	"github.com/ThankCat/unio-api/internal/core/auth"
	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/httpx"
)

// newUpstreamChatError 构造一个携带稳定 category 的上游错误，模拟 adapter 返回。
//
// cause 携带 failure.CodeAdapterUpstreamStatus，与生产 adapter 一致，确保 failure.CodeOf
// 不会误命中余额 / 不支持参数等更靠前的分支。
func newUpstreamChatError(category adapter.UpstreamErrorCategory) error {
	return adapter.NewUpstreamError(
		category,
		adapter.UpstreamMetadata{StatusCode: 0},
		failure.New(failure.CodeAdapterUpstreamStatus),
	)
}

func chatAuthenticator() *fakeAPIKeyAuthenticator {
	return &fakeAPIKeyAuthenticator{
		principal: &auth.APIKeyPrincipal{
			APIKeyID:  1,
			UserID:    1,
			KeyPrefix: "unio_sk_test",
		},
	}
}

// TestRouterV1ChatCompletionMapsUpstreamErrors 验证上游错误分类被映射成正确的 OpenAI 错误形状。
//
// 关键安全语义：upstream auth/permission 是平台 channel 凭据问题，绝不能渲染成 401 /
// authentication_error，避免客户误以为自己的 API key 失效，统一归 502 api_error。
func TestRouterV1ChatCompletionMapsUpstreamErrors(t *testing.T) {
	cases := []struct {
		name       string
		category   adapter.UpstreamErrorCategory
		wantStatus int
		wantCode   string
		wantType   string
	}{
		{"rate limit", adapter.UpstreamErrorRateLimit, http.StatusTooManyRequests, "rate_limit_exceeded", "rate_limit_error"},
		{"timeout", adapter.UpstreamErrorTimeout, http.StatusGatewayTimeout, "upstream_timeout", "api_error"},
		{"bad request", adapter.UpstreamErrorBadRequest, http.StatusBadRequest, "invalid_request", "invalid_request_error"},
		{"server error", adapter.UpstreamErrorServer, http.StatusBadGateway, "upstream_error", "api_error"},
		{"auth not surfaced as 401", adapter.UpstreamErrorAuth, http.StatusBadGateway, "upstream_error", "api_error"},
		{"permission not surfaced as 403", adapter.UpstreamErrorPermission, http.StatusBadGateway, "upstream_error", "api_error"},
		{"unknown", adapter.UpstreamErrorUnknown, http.StatusBadGateway, "upstream_error", "api_error"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			service := &fakeChatCompletionService{err: newUpstreamChatError(tc.category)}
			handler := newTestRouter(chatAuthenticator(), service, nil)

			buf := new(bytes.Buffer)
			reqBody := ChatCompletionRequest{
				Model:    "openai/gpt-4.1",
				Messages: []ChatMessage{{Role: "user", Content: jsonContent("Hello")}},
			}
			if err := json.NewEncoder(buf).Encode(reqBody); err != nil {
				t.Fatalf("encode request body: %v", err)
			}

			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", buf)
			req.Header.Set("Authorization", "Bearer unio_sk_test")
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("expected status %d, got %d", tc.wantStatus, rec.Code)
			}

			var body httpx.ErrorResponse
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatalf("decode response body: %v", err)
			}

			if body.Error.Code != tc.wantCode {
				t.Fatalf("expected error code %q, got %q", tc.wantCode, body.Error.Code)
			}
			if body.Error.Type != tc.wantType {
				t.Fatalf("expected error type %q, got %q", tc.wantType, body.Error.Type)
			}

			// 安全断言：永远不向客户暴露上游原始诊断或 401/authentication 语义。
			if body.Error.Type == "authentication_error" {
				t.Fatalf("upstream error must not be rendered as authentication_error")
			}
			if strings.Contains(body.Error.Message, "status") {
				t.Fatalf("public message leaked internal upstream detail: %q", body.Error.Message)
			}
		})
	}
}

// TestRouterV1ChatCompletionStreamMapsUpstreamErrorAfterChunkStarted 验证 SSE 开始后，
// 上游错误也按分类渲染成 data-only error chunk，而不是写死的通用错误。
func TestRouterV1ChatCompletionStreamMapsUpstreamErrorAfterChunkStarted(t *testing.T) {
	service := &fakeChatCompletionService{
		streamResp: []ChatCompletionStreamResponse{
			{
				ID:     "chatcmpl_stream_test",
				Object: "chat.completion.chunk",
				Model:  "openai/gpt-4.1",
				Choices: []ChatCompletionStreamChoice{
					{
						Index: 0,
						Delta: ChatCompletionStreamDelta{Role: "assistant", Content: "mock response"},
					},
				},
			},
		},
		streamErrAfterEmit: newUpstreamChatError(adapter.UpstreamErrorRateLimit),
	}
	handler := newTestRouter(chatAuthenticator(), service, nil)

	stream := true
	reqBody := ChatCompletionRequest{
		Model:    "openai/gpt-4.1",
		Messages: []ChatMessage{{Role: "user", Content: jsonContent("Hello")}},
		Stream:   &stream,
	}
	buf := new(bytes.Buffer)
	if err := json.NewEncoder(buf).Encode(reqBody); err != nil {
		t.Fatalf("encode request body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", buf)
	req.Header.Set("Authorization", "Bearer unio_sk_test")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d (header sent with first chunk), got %d", http.StatusOK, rec.Code)
	}

	gotBody := rec.Body.String()
	if !strings.Contains(gotBody, "mock response") {
		t.Fatalf("expected first chunk written, got %q", gotBody)
	}
	if !strings.Contains(gotBody, `"type":"rate_limit_error"`) {
		t.Fatalf("expected SSE error mapped to rate_limit_error, got %q", gotBody)
	}
	if strings.Contains(gotBody, "data: [DONE]") {
		t.Fatalf("expected no [DONE] after stream error, got %q", gotBody)
	}
}
