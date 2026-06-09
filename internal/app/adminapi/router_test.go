package adminapi_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ThankCat/unio-api/internal/app/adminapi"
	"github.com/ThankCat/unio-api/internal/core/adminauth"
)

const testAdminToken = "s3cret-admin-token"

func newTestRouter(t *testing.T) http.Handler {
	t.Helper()

	authenticator, err := adminauth.NewStaticTokenAuthenticator(testAdminToken)
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}

	return adminapi.NewRouter(adminapi.RouterDeps{
		Logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		AdminAuthenticator: authenticator,
	})
}

func TestPingRequiresToken(t *testing.T) {
	handler := newTestRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/admin/v1/ping", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestPingRejectsInvalidToken(t *testing.T) {
	handler := newTestRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/admin/v1/ping", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestPingAcceptsValidToken(t *testing.T) {
	handler := newTestRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/admin/v1/ping", nil)
	req.Header.Set("Authorization", "Bearer "+testAdminToken)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, rec.Code)
	}
}

func TestHealthzSkipsAuth(t *testing.T) {
	handler := newTestRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, rec.Code)
	}
}
