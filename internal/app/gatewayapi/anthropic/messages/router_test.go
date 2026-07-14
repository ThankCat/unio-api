package messages

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ThankCat/unio-api/internal/app/gatewayapi/anthropic"
	"github.com/ThankCat/unio-api/internal/app/gatewayapi/middleware"
	"github.com/ThankCat/unio-api/internal/core/auth"
	"github.com/ThankCat/unio-api/internal/core/routing"
	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/ratelimit"
)

// fakeMessagesAuthenticator 是 /v1/messages 集成测试使用的 API Key 认证器替身。
type fakeMessagesAuthenticator struct {
	principal *auth.APIKeyPrincipal
	err       error
	token     string
}

// AuthenticateAPIKey 记录收到的明文 key，并返回测试预设的认证结果。
func (a *fakeMessagesAuthenticator) AuthenticateAPIKey(ctx context.Context, plaintext string) (*auth.APIKeyPrincipal, error) {
	a.token = plaintext
	return a.principal, a.err
}

// newMessagesAuthenticator 创建默认认证通过的测试认证器。
func newMessagesAuthenticator() *fakeMessagesAuthenticator {
	return &fakeMessagesAuthenticator{
		principal: &auth.APIKeyPrincipal{
			APIKeyID:  1,
			UserID:    1,
			KeyPrefix: "unio_sk_test",
		},
	}
}

// messagesRateLimiter 是 /v1/messages 集成测试使用的限流器替身。
type messagesRateLimiter struct {
	decision ratelimit.Decision
	err      error
}

// AllowRouteUserRequest 返回测试预设的限流判断结果。
func (l *messagesRateLimiter) AllowRouteUserRequest(_ context.Context, _, _ int64, _ ratelimit.Limits) (ratelimit.Decision, error) {
	return l.decision, l.err
}

// newAllowingMessagesRateLimiter 创建默认放行请求的测试限流器。
func newAllowingMessagesRateLimiter() *messagesRateLimiter {
	return &messagesRateLimiter{
		decision: ratelimit.Decision{
			Allowed:   true,
			Limit:     60,
			Remaining: 59,
			ResetAt:   time.Date(2026, 6, 2, 10, 1, 0, 0, time.UTC),
		},
	}
}

// fakeMessagesService 是 /v1/messages handler 测试使用的 service 替身。
//
// 它不依赖 gateway/adapter 组合，只按测试预设返回非流式响应、错误，或逐帧发出
// Anthropic 原生 SSE 帧，用来验证 handler 的鉴权、ingress 校验与 SSE 写出行为。
type fakeMessagesService struct {
	createCalled       bool
	streamCalled       bool
	req                MessageRequest
	createResp         *MessageResponse
	err                error
	streamFrames       []StreamFrame
	streamErrAfterEmit error
}

// CreateMessage 记录 handler 传入的请求，并返回测试预设的非流式响应或错误。
func (s *fakeMessagesService) CreateMessage(ctx context.Context, req MessageRequest) (*MessageResponse, error) {
	s.createCalled = true
	s.req = req

	if s.err != nil {
		return nil, s.err
	}

	if s.createResp != nil {
		return s.createResp, nil
	}

	return defaultMessageResponse(req.Model), nil
}

// StreamMessage 记录流式请求，先发出预设错误，否则逐帧 emit 后返回 emit 完成后的错误。
func (s *fakeMessagesService) StreamMessage(ctx context.Context, req MessageRequest, emit func(StreamFrame) error) error {
	s.streamCalled = true
	s.req = req

	if s.err != nil {
		return s.err
	}

	frames := s.streamFrames
	if frames == nil && s.streamErrAfterEmit == nil {
		frames = defaultStreamFrames(req.Model)
	}

	for _, frame := range frames {
		if emitErr := emit(frame); emitErr != nil {
			return emitErr
		}
	}

	return s.streamErrAfterEmit
}

// defaultMessageResponse 返回一个最小可用的 Anthropic 原生 Message 响应。
func defaultMessageResponse(model string) *MessageResponse {
	stop := "end_turn"
	return &MessageResponse{
		ID:         "msg_test",
		Type:       "message",
		Role:       "assistant",
		Model:      model,
		Content:    []json.RawMessage{json.RawMessage(`{"type":"text","text":"hi"}`)},
		StopReason: &stop,
		Usage:      MessageUsage{InputTokens: 5, OutputTokens: 3},
	}
}

// defaultStreamFrames 构造一组最小但完整的 Anthropic 原生流式帧（start → delta → stop）。
func defaultStreamFrames(model string) []StreamFrame {
	startData, _ := json.Marshal(StreamMessageStart{Type: "message_start", Message: *defaultMessageResponse(model)})
	deltaData, _ := json.Marshal(StreamContentBlockDelta{
		Type:  "content_block_delta",
		Index: 0,
		Delta: ContentBlockDelta{Type: "text_delta", Text: "hi"},
	})
	stopData, _ := json.Marshal(StreamMessageStop{Type: "message_stop"})

	return []StreamFrame{
		{EventType: "message_start", Data: startData},
		{EventType: "content_block_delta", Data: deltaData},
		{EventType: "message_stop", Data: stopData},
	}
}

