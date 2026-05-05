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

var logger = slog.New(slog.NewTextHandler(io.Discard, nil))

type fakeAPIKeyAuthenticator struct {
	principal *auth.APIKeyPrincipal
	err       error
	token     string
}

func (a *fakeAPIKeyAuthenticator) AuthenticateAPIKey(ctx context.Context, plaintext string) (*auth.APIKeyPrincipal, error) {
	a.token = plaintext
	return a.principal, a.err
}

func TestRouterHealthz(t *testing.T) {
	handle := NewRouter(RouterDeps{
		Logger: logger,
		APIKeyAuthenticator: &fakeAPIKeyAuthenticator{
			principal: &auth.APIKeyPrincipal{
				APIKeyID:  1,
				ProjectID: 1,
				KeyPrefix: "unio_sk_test",
			},
		},
	})
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	handle.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	var body struct {
		Status string `json:"status"`
	}

	// 两种写法都可以
	// if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
	// 	t.Fatalf("decode response body: %v", err)
	// }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response body: %v", err)
	}

	if body.Status != "ok" {
		t.Fatalf("expected status body %q, got %q", "ok", body.Status)
	}
}

func TestRouterNotFound(t *testing.T) {
	handle := NewRouter(RouterDeps{
		Logger: logger,
		APIKeyAuthenticator: &fakeAPIKeyAuthenticator{
			principal: &auth.APIKeyPrincipal{
				APIKeyID:  1,
				ProjectID: 1,
				KeyPrefix: "unio_sk_test",
			},
		},
	})
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
	handle := NewRouter(RouterDeps{
		Logger: logger,
		APIKeyAuthenticator: &fakeAPIKeyAuthenticator{
			principal: &auth.APIKeyPrincipal{
				APIKeyID:  1,
				ProjectID: 1,
				KeyPrefix: "unio_sk_test",
			},
		},
	})
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
	handle := NewRouter(RouterDeps{
		Logger: logger,
		APIKeyAuthenticator: &fakeAPIKeyAuthenticator{
			principal: &auth.APIKeyPrincipal{
				APIKeyID:  1,
				ProjectID: 1,
				KeyPrefix: "unio_sk_test",
			},
		},
	})
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	handle.ServeHTTP(rec, req)

	requestID := rec.Header().Get(httpx.HeaderRequestID)
	if requestID == "" {
		t.Fatalf("expected request id in context")
	}
}

func TestRouterModelsSuccess(t *testing.T) {
	authenticator := &fakeAPIKeyAuthenticator{
		principal: &auth.APIKeyPrincipal{
			APIKeyID:  1,
			ProjectID: 1,
			KeyPrefix: "unio_sk_test",
		},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handle := NewRouter(RouterDeps{
		Logger:              logger,
		APIKeyAuthenticator: authenticator,
	})
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer unio_sk_test")
	rec := httptest.NewRecorder()
	handle.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	if authenticator.token != "unio_sk_test" {
		t.Fatalf("expected token %q, got %q", "unio_sk_test", authenticator.token)
	}
}

func TestRouterModelsRequiresAPIKey(t *testing.T) {
	authenticator := &fakeAPIKeyAuthenticator{
		principal: &auth.APIKeyPrincipal{
			APIKeyID:  1,
			ProjectID: 1,
			KeyPrefix: "unio_sk_test",
		},
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := NewRouter(RouterDeps{
		APIKeyAuthenticator: authenticator,
		Logger:              logger,
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}

	if authenticator.token != "" {
		t.Fatalf("expected authenticator not to receive token, got %q", authenticator.token)
	}
}

func TestRouterV1ChatCompletionWithMissingAPIKey(t *testing.T) {
	authenticator := &fakeAPIKeyAuthenticator{principal: &auth.APIKeyPrincipal{
		APIKeyID:  1,
		ProjectID: 1,
		KeyPrefix: "unio_sk_test",
	}}

	handler := NewRouter(RouterDeps{
		Logger:              logger,
		APIKeyAuthenticator: authenticator,
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestRouterV1ChatCompletionWithAPIKey(t *testing.T) {
	authenticator := &fakeAPIKeyAuthenticator{principal: &auth.APIKeyPrincipal{
		APIKeyID:  1,
		ProjectID: 1,
		KeyPrefix: "unio_sk_test",
	}}

	handler := NewRouter(RouterDeps{
		Logger:              logger,
		APIKeyAuthenticator: authenticator,
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
}

func TestRouterV1ChatCompletionWithInvalidBody(t *testing.T) {
	authenticator := &fakeAPIKeyAuthenticator{principal: &auth.APIKeyPrincipal{
		APIKeyID:  1,
		ProjectID: 1,
		KeyPrefix: "unio_sk_test",
	}}

	handler := NewRouter(RouterDeps{
		Logger:              logger,
		APIKeyAuthenticator: authenticator,
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{"))
	req.Header.Set("Authorization", "Bearer unio_sk_test")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}

	recBody := httpx.ErrorResponse{}
	if err := json.NewDecoder(rec.Body).Decode(&recBody); err != nil {
		t.Fatalf("decode response body: %v", err)
	}

	if recBody.Error.Code != "invalid_request" {
		t.Fatalf("expected error code %q, got %q", "invalid_request", recBody.Error.Code)
	}
	if recBody.Error.Message != "invalid json body" {
		t.Fatalf("expected message %q, got %q", "invalid json body", recBody.Error.Message)
	}

}

func TestRouterV1ChatCompletionWithMissingModel(t *testing.T) {
	authenticator := &fakeAPIKeyAuthenticator{principal: &auth.APIKeyPrincipal{
		APIKeyID:  1,
		ProjectID: 1,
		KeyPrefix: "unio_sk_test",
	}}
	handler := NewRouter(RouterDeps{
		Logger:              logger,
		APIKeyAuthenticator: authenticator,
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

	recBody := httpx.ErrorResponse{}
	if err := json.NewDecoder(rec.Body).Decode(&recBody); err != nil {
		t.Fatalf("decode response body: %v", err)
	}

	if recBody.Error.Code != "invalid_request" {
		t.Fatalf("expected error code %q, got %q", "invalid_request", recBody.Error.Code)
	}

	if recBody.Error.Message != "model is required" {
		t.Fatalf("expected message %q, got %q", "model is required", recBody.Error.Message)
	}
}

func TestRouterV1ChatCompletionWithMissingMessage(t *testing.T) {
	authenticator := &fakeAPIKeyAuthenticator{principal: &auth.APIKeyPrincipal{APIKeyID: 1, ProjectID: 1, KeyPrefix: "unio_sk_test"}}
	handler := NewRouter(RouterDeps{
		Logger:              logger,
		APIKeyAuthenticator: authenticator,
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

	recBody := httpx.ErrorResponse{}
	if err := json.NewDecoder(rec.Body).Decode(&recBody); err != nil {
		t.Fatalf("decode response body: %v", err)
	}
	if recBody.Error.Code != "invalid_request" {
		t.Fatalf("expected error code %q, got %q", "invalid_request", recBody.Error.Code)
	}
	if recBody.Error.Message != "messages is required" {
		t.Fatalf("expected message %q, got %q", "messages is required", recBody.Error.Message)
	}
}
