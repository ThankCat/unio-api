package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ThankCat/unio-api/internal/auth"
	"github.com/ThankCat/unio-api/internal/httpx"
)

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
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
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
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
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
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
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
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
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