// newMessagesTestRouter 创建仅含 /v1/messages 路由的测试 router，挂载与生产一致的鉴权与限流中间件。
//
// 它不引入 gatewayapi 根包，避免 messages → gatewayapi → messages 的测试编译环；
// 顶层 httpmw（request id/metrics/logger）在 gatewayapi router_test.go 中单独验证。
func newMessagesTestRouter(authenticator middleware.APIKeyAuthenticator, service MessagesService, limiter middleware.KeyRateLimiter) http.Handler {
	if service == nil {
		service = &fakeMessagesService{}
	}

	if limiter == nil {
		limiter = newAllowingMessagesRateLimiter()
	}

	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		r.Use(middleware.APIKeyAuth(authenticator))
		r.Use(middleware.RateLimit(limiter, middleware.RateLimitOptions{
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		}))

		r.Method(http.MethodPost, "/messages", NewMessagesHandler(service, slog.New(slog.NewTextHandler(io.Discard, nil))))
	})

	return r
}

// encodeMessageBody 把请求体编码为 JSON buffer，编码失败直接让测试 fatal。
func encodeMessageBody(t *testing.T, stream bool) *bytes.Buffer {
	t.Helper()

	maxTokens := 64
	req := MessageRequest{
		Model:     "claude-sonnet-4",
		MaxTokens: &maxTokens,
		Messages:  []Message{{Role: "user", Content: json.RawMessage(`"Hello"`)}},
	}
	if stream {
		req.Stream = &stream
	}

	buf := new(bytes.Buffer)
	if err := json.NewEncoder(buf).Encode(req); err != nil {
		t.Fatalf("encode message request body: %v", err)
	}
	return buf
}

// newMessagesRequest 构造一个带合法 x-api-key 与 anthropic-version 的 /v1/messages 请求。
func newMessagesRequest(body io.Reader) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", body)
	req.Header.Set("x-api-key", "unio_sk_test")
	req.Header.Set("anthropic-version", "2023-06-01")
	return req
}

// decodeAnthropicError 解码 Anthropic 原生错误响应，失败直接 fatal。
func decodeAnthropicError(t *testing.T, body io.Reader) anthropic.ErrorResponse {
	t.Helper()

	var resp anthropic.ErrorResponse
	if err := json.NewDecoder(body).Decode(&resp); err != nil {
		t.Fatalf("decode anthropic error: %v", err)
	}
	return resp
}

func TestRouterV1MessagesMissingAPIKey(t *testing.T) {
	handler := newMessagesTestRouter(newMessagesAuthenticator(), nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", encodeMessageBody(t, false))
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestRouterV1MessagesWithXAPIKey(t *testing.T) {
	service := &fakeMessagesService{}
	handler := newMessagesTestRouter(newMessagesAuthenticator(), service, nil)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, newMessagesRequest(encodeMessageBody(t, false)))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (body %q)", http.StatusOK, rec.Code, rec.Body.String())
	}

	if !service.createCalled {
		t.Fatal("expected CreateMessage to be called")
	}

	var body MessageResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response body: %v", err)
	}

	if body.Type != "message" {
		t.Fatalf("expected type %q, got %q", "message", body.Type)
	}
	if body.Role != "assistant" {
		t.Fatalf("expected role %q, got %q", "assistant", body.Role)
	}
	if body.Model != "claude-sonnet-4" {
		t.Fatalf("expected model echoed, got %q", body.Model)
	}
}

func TestRouterV1MessagesWithBearerAPIKey(t *testing.T) {
	handler := newMessagesTestRouter(newMessagesAuthenticator(), &fakeMessagesService{}, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", encodeMessageBody(t, false))
	req.Header.Set("Authorization", "Bearer unio_sk_test")
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (body %q)", http.StatusOK, rec.Code, rec.Body.String())
	}
}

func TestRouterV1MessagesMissingAnthropicVersion(t *testing.T) {
	service := &fakeMessagesService{}
	handler := newMessagesTestRouter(newMessagesAuthenticator(), service, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", encodeMessageBody(t, false))
	req.Header.Set("x-api-key", "unio_sk_test")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}
	if service.createCalled {
		t.Fatal("expected handler to reject before calling service")
	}

	body := decodeAnthropicError(t, rec.Body)
	if body.Type != "error" {
		t.Fatalf("expected top-level type %q, got %q", "error", body.Type)
	}
	if body.Error.Type != "invalid_request_error" {
		t.Fatalf("expected error type %q, got %q", "invalid_request_error", body.Error.Type)
	}
}

func TestRouterV1MessagesUnsupportedAnthropicVersion(t *testing.T) {
	service := &fakeMessagesService{}
	handler := newMessagesTestRouter(newMessagesAuthenticator(), service, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", encodeMessageBody(t, false))
	req.Header.Set("x-api-key", "unio_sk_test")
	req.Header.Set("anthropic-version", "1999-01-01")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}
	if service.createCalled {
		t.Fatal("expected handler to reject before calling service")
	}

	body := decodeAnthropicError(t, rec.Body)
	if body.Error.Type != "invalid_request_error" {
		t.Fatalf("expected error type %q, got %q", "invalid_request_error", body.Error.Type)
	}
}

