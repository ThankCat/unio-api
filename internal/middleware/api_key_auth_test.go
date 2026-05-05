package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ThankCat/unio-api/internal/auth"
)

// fakeAPIKeyAuthenticator 是 middleware 测试使用的认证器替身。
type fakeAPIKeyAuthenticator struct {
	principal *auth.APIKeyPrincipal
	err       error
	token     string
}

// AuthenticateAPIKey 记录收到的明文 token，并返回测试预设的认证结果。
func (a *fakeAPIKeyAuthenticator) AuthenticateAPIKey(rctx context.Context, plaintext string) (*auth.APIKeyPrincipal, error) {
	a.token = plaintext
	return a.principal, a.err
}

// missing Authorization -> 401
func TestAPIKeyAuthMissingAuthorization(t *testing.T) {
	authenticator := &fakeAPIKeyAuthenticator{}

	nextCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
	})

	handler := APIKeyAuth(authenticator)(next)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}

	if nextCalled {
		t.Fatal("expected next handler not to be called")
	}

	if authenticator.token != "" {
		t.Fatalf("expected authenticator not to receive token, got %q", authenticator.token)
	}
}

func TestAPIKeyAuthAuthenticatorError(t *testing.T) {
	authenticator := &fakeAPIKeyAuthenticator{err: auth.ErrInvalidAPIKey}

	nextCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
	})

	handler := APIKeyAuth(authenticator)(next)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer unio_sk_test")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if authenticator.token != "unio_sk_test" {
		t.Fatalf("expected token %q, got %q", "unio_sk_test", authenticator.token)
	}

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}

	if nextCalled {
		t.Fatal("expected next handler not to be called")
	}
}

func TestAPIKeyAuthSuccess(t *testing.T) {
	expectedPrincipal := &auth.APIKeyPrincipal{
		APIKeyID:  1,
		ProjectID: 1,
		KeyPrefix: "unio_sk_XhE8wL5D",
	}

	authenticator := &fakeAPIKeyAuthenticator{
		principal: expectedPrincipal,
	}

	nextCalled := false
	var gotPrincipal *auth.APIKeyPrincipal
	var gotOK bool

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		gotPrincipal, gotOK = auth.APIKeyPrincipalFromContext(r.Context())
		w.WriteHeader(http.StatusNoContent)
	})

	handler := APIKeyAuth(authenticator)(next)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer unio_sk_test")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if authenticator.token != "unio_sk_test" {
		t.Fatalf("expected token %q, got %q", "unio_sk_test", authenticator.token)
	}

	if !nextCalled {
		t.Fatal("expected next handler to be called")
	}

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d", http.StatusNoContent, rec.Code)
	}

	if !gotOK {
		t.Fatal("expected principal in context")
	}

	if gotPrincipal != expectedPrincipal {
		t.Fatal("expected principal from context to match authenticator principal")
	}
}
