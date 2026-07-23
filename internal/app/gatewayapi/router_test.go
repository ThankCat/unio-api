package gatewayapi

import (
	"context"
	"encoding/json"
	"go.uber.org/zap"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/app/gatewayapi/middleware"
	gatewaychat "github.com/ThankCat/unio-gateway/internal/app/gatewayapi/openai/chatcompletions"
	gatewaymodels "github.com/ThankCat/unio-gateway/internal/app/gatewayapi/openai/models"
	"github.com/ThankCat/unio-gateway/internal/core/auth"
	"github.com/ThankCat/unio-gateway/internal/core/modelcatalog"
	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/lifecycle"
)

// jsonContent 是 router 测试构造 OpenAI message content 的辅助函数。
func jsonContent(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}

// routerTestAPIKeyAuthenticator 是 router 通用测试使用的 API Key 认证器。
type routerTestAPIKeyAuthenticator struct {
	principal *auth.APIKeyPrincipal
	err       error
	token     string
}

// AuthenticateAPIKey 记录收到的 token，并返回测试预设的认证结果。
func (a *routerTestAPIKeyAuthenticator) AuthenticateAPIKey(ctx context.Context, plaintext string) (*auth.APIKeyPrincipal, error) {
	a.token = plaintext
	return a.principal, a.err
}

// routerTestModelCatalogService 是 router 测试使用的模型目录 service 替身。
type routerTestModelCatalogService struct {
	called    bool
	projectID int64
	routeID   int64
	models    []modelcatalog.Model
	err       error
}

// ListAvailableModels 记录收到的 project id，并返回测试预设的模型列表。
func (s *routerTestModelCatalogService) ListAvailableModels(ctx context.Context, projectID, routeID int64, _ []string) ([]modelcatalog.Model, error) {
	s.called = true
	s.projectID = projectID
	s.routeID = routeID
	return s.models, s.err
}

// routerTestChatCompletionService 是 router 测试使用的 chat completion service 替身。
type routerTestChatCompletionService struct{}

// CreateChatCompletion 返回固定响应，避免 router 测试依赖 gateway/provider 组合。
func (s *routerTestChatCompletionService) CreateChatCompletion(ctx context.Context, req gatewaychat.ChatCompletionRequest) (*lifecycle.NonStreamResult[*gatewaychat.ChatCompletionResponse], error) {
	resp := &gatewaychat.ChatCompletionResponse{
		ID:      "chatcmpl_test",
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   req.Model,
		Choices: []gatewaychat.ChatCompletionChoice{
			{
				Index: 0,
				Message: gatewaychat.ChatMessage{
					Role:    "assistant",
					Content: jsonContent("mock response"),
				},
				FinishReason: "stop",
			},
		},
		Usage: gatewaychat.ChatCompletionUsage{},
	}
	return lifecycle.NewNonStreamResult(resp, lifecycle.NewDeliveryFinalizer(func() {}, func() {})), nil
}

// StreamChatCompletion 发出固定流式响应，避免 router 测试依赖 gateway/adapter 组合。
func (s *routerTestChatCompletionService) StreamChatCompletion(ctx context.Context, req gatewaychat.ChatCompletionRequest, emit func(gatewaychat.ChatCompletionStreamResponse) error) error {
	return emit(gatewaychat.ChatCompletionStreamResponse{
		ID:      "chatcmpl_mock",
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   req.Model,
		Choices: []gatewaychat.ChatCompletionStreamChoice{
			{
				Index: 0,
				Delta: gatewaychat.ChatCompletionStreamDelta{
					Role:    "assistant",
					Content: "mock response",
				},
				FinishReason: nil,
			},
		},
	})
}

