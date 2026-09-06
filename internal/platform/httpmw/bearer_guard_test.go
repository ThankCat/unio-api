package httpmw

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequireBearerRejectsMissingOrWrongToken(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	guarded := RequireBearer("s3cret-token", next)

	for _, header := range []string{"", "Bearer wrong", "Basic s3cret-token", "s3cret-token", "Bearer s3cret-toke"} {
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		rec := httptest.NewRecorder()
		guarded.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("header %q: status = %d, want 401", header, rec.Code)
		}
		if rec.Header().Get("WWW-Authenticate") == "" {
			t.Fatalf("header %q: missing WWW-Authenticate", header)
		}
	}
}

func TestRequireBearerPassesMatchingToken(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer s3cret-token")
	rec := httptest.NewRecorder()
	RequireBearer("s3cret-token", next).ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
}

// 未配置 token 时不加防护：由部署层（反代屏蔽）兜底，行为与历史一致。
func TestRequireBearerWithoutTokenIsPassThrough(t *testing.T) {
	next := http.NewServeMux()
	if RequireBearer("", next) != http.Handler(next) {
		t.Fatal("empty token must return next unchanged")
	}
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	RequireBearer("", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 without any Authorization header", rec.Code)
	}
}
