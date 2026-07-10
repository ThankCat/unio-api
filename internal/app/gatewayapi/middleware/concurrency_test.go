package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ThankCat/unio-api/internal/core/auth"
	"github.com/ThankCat/unio-api/internal/platform/ratelimit"
)

func concurrencyTestRequest() *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	return req.WithContext(auth.ContextWithAPIKeyPrincipal(req.Context(), &auth.APIKeyPrincipal{
		APIKeyID:  123,
		UserID:    456,
		RouteID:   i64(9),
		KeyPrefix: "unio_sk_test",
	}))
}

// TestConcurrencyLimitRejectsExcessInFlight 验证在途已满时立即 429，且不进 next（不打上游）。
func TestConcurrencyLimitRejectsExcessInFlight(t *testing.T) {
	limiter := ratelimit.NewConcurrencyLimiter(1, 0)

	// 占住唯一名额，模拟一个仍在进行中的请求。
	blocked, ok := limiter.AcquireRouteUser(9, 456)
	if !ok {
		t.Fatal("precondition: first slot must acquire")
	}
	defer blocked()

	nextCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusNoContent)
	})

	handler := ConcurrencyLimit(limiter)(next)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, concurrencyTestRequest())

	if nextCalled {
		t.Fatal("next handler must not be called when concurrency limit is hit")
	}
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("want status %d, got %d", http.StatusTooManyRequests, rec.Code)
	}
}

// TestConcurrencyLimitReleasesAfterRequest 验证请求结束后名额被释放，后续请求可再进。
func TestConcurrencyLimitReleasesAfterRequest(t *testing.T) {
	limiter := ratelimit.NewConcurrencyLimiter(1, 0)

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 处理期间名额被占用。
		if _, ok := limiter.AcquireRouteUser(9, 456); ok {
			t.Error("slot should be held while request is in flight")
		}
		w.WriteHeader(http.StatusNoContent)
	})

	handler := ConcurrencyLimit(limiter)(next)
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, concurrencyTestRequest())
		if rec.Code != http.StatusNoContent {
			t.Fatalf("request %d: want %d, got %d", i, http.StatusNoContent, rec.Code)
		}
	}
	if got := limiter.Inflight(ratelimit.RouteUserInflightSubject(9, 456)); got != 0 {
		t.Fatalf("inflight should be released after requests, got %d", got)
	}
}

// TestConcurrencyLimitNilLimiterPassesThrough 验证未启用（nil）时零行为变化。
func TestConcurrencyLimitNilLimiterPassesThrough(t *testing.T) {
	nextCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusNoContent)
	})

	handler := ConcurrencyLimit(nil)(next)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, concurrencyTestRequest())

	if !nextCalled || rec.Code != http.StatusNoContent {
		t.Fatalf("nil limiter must pass through, nextCalled=%v code=%d", nextCalled, rec.Code)
	}
}