// newTestRouter 创建带默认测试依赖的 router。
func newTestRouter(authenticator middleware.APIKeyAuthenticator, chatService gatewaychat.ChatCompletionService, _ any, modelCatalogServices ...gatewaymodels.ModelCatalogService) http.Handler {
	if chatService == nil {
		chatService = &routerTestChatCompletionService{}
	}

	modelCatalogService := gatewaymodels.ModelCatalogService(&routerTestModelCatalogService{})
	if len(modelCatalogServices) > 0 && modelCatalogServices[0] != nil {
		modelCatalogService = modelCatalogServices[0]
	}

	return NewRouter(RouterDeps{
		Logger:                zap.NewNop(),
		APIKeyAuthenticator:   authenticator,
		ChatCompletionService: chatService,
		ModelCatalogService:   modelCatalogService,
	})
}

func TestRouterHealthz(t *testing.T) {
	authenticator := &routerTestAPIKeyAuthenticator{
		principal: &auth.APIKeyPrincipal{
			APIKeyID:  1,
			UserID:    1,
			KeyPrefix: "unio_sk_test",
		},
	}
	handle := newTestRouter(authenticator, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	handle.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	var body struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response body: %v", err)
	}

	if body.Status != "ok" {
		t.Fatalf("expected status body %q, got %q", "ok", body.Status)
	}
}

type readinessProbeStub struct {
	ready  bool
	reason string
}

func (p readinessProbeStub) Check(context.Context) (bool, string) { return p.ready, p.reason }

func TestRouterReadyzIsDynamicAndKeepsHealthzLive(t *testing.T) {
	handler := NewRouter(RouterDeps{Logger: zap.NewNop(), Readiness: readinessProbeStub{ready: false, reason: "redis_unavailable"}})

	readyResponse := httptest.NewRecorder()
	handler.ServeHTTP(readyResponse, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if readyResponse.Code != http.StatusServiceUnavailable || readyResponse.Body.String() != "{\"status\":\"not_ready\"}\n" {
		t.Fatalf("unexpected not-ready response: code=%d body=%q", readyResponse.Code, readyResponse.Body.String())
	}

	healthResponse := httptest.NewRecorder()
	handler.ServeHTTP(healthResponse, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if healthResponse.Code != http.StatusOK {
		t.Fatalf("liveness must remain healthy, got %d", healthResponse.Code)
	}

	handler = NewRouter(RouterDeps{Logger: zap.NewNop(), Readiness: readinessProbeStub{ready: true, reason: "ready"}})
	readyResponse = httptest.NewRecorder()
	handler.ServeHTTP(readyResponse, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if readyResponse.Code != http.StatusOK || readyResponse.Body.String() != "{\"status\":\"ready\"}\n" {
		t.Fatalf("unexpected ready response: code=%d body=%q", readyResponse.Code, readyResponse.Body.String())
	}
}

func TestRouterNotFound(t *testing.T) {
	authenticator := &routerTestAPIKeyAuthenticator{
		principal: &auth.APIKeyPrincipal{
			APIKeyID:  1,
			UserID:    1,
			KeyPrefix: "unio_sk_test",
		},
	}
	handle := newTestRouter(authenticator, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/not-found", nil)
	rec := httptest.NewRecorder()

	handle.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, rec.Code)
	}

	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}

	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response body: %v", err)
	}

	if body.Error.Code != "not_found" {
		t.Fatalf("expected error code %q, got %q", "not_found", body.Error.Code)
	}

	if body.Error.Message != "route not found" {
		t.Fatalf("expected error message %q, got %q", "route not found", body.Error.Message)
	}
}

func TestRouterLegacyCircuitBreakerEndpointIsGone(t *testing.T) {
	authenticator := &routerTestAPIKeyAuthenticator{
		principal: &auth.APIKeyPrincipal{APIKeyID: 1, UserID: 1, KeyPrefix: "unio_sk_test"},
	}
	handle := newTestRouter(authenticator, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/internal/v1/circuit-breaker", nil)
	rec := httptest.NewRecorder()
	handle.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("legacy circuit-breaker endpoint status = %d, want 404", rec.Code)
	}
	if code := decodeRouterError(t, rec); code != "not_found" {
		t.Fatalf("legacy circuit-breaker endpoint error = %q, want not_found", code)
	}
}

