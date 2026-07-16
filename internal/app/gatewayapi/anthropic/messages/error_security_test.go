package messages

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

// newUpstreamMessageError 构造一个携带稳定 category 的上游错误，模拟 adapter 返回。
func newUpstreamMessageError(category adapter.UpstreamErrorCategory) error {
	return adapter.NewUpstreamError(
		category,
		adapter.UpstreamMetadata{StatusCode: 0},
		failure.New(failure.CodeAdapterUpstreamStatus),
	)
}

// TestRouterV1MessagesMapsUpstreamErrors 验证上游错误分类被映射成正确的 Anthropic 原生错误形状。
//
// 关键安全语义：upstream auth/permission 是平台 channel 凭据问题，绝不能渲染成
// authentication_error，避免客户误以为自己的 API key 失效，统一归 502 api_error。
func TestRouterV1MessagesMapsUpstreamErrors(t *testing.T) {
	cases := []struct {
		name       string
		category   adapter.UpstreamErrorCategory
		wantStatus int
		wantType   string
	}{
		{"rate limit", adapter.UpstreamErrorRateLimit, http.StatusTooManyRequests, "rate_limit_error"},
		{"timeout", adapter.UpstreamErrorTimeout, http.StatusGatewayTimeout, "api_error"},
		{"bad request", adapter.UpstreamErrorBadRequest, http.StatusBadRequest, "invalid_request_error"},
		{"server error", adapter.UpstreamErrorServer, http.StatusBadGateway, "api_error"},
		{"auth not surfaced as authentication", adapter.UpstreamErrorAuth, http.StatusBadGateway, "api_error"},
		{"permission not surfaced as permission", adapter.UpstreamErrorPermission, http.StatusBadGateway, "api_error"},
		{"unknown", adapter.UpstreamErrorUnknown, http.StatusBadGateway, "api_error"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			service := &fakeMessagesService{err: newUpstreamMessageError(tc.category)}
			handler := newMessagesTestRouter(newMessagesAuthenticator(), service, nil)

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, newMessagesRequest(encodeMessageBody(t, false)))

			if rec.Code != tc.wantStatus {
				t.Fatalf("expected status %d, got %d (body %q)", tc.wantStatus, rec.Code, rec.Body.String())
			}

			body := decodeAnthropicError(t, rec.Body)
			if body.Error.Type != tc.wantType {
				t.Fatalf("expected error type %q, got %q", tc.wantType, body.Error.Type)
			}

			// 安全断言：上游凭据问题绝不暴露成 authentication_error。
			if body.Error.Type == "authentication_error" {
				t.Fatalf("upstream error must not be rendered as authentication_error")
			}
			if strings.Contains(body.Error.Message, "status") {
				t.Fatalf("public message leaked internal upstream detail: %q", body.Error.Message)
			}
		})
	}
}

// TestRouterV1MessagesStreamMapsUpstreamErrorAfterChunkStarted 验证 SSE 开始后，
// 上游错误按分类渲染成原生 error event，而不是写死的 api_error / "stream request failed"。
func TestRouterV1MessagesStreamMapsUpstreamErrorAfterChunkStarted(t *testing.T) {
	startData, _ := json.Marshal(StreamMessageStart{Type: "message_start", Message: *defaultMessageResponse("claude-sonnet-4")})
	service := &fakeMessagesService{
		streamFrames:       []StreamFrame{{EventType: "message_start", Data: startData}},
		streamErrAfterEmit: newUpstreamMessageError(adapter.UpstreamErrorRateLimit),
	}
	handler := newMessagesTestRouter(newMessagesAuthenticator(), service, nil)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, newMessagesRequest(encodeMessageBody(t, true)))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d (header sent with first chunk), got %d", http.StatusOK, rec.Code)
	}

	gotBody := rec.Body.String()
	if !strings.Contains(gotBody, "event: message_start") {
		t.Fatalf("expected first chunk written, got %q", gotBody)
	}
	if !strings.Contains(gotBody, "event: error") {
		t.Fatalf("expected SSE error event after chunk started, got %q", gotBody)
	}
	if !strings.Contains(gotBody, `"type":"rate_limit_error"`) {
		t.Fatalf("expected stream error mapped to rate_limit_error, got %q", gotBody)
	}
	if strings.Contains(gotBody, "event: message_stop") {
		t.Fatalf("expected no message_stop after stream error, got %q", gotBody)
	}
}

// TestRouterV1MessagesAcceptsAnyBeta 验证 DEC-013：anthropic-beta 一律接受不再 400。
//
// 按 DEC-012「协议为先」，beta 不因 provider 能力被 ingress 拒绝；已知 / 未知 / 看似畸形
// 的 beta 都放行进入业务链路（当前 DeepSeek 路径出站 Drop，beta 对行为无影响）。
func TestRouterV1MessagesAcceptsAnyBeta(t *testing.T) {
	cases := []struct {
		name string
		beta string
	}{
		{"well-known beta", "prompt-caching-2024-07-31"},
		{"unknown but well-formed beta", "totally-made-up-2099-01-01"},
		{"arbitrary token", "made-up-beta"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			service := &fakeMessagesService{}
			handler := newMessagesTestRouter(newMessagesAuthenticator(), service, nil)

			req := newMessagesRequest(encodeMessageBody(t, false))
			req.Header.Set("anthropic-beta", tc.beta)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected status %d, got %d (body %q)", http.StatusOK, rec.Code, rec.Body.String())
			}
			if !service.createCalled {
				t.Fatal("expected beta to be accepted and service called")
			}
		})
	}
}

// TestRouterV1MessagesAcceptsMultipleBetas 验证逗号分隔的 beta 列表（含未登记值）整体放行。
func TestRouterV1MessagesAcceptsMultipleBetas(t *testing.T) {
	service := &fakeMessagesService{}
	handler := newMessagesTestRouter(newMessagesAuthenticator(), service, nil)

	req := newMessagesRequest(encodeMessageBody(t, false))
	req.Header.Set("anthropic-beta", "prompt-caching-2024-07-31, made-up-beta")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (body %q)", http.StatusOK, rec.Code, rec.Body.String())
	}
	if !service.createCalled {
		t.Fatal("expected comma-separated beta list to be accepted and service called")
	}
}