func TestRouterV1MessagesInvalidBody(t *testing.T) {
	handler := newMessagesTestRouter(newMessagesAuthenticator(), &fakeMessagesService{}, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{"))
	req.Header.Set("x-api-key", "unio_sk_test")
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}

	body := decodeAnthropicError(t, rec.Body)
	if body.Error.Type != "invalid_request_error" {
		t.Fatalf("expected error type %q, got %q", "invalid_request_error", body.Error.Type)
	}
}

func TestRouterV1MessagesMapsModelNotFound(t *testing.T) {
	service := &fakeMessagesService{err: routing.ErrModelNotFound}
	handler := newMessagesTestRouter(newMessagesAuthenticator(), service, nil)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, newMessagesRequest(encodeMessageBody(t, false)))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, rec.Code)
	}

	body := decodeAnthropicError(t, rec.Body)
	if body.Error.Type != "not_found_error" {
		t.Fatalf("expected error type %q, got %q", "not_found_error", body.Error.Type)
	}
	if !strings.Contains(body.Error.Message, "claude-sonnet-4") {
		t.Fatalf("expected message to mention model, got %q", body.Error.Message)
	}
}

func TestRouterV1MessagesMapsInsufficientBalance(t *testing.T) {
	service := &fakeMessagesService{err: failure.New(failure.CodeLedgerInsufficientBalance)}
	handler := newMessagesTestRouter(newMessagesAuthenticator(), service, nil)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, newMessagesRequest(encodeMessageBody(t, false)))

	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("expected status %d, got %d", http.StatusPaymentRequired, rec.Code)
	}

	body := decodeAnthropicError(t, rec.Body)
	if body.Error.Type != "invalid_request_error" {
		t.Fatalf("expected error type %q, got %q", "invalid_request_error", body.Error.Type)
	}
}

func TestRouterV1MessagesStreamWritesSSE(t *testing.T) {
	service := &fakeMessagesService{}
	handler := newMessagesTestRouter(newMessagesAuthenticator(), service, nil)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, newMessagesRequest(encodeMessageBody(t, true)))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	if !service.streamCalled {
		t.Fatal("expected StreamMessage to be called")
	}
	if service.req.Stream == nil || !*service.req.Stream {
		t.Fatalf("expected service to receive stream=true, got %#v", service.req.Stream)
	}

	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("expected Content-Type %q, got %q", "text/event-stream", ct)
	}

	gotBody := rec.Body.String()
	for _, want := range []string{"event: message_start", "event: content_block_delta", "event: message_stop", "data: "} {
		if !strings.Contains(gotBody, want) {
			t.Fatalf("expected body to contain %q, got %q", want, gotBody)
		}
	}
}

func TestRouterV1MessagesStreamErrorBeforeFirstChunk(t *testing.T) {
	service := &fakeMessagesService{err: routing.ErrNoAvailableChannel}
	handler := newMessagesTestRouter(newMessagesAuthenticator(), service, nil)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, newMessagesRequest(encodeMessageBody(t, true)))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status %d, got %d", http.StatusServiceUnavailable, rec.Code)
	}

	if ct := rec.Header().Get("Content-Type"); ct == "text/event-stream" {
		t.Fatalf("expected JSON error before first chunk, got SSE Content-Type %q", ct)
	}

	gotBody := rec.Body.String()
	if strings.Contains(gotBody, "data:") || strings.Contains(gotBody, "event:") {
		t.Fatalf("expected non-SSE error body, got %q", gotBody)
	}

	body := decodeAnthropicError(t, rec.Body)
	if body.Error.Type != "api_error" {
		t.Fatalf("expected error type %q, got %q", "api_error", body.Error.Type)
	}
}

func TestRouterV1MessagesStreamWritesSSEErrorAfterChunkStarted(t *testing.T) {
	startData, _ := json.Marshal(StreamMessageStart{Type: "message_start", Message: *defaultMessageResponse("claude-sonnet-4")})
	service := &fakeMessagesService{
		streamFrames:       []StreamFrame{{EventType: "message_start", Data: startData}},
		streamErrAfterEmit: routing.ErrNoAvailableChannel,
	}
	handler := newMessagesTestRouter(newMessagesAuthenticator(), service, nil)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, newMessagesRequest(encodeMessageBody(t, true)))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d (header sent with first chunk), got %d", http.StatusOK, rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("expected Content-Type %q, got %q", "text/event-stream", ct)
	}

	gotBody := rec.Body.String()
	if !strings.Contains(gotBody, "event: message_start") {
		t.Fatalf("expected first chunk written, got %q", gotBody)
	}
	if !strings.Contains(gotBody, "event: error") {
		t.Fatalf("expected SSE error event after chunk started, got %q", gotBody)
	}
	if strings.Contains(gotBody, "event: message_stop") {
		t.Fatalf("expected no message_stop after stream error, got %q", gotBody)
	}
}