func TestRouterMethodNotAllowed(t *testing.T) {
	authenticator := &routerTestAPIKeyAuthenticator{
		principal: &auth.APIKeyPrincipal{
			APIKeyID:  1,
			UserID:    1,
			KeyPrefix: "unio_sk_test",
		},
	}
	handle := newTestRouter(authenticator, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/healthz", nil)
	rec := httptest.NewRecorder()

	handle.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status %d, got %d", http.StatusMethodNotAllowed, rec.Code)
	}

	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response body: %v", err)
	}

	if body.Error.Code != "method_not_allowed" {
		t.Fatalf("expected error code %q, got %q", "method_not_allowed", body.Error.Code)
	}

	if body.Error.Message != "method not allowed" {
		t.Fatalf("expected error message %q, got %q", "method not allowed", body.Error.Message)
	}
}

// routerWithPrincipal 创建一个认证通过的测试 router，用于验证 /responses* 路由注册。
func routerWithPrincipal() http.Handler {
	authenticator := &routerTestAPIKeyAuthenticator{
		principal: &auth.APIKeyPrincipal{APIKeyID: 1, UserID: 1, KeyPrefix: "unio_sk_test"},
	}
	return newTestRouter(authenticator, nil, nil)
}

// decodeRouterError 解析 router 错误响应的 code 字段。
func decodeRouterError(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body %q: %v", rec.Body.String(), err)
	}
	return body.Error.Code
}

func TestRouterResponsesStatelessUnsupported(t *testing.T) {
	handle := routerWithPrincipal()

	// 有状态 endpoint 全部 501 unsupported_endpoint_stateless（无服务端存储）。
	cases := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/v1/responses/resp_123"},
		{http.MethodDelete, "/v1/responses/resp_123"},
		{http.MethodGet, "/v1/responses/resp_123/input_items"},
		{http.MethodPost, "/v1/responses/resp_123/cancel"},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.Header.Set("Authorization", "Bearer unio_sk_test")
		rec := httptest.NewRecorder()
		handle.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotImplemented {
			t.Fatalf("%s %s: expected 501, got %d", tc.method, tc.path, rec.Code)
		}
		if code := decodeRouterError(t, rec); code != "unsupported_endpoint_stateless" {
			t.Fatalf("%s %s: expected unsupported_endpoint_stateless, got %q", tc.method, tc.path, code)
		}
	}
}

func TestRouterResponsesBackgroundRejected(t *testing.T) {
	handle := routerWithPrincipal()

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"m","input":"hi","background":true}`))
	req.Header.Set("Authorization", "Bearer unio_sk_test")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handle.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	if code := decodeRouterError(t, rec); code != "unsupported_background" {
		t.Fatalf("expected unsupported_background, got %q", code)
	}
}

func TestRouterResponsesCompactAndInputTokensRegistered(t *testing.T) {
	handle := routerWithPrincipal()

	// 路由已注册：非法 body 在 handler 内校验失败返回 400（不触达 nil service）。
	for _, path := range []string{"/v1/responses/compact", "/v1/responses/input_tokens"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":""}`))
		req.Header.Set("Authorization", "Bearer unio_sk_test")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handle.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: expected 400 (route registered, validation reached), got %d body=%q", path, rec.Code, rec.Body.String())
		}
		if code := decodeRouterError(t, rec); code != "invalid_request" {
			t.Fatalf("%s: expected invalid_request, got %q", path, code)
		}
	}
}

func TestRouterRequestID(t *testing.T) {
	authenticator := &routerTestAPIKeyAuthenticator{
		principal: &auth.APIKeyPrincipal{
			APIKeyID:  1,
			UserID:    1,
			KeyPrefix: "unio_sk_test",
		},
	}
	handle := newTestRouter(authenticator, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	handle.ServeHTTP(rec, req)

	requestID := rec.Header().Get(httpx.HeaderRequestID)
	if requestID == "" {
		t.Fatalf("expected request id in context")
	}
}
