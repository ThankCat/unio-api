package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ThankCat/unio-api/internal/httpx"
)

func TestRouterV1ChatCompletionWithMissingAPIKey(t *testing.T) {
	authenticator := newSuccessfulAuthenticator()
	handler := newTestRouter(authenticator)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestRouterV1ChatCompletionWithAPIKey(t *testing.T) {
	authenticator := newSuccessfulAuthenticator()
	handler := newTestRouter(authenticator)

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
	authenticator := newSuccessfulAuthenticator()
	handler := newTestRouter(authenticator)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{"))
	req.Header.Set("Authorization", "Bearer unio_sk_test")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assertErrorResponse(t, rec, http.StatusBadRequest, "invalid_request", "invalid json body")
}

func TestRouterV1ChatCompletionWithMissingModel(t *testing.T) {
	authenticator := newSuccessfulAuthenticator()
	handler := newTestRouter(authenticator)

	reqBody := ChatCompletionRequest{}

	buf := new(bytes.Buffer)
	if err := json.NewEncoder(buf).Encode(reqBody); err != nil {
		t.Fatalf("encode request body: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", buf)
	req.Header.Set("Authorization", "Bearer unio_sk_test")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assertErrorResponse(t, rec, http.StatusBadRequest, "invalid_request", "model is required")
}

func TestRouterV1ChatCompletionWithMissingMessages(t *testing.T) {
	authenticator := newSuccessfulAuthenticator()
	handler := newTestRouter(authenticator)

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

	assertErrorResponse(t, rec, http.StatusBadRequest, "invalid_request", "messages is required")
}

// assertErrorResponse 校验统一 JSON 错误响应。
func assertErrorResponse(t *testing.T, rec *httptest.ResponseRecorder, status int, code string, message string) {
	t.Helper()

	if rec.Code != status {
		t.Fatalf("expected status %d, got %d", status, rec.Code)
	}

	var body httpx.ErrorResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response body: %v", err)
	}

	if body.Error.Code != code {
		t.Fatalf("expected error code %q, got %q", code, body.Error.Code)
	}

	if body.Error.Message != message {
		t.Fatalf("expected message %q, got %q", message, body.Error.Message)
	}
}
