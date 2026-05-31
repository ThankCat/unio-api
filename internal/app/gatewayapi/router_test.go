package gatewayapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ThankCat/unio-api/internal/app/gatewayapi/middleware"
	"github.com/ThankCat/unio-api/internal/core/auth"
	"github.com/ThankCat/unio-api/internal/core/modelcatalog"
	"github.com/ThankCat/unio-api/internal/platform/httpx"
	"github.com/ThankCat/unio-api/internal/platform/ratelimit"
)

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

// routerTestRateLimiter 是 router 测试使用的限流器替身。
type routerTestRateLimiter struct {
	subject  string
	limit    int64
	window   time.Duration
	decision ratelimit.Decision
	err      error
}

// routerTestModelCatalogService 是 router 测试使用的模型目录 service 替身。
type routerTestModelCatalogService struct {
	called    bool
	projectID int64
	models    []modelcatalog.Model
	err       error
}

// ListAvailableModels 记录收到的 project id，并返回测试预设的模型列表。
func (s *routerTestModelCatalogService) ListAvailableModels(ctx context.Context, projectID int64) ([]modelcatalog.Model, error) {
	s.called = true
	s.projectID = projectID
	return s.models, s.err
}

// Allow 记录收到的限流参数，并返回测试预设的限流判断结果。
func (l *routerTestRateLimiter) Allow(ctx context.Context, subject string, limit int64, window time.Duration) (ratelimit.Decision, error) {
	l.subject = subject
	l.limit = limit
	l.window = window
	return l.decision, l.err
}

// routerTestChatCompletionService 是 router 测试使用的 chat completion service 替身。
type routerTestChatCompletionService struct{}

// CreateChatCompletion 返回固定响应，避免 router 测试依赖 gateway/provider 组合。
func (s *routerTestChatCompletionService) CreateChatCompletion(ctx context.Context, req ChatCompletionRequest) (*ChatCompletionResponse, error) {
	return &ChatCompletionResponse{
		ID:      "chatcmpl_test",
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   req.Model,
		Choices: []ChatCompletionChoice{
			{
				Index: 0,
				Message: ChatMessage{
					Role:    "assistant",
					Content: jsonContent("mock response"),
				},
				FinishReason: "stop",
			},
		},
		Usage: ChatCompletionUsage{},
	}, nil
}

// StreamChatCompletion 发出固定流式响应，避免 router 测试依赖 gateway/adapter 组合。
func (s *routerTestChatCompletionService) StreamChatCompletion(ctx context.Context, req ChatCompletionRequest, emit func(ChatCompletionStreamResponse) error) error {
	return emit(ChatCompletionStreamResponse{
		ID:      "chatcmpl_mock",
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   req.Model,
		Choices: []ChatCompletionStreamChoice{
			{
				Index: 0,
				Delta: ChatCompletionStreamDelta{
					Role:    "assistant",
					Content: "mock response",
				},
				FinishReason: nil,
			},
		},
	})
}

// newTestRouter 创建带默认测试依赖的 router。
func newTestRouter(authenticator middleware.APIKeyAuthenticator, chatService ChatCompletionService, limiter middleware.RateLimiter, modelCatalogServices ...ModelCatalogService) http.Handler {
	if chatService == nil {
		chatService = &routerTestChatCompletionService{}
	}

	if limiter == nil {
		limiter = newAllowingRateLimiter()
	}

	modelCatalogService := ModelCatalogService(&routerTestModelCatalogService{})
	if len(modelCatalogServices) > 0 && modelCatalogServices[0] != nil {
		modelCatalogService = modelCatalogServices[0]
	}

	return NewRouter(RouterDeps{
		Logger:                slog.New(slog.NewTextHandler(io.Discard, nil)),
		APIKeyAuthenticator:   authenticator,
		RateLimiter:           limiter,
		RateLimitLimit:        60,
		RateLimitWindow:       time.Minute,
		ChatCompletionService: chatService,
		ModelCatalogService:   modelCatalogService,
	})
}

// newAllowingRateLimiter 创建默认允许请求通过的测试限流器。
func newAllowingRateLimiter() *routerTestRateLimiter {
	return &routerTestRateLimiter{
		decision: ratelimit.Decision{
			Allowed:   true,
			Limit:     60,
			Remaining: 59,
			ResetAt:   time.Date(2026, 5, 8, 10, 1, 0, 0, time.UTC),
		},
	}
}

func TestRouterHealthz(t *testing.T) {
	authenticator := &routerTestAPIKeyAuthenticator{
		principal: &auth.APIKeyPrincipal{
			APIKeyID:  1,
			ProjectID: 1,
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

func TestRouterNotFound(t *testing.T) {
	authenticator := &routerTestAPIKeyAuthenticator{
		principal: &auth.APIKeyPrincipal{
			APIKeyID:  1,
			ProjectID: 1,
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

func TestRouterMethodNotAllowed(t *testing.T) {
	authenticator := &routerTestAPIKeyAuthenticator{
		principal: &auth.APIKeyPrincipal{
			APIKeyID:  1,
			ProjectID: 1,
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

func TestRouterRequestID(t *testing.T) {
	authenticator := &routerTestAPIKeyAuthenticator{
		principal: &auth.APIKeyPrincipal{
			APIKeyID:  1,
			ProjectID: 1,
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
