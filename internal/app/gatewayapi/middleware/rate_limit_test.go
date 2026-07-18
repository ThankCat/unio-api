package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/core/auth"
	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
	"github.com/ThankCat/unio-gateway/internal/platform/ratelimit"
)

func i64(v int64) *int64 { return &v }

// fakeKeyRateLimiter 是 RateLimit middleware 测试使用的「线路+用户」级限流器替身。
type fakeKeyRateLimiter struct {
	routeID  int64
	userID   int64
	limits   ratelimit.Limits
	decision ratelimit.Decision
	err      error
	called   bool
}

func (l *fakeKeyRateLimiter) AllowRouteUserRequest(_ context.Context, routeID, userID int64, limits ratelimit.Limits) (ratelimit.Decision, error) {
	l.called = true
	l.routeID = routeID
	l.userID = userID
	l.limits = limits
	return l.decision, l.err
}

func TestRateLimitAllowsRequest(t *testing.T) {
	resetAt := time.Date(2026, 5, 8, 10, 1, 0, 0, time.UTC)
	limiter := &fakeKeyRateLimiter{
		decision: ratelimit.Decision{
			Allowed:   true,
			Limit:     60,
			Remaining: 59,
			ResetAt:   resetAt,
		},
	}

	nextCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusNoContent)
	})

	handler := RateLimit(limiter, RateLimitOptions{})(next)
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req = req.WithContext(auth.ContextWithAPIKeyPrincipal(req.Context(), &auth.APIKeyPrincipal{
		APIKeyID:  123,
		UserID:    456,
		RouteID:   i64(9),
		KeyPrefix: "unio_sk_test",
	}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if !nextCalled {
		t.Fatal("want next handler to be called")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("want status %d, got %d", http.StatusNoContent, rec.Code)
	}
	if limiter.routeID != 9 || limiter.userID != 456 {
		t.Fatalf("want route 9 / user 456, got route %d / user %d", limiter.routeID, limiter.userID)
	}
	assertRateLimitHeaders(t, rec, "60", "59", resetAt)
}

func TestRateLimitForwardsPerKeyLimits(t *testing.T) {
	limiter := &fakeKeyRateLimiter{decision: ratelimit.Decision{Allowed: true, Limit: 10, Remaining: 9}}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })

	rpm := int64(10)
	rpd := int64(1000)
	handler := RateLimit(limiter, RateLimitOptions{})(next)
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req = req.WithContext(auth.ContextWithAPIKeyPrincipal(req.Context(), &auth.APIKeyPrincipal{
		APIKeyID: 7,
		UserID:   7,
		RouteID:  i64(1),
		RPMLimit: &rpm,
		RPDLimit: &rpd,
	}))
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if limiter.limits.RPM == nil || *limiter.limits.RPM != 10 {
		t.Fatalf("want rpm override 10 forwarded, got %v", limiter.limits.RPM)
	}
	if limiter.limits.RPD == nil || *limiter.limits.RPD != 1000 {
		t.Fatalf("want rpd override 1000 forwarded, got %v", limiter.limits.RPD)
	}
}

func TestRateLimitRejectsRequest(t *testing.T) {
	resetAt := time.Date(2026, 5, 8, 10, 1, 0, 0, time.UTC)
	limiter := &fakeKeyRateLimiter{
		decision: ratelimit.Decision{
			Allowed:   false,
			Limit:     60,
			Remaining: 0,
			ResetAt:   resetAt,
		},
	}

	nextCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { nextCalled = true })

	handler := RateLimit(limiter, RateLimitOptions{})(next)
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req = req.WithContext(auth.ContextWithAPIKeyPrincipal(req.Context(), &auth.APIKeyPrincipal{
		APIKeyID:  123,
		UserID:    123,
		RouteID:   i64(1),
		KeyPrefix: "unio_sk_test",
	}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if nextCalled {
		t.Fatal("want next handler not to be called")
	}
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("want status %d, got %d", http.StatusTooManyRequests, rec.Code)
	}
	assertRateLimitHeaders(t, rec, "60", "0", resetAt)

	var body httpx.ErrorResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if body.Error.Code != "rate_limited" {
		t.Fatalf("want error code %q, got %q", "rate_limited", body.Error.Code)
	}
}

func TestRateLimitUnlimitedSkipsHeaders(t *testing.T) {
	limiter := &fakeKeyRateLimiter{decision: ratelimit.Decision{Allowed: true, Limit: 0}}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })

	handler := RateLimit(limiter, RateLimitOptions{})(next)
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req = req.WithContext(auth.ContextWithAPIKeyPrincipal(req.Context(), &auth.APIKeyPrincipal{APIKeyID: 1, UserID: 1, RouteID: i64(1)}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Header().Get(HeaderRateLimitLimit) != "" {
		t.Fatalf("want no rate limit header under unlimited, got %q", rec.Header().Get(HeaderRateLimitLimit))
	}
}

func TestRateLimitMissingPrincipal(t *testing.T) {
	limiter := &fakeKeyRateLimiter{}
	nextCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { nextCalled = true })

	handler := RateLimit(limiter, RateLimitOptions{})(next)
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if nextCalled {
		t.Fatal("want next handler not to be called")
	}
	if limiter.called {
		t.Fatal("want limiter not to be called without principal")
	}
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want status %d, got %d", http.StatusInternalServerError, rec.Code)
	}
}

func TestRateLimitStoreErrorReturns500(t *testing.T) {
	limiter := &fakeKeyRateLimiter{err: errors.New("rate limit failed")}
	nextCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { nextCalled = true })

	handler := RateLimit(limiter, RateLimitOptions{Logger: zap.NewNop()})(next)
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req = req.WithContext(auth.ContextWithAPIKeyPrincipal(req.Context(), &auth.APIKeyPrincipal{
		APIKeyID:  123,
		UserID:    123,
		RouteID:   i64(1),
		KeyPrefix: "unio_sk_test",
	}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if nextCalled {
		t.Fatal("want next handler not to be called on store error (guard already applied fail policy)")
	}
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want status %d, got %d", http.StatusInternalServerError, rec.Code)
	}
	if limiter.userID != 123 {
		t.Fatalf("want user id 123, got %d", limiter.userID)
	}
}

// assertRateLimitHeaders 校验响应中的限流 header。
func assertRateLimitHeaders(t *testing.T, rec *httptest.ResponseRecorder, limit string, remaining string, resetAt time.Time) {
	t.Helper()

	if rec.Header().Get(HeaderRateLimitLimit) != limit {
		t.Fatalf("want %s %q, got %q", HeaderRateLimitLimit, limit, rec.Header().Get(HeaderRateLimitLimit))
	}
	if rec.Header().Get(HeaderRateLimitRemaining) != remaining {
		t.Fatalf("want %s %q, got %q", HeaderRateLimitRemaining, remaining, rec.Header().Get(HeaderRateLimitRemaining))
	}
	wantReset := strconv.FormatInt(resetAt.Unix(), 10)
	if rec.Header().Get(HeaderRateLimitReset) != wantReset {
		t.Fatalf("want %s %q, got %q", HeaderRateLimitReset, wantReset, rec.Header().Get(HeaderRateLimitReset))
	}
}
