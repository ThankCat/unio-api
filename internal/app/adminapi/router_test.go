package adminapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi"
	"github.com/ThankCat/unio-gateway/internal/core/adminauth"
)

const testAdminToken = "s3cret-admin-token"

// stubSessions 是内存版会话存储：只认 testAdminToken，用于 handler 层测试。
// 真实的 Redis 会话行为由 platform/adminsession 自己的测试覆盖。
type stubSessions struct{ err error }

func (s stubSessions) Validate(_ context.Context, token string) (bool, error) {
	if s.err != nil {
		return false, s.err
	}
	return token == testAdminToken, nil
}

func (s stubSessions) Issue(_ context.Context) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	return testAdminToken, nil
}

func (s stubSessions) Revoke(_ context.Context, _ string) error { return s.err }

// newTestAdminAuthenticator 返回基于内存会话的认证器，签名与旧的静态 token 构造器一致，
// 便于各 handler 测试沿用 `authenticator, err := ...` 的写法。
func newTestAdminAuthenticator() (*adminauth.SessionAuthenticator, error) {
	return adminauth.NewSessionAuthenticator(stubSessions{})
}

func newTestRouter(t *testing.T) http.Handler {
	t.Helper()

	authenticator, err := newTestAdminAuthenticator()
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}

	return adminapi.NewRouter(adminapi.RouterDeps{
		Logger:             zap.NewNop(),
		AdminAuthenticator: authenticator,
	})
}

func TestPingRequiresToken(t *testing.T) {
	handler := newTestRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/ping", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestPingRejectsInvalidToken(t *testing.T) {
	handler := newTestRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/ping", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestPingAcceptsValidToken(t *testing.T) {
	handler := newTestRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/ping", nil)
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
