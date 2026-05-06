package httpapi

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

	"github.com/ThankCat/unio-api/internal/auth"
	"github.com/ThankCat/unio-api/internal/httpx"
)

// fakeAPIKeyAuthenticator 是 chat completions 测试使用的 API Key 认证器。
type fakeAPIKeyAuthenticator struct {
	principal *auth.APIKeyPrincipal
	err       error
	token     string
}

// AuthenticateAPIKey 记录收到的 token，并返回测试预设的认证结果。
func (a *fakeAPIKeyAuthenticator) AuthenticateAPIKey(ctx context.Context, plaintext string) (*auth.APIKeyPrincipal, error) {
	a.token = plaintext
	return a.principal, a.err
}

func TestRouterV1ChatCompletionWithMissingAPIKey(t *testing.T) {
	authenticator := &fakeAPIKeyAuthenticator{
		principal: &auth.APIKeyPrincipal{
			APIKeyID:  1,
			ProjectID: 1,
			KeyPrefix: "unio_sk_test",
		},
	}
	handler := NewRouter(RouterDeps{
		Logger:                slog.New(slog.NewTextHandler(io.Discard, nil)),
		APIKeyAuthenticator:   authenticator,
		ChatCompletionService: NewMockChatCompletionService(),
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestRouterV1ChatCompletionWithAPIKey(t *testing.T) {
	authenticator := &fakeAPIKeyAuthenticator{
		principal: &auth.APIKeyPrincipal{
			APIKeyID:  1,
			ProjectID: 1,
			KeyPrefix: "unio_sk_test",
		},
	}
	handler := NewRouter(RouterDeps{
		Logger:                slog.New(slog.NewTextHandler(io.Discard, nil)),
		APIKeyAuthenticator:   authenticator,
		ChatCompletionService: NewMockChatCompletionService(),
	})

	reqBody := ChatCompletionRequest{
		Model: "openai/gpt-4.1",
		Messages: []ChatMessage{
			{Role: "user", Content: "Hello"},
		},
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
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	var body ChatCompletionResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response body: %v", err)
	}

	if body.Object != "chat.completion" {
		t.Fatalf("expected object %q, got %q", "chat.completion", body.Object)
	}

	if body.Model != reqBody.Model {
		t.Fatalf("expected model %q, got %q", reqBody.Model, body.Model)
	}

	if len(body.Choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(body.Choices))
	}
}

func TestRouterV1ChatCompletionWithInvalidBody(t *testing.T) {
	authenticator := &fakeAPIKeyAuthenticator{
		principal: &auth.APIKeyPrincipal{
			APIKeyID:  1,
			ProjectID: 1,
			KeyPrefix: "unio_sk_test",
		},
	}
	handler := NewRouter(RouterDeps{
		Logger:                slog.New(slog.NewTextHandler(io.Discard, nil)),
		APIKeyAuthenticator:   authenticator,
		ChatCompletionService: NewMockChatCompletionService(),
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{"))
	req.Header.Set("Authorization", "Bearer unio_sk_test")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}

	var body httpx.ErrorResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response body: %v", err)
	}

	if body.Error.Code != "invalid_request" {
		t.Fatalf("expected error code %q, got %q", "invalid_request", body.Error.Code)
	}

	if body.Error.Message != "invalid json body" {
		t.Fatalf("expected message %q, got %q", "invalid json body", body.Error.Message)
	}
}

func TestRouterV1ChatCompletionWithMissingModel(t *testing.T) {
	authenticator := &fakeAPIKeyAuthenticator{
		principal: &auth.APIKeyPrincipal{
			APIKeyID:  1,
			ProjectID: 1,
			KeyPrefix: "unio_sk_test",
		},
	}
	handler := NewRouter(RouterDeps{
		Logger:                slog.New(slog.NewTextHandler(io.Discard, nil)),
		APIKeyAuthenticator:   authenticator,
		ChatCompletionService: NewMockChatCompletionService(),
	})

	reqBody := ChatCompletionRequest{}

	buf := new(bytes.Buffer)
	if err := json.NewEncoder(buf).Encode(reqBody); err != nil {
		t.Fatalf("encode request body: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", buf)
	req.Header.Set("Authorization", "Bearer unio_sk_test")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}

	var body httpx.ErrorResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response body: %v", err)
	}

	if body.Error.Code != "invalid_request" {
		t.Fatalf("expected error code %q, got %q", "invalid_request", body.Error.Code)
	}

	if body.Error.Message != "model is required" {
		t.Fatalf("expected message %q, got %q", "model is required", body.Error.Message)
	}
}

func TestRouterV1ChatCompletionWithMissingMessages(t *testing.T) {
	authenticator := &fakeAPIKeyAuthenticator{
		principal: &auth.APIKeyPrincipal{
			APIKeyID:  1,
			ProjectID: 1,
			KeyPrefix: "unio_sk_test",
		},
	}
	handler := NewRouter(RouterDeps{
		Logger:                slog.New(slog.NewTextHandler(io.Discard, nil)),
		APIKeyAuthenticator:   authenticator,
		ChatCompletionService: NewMockChatCompletionService(),
	})

	reqBody := ChatCompletionRequest{
		Model:    "openai/gpt-4.1",
		Messages: []ChatMessage{},
	}

	buf := new(bytes.Buffer)
	if err := json.NewEncoder(buf).Encode(reqBody); err != nil {
		t.Fatalf("encode request body: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", buf)
	req.Header.Set("Authorization", "Bearer unio_sk_test")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}

	var body httpx.ErrorResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response body: %v", err)
	}

	if body.Error.Code != "invalid_request" {
		t.Fatalf("expected error code %q, got %q", "invalid_request", body.Error.Code)
	}

	if body.Error.Message != "messages is required" {
		t.Fatalf("expected message %q, got %q", "messages is required", body.Error.Message)
	}
}

// fakeChatCompletionService 是 chat completions 测试使用的 service 替身。
type fakeChatCompletionService struct {
	called bool
	req    ChatCompletionRequest
	resp   *ChatCompletionResponse
	err    error
}

// CreateChatCompletion 记录 handler 传入的请求，并返回测试预设的响应。
func (s *fakeChatCompletionService) CreateChatCompletion(ctx context.Context, req ChatCompletionRequest) (*ChatCompletionResponse, error) {
	s.called = true
	s.req = req
	return s.resp, s.err
}

func TestRouterV1ChatCompletionCallsService(t *testing.T) {
	authenticator := &fakeAPIKeyAuthenticator{
		principal: &auth.APIKeyPrincipal{
			APIKeyID:  1,
			ProjectID: 1,
			KeyPrefix: "unio_sk_test",
		},
	}

	service := &fakeChatCompletionService{
		resp: &ChatCompletionResponse{
			Object: "chat.completion",
			Model:  "openai/gpt-4.1",
			Choices: []ChatCompletionChoice{
				{
					Index: 0,
					Message: ChatMessage{
						Role:    "assistant",
						Content: "mock response",
					},
					FinishReason: "stop",
				},
			},
		},
	}

	handler := NewRouter(RouterDeps{
		Logger:                slog.New(slog.NewTextHandler(io.Discard, nil)),
		APIKeyAuthenticator:   authenticator,
		ChatCompletionService: service,
	})

	buf := new(bytes.Buffer)
	reqBody := ChatCompletionRequest{
		Model: "openai/gpt-4.1",
		Messages: []ChatMessage{
			{Role: "user", Content: "Hello"},
		},
	}
	if err := json.NewEncoder(buf).Encode(reqBody); err != nil {
		t.Fatalf("encode request body: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", buf)
	req.Header.Set("Authorization", "Bearer unio_sk_test")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	if !service.called {
		t.Fatal("expected chat completion service to be called")
	}

	if service.req.Model != reqBody.Model {
		t.Fatalf("expected service model %q, got %q", reqBody.Model, service.req.Model)
	}

	if len(service.req.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(service.req.Messages))
	}

	var recBody ChatCompletionResponse
	if err := json.NewDecoder(rec.Body).Decode(&recBody); err != nil {
		t.Fatalf("decode response body: %v", err)
	}

	if recBody.Model != service.resp.Model {
		t.Fatalf("expected response model %q, got %q", service.resp.Model, recBody.Model)
	}
}
