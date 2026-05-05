package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRouterModelsRequiresAPIKey(t *testing.T) {
	authenticator := newSuccessfulAuthenticator()
	handler := newTestRouter(authenticator)

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

func TestRouterModelsSuccess(t *testing.T) {
	authenticator := newSuccessfulAuthenticator()
	handler := newTestRouter(authenticator)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer unio_sk_test")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	if authenticator.token != "unio_sk_test" {
		t.Fatalf("expected token %q, got %q", "unio_sk_test", authenticator.token)
	}

	var body struct {
		Object string `json:"object"`
		Data   []any  `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response body: %v", err)
	}

	if body.Object != "list" {
		t.Fatalf("expected object %q, got %q", "list", body.Object)
	}

	if len(body.Data) != 0 {
		t.Fatalf("expected empty data, got %d items", len(body.Data))
	}
}
