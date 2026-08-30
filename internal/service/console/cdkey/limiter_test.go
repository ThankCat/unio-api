package cdkey

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestCDKeyRateLimiter(t *testing.T) (*RateLimiter, *miniredis.Miniredis) {
	t.Helper()
	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	limiter, err := NewRateLimiter(client, "test", "01234567890123456789012345678901")
	if err != nil {
		t.Fatal(err)
	}
	return limiter, mini
}

func TestRateLimiterBlocksUserAfterFailures(t *testing.T) {
	limiter, _ := newTestCDKeyRateLimiter(t)
	ctx := context.Background()
	for i := int64(0); i < DefaultCDKeyUserFailureLimit; i++ {
		if err := limiter.RecordFailure(ctx, 42, "192.0.2.10"); err != nil {
			t.Fatalf("record %d: %v", i+1, err)
		}
	}
	err := limiter.Check(ctx, 42, "192.0.2.10")
	if err == nil || err.Code != CodeCDKeyRateLimit || err.Status != 429 || err.RetryAfter <= 0 {
		t.Fatalf("expected user rate limit, got %v", err)
	}
}

func TestRateLimiterIPDimensionIsIndependent(t *testing.T) {
	limiter, _ := newTestCDKeyRateLimiter(t)
	ctx := context.Background()
	for i := int64(0); i < DefaultCDKeyIPFailureLimit; i++ {
		if err := limiter.RecordFailure(ctx, i+1, "192.0.2.10"); err != nil {
			t.Fatalf("record %d: %v", i+1, err)
		}
	}
	err := limiter.Check(ctx, 9999, "192.0.2.10")
	if err == nil || err.Code != CodeCDKeyRateLimit {
		t.Fatalf("expected IP rate limit, got %v", err)
	}
	if err := limiter.Check(ctx, 9999, "192.0.2.11"); err != nil {
		t.Fatalf("different IP should remain available: %v", err)
	}
}

func TestRateLimiterKeysDoNotExposeSubjects(t *testing.T) {
	limiter, mini := newTestCDKeyRateLimiter(t)
	if err := limiter.RecordFailure(context.Background(), 987654321, "192.0.2.44"); err != nil {
		t.Fatal(err)
	}
	for _, key := range mini.Keys() {
		if strings.Contains(key, "987654321") || strings.Contains(key, "192.0.2.44") {
			t.Fatalf("rate-limit key exposes subject: %q", key)
		}
	}
}

func TestRateLimiterResetAndExpiry(t *testing.T) {
	limiter, _ := newTestCDKeyRateLimiter(t)
	ctx := context.Background()
	now := time.Now()
	limiter.now = func() time.Time { return now }
	for i := int64(0); i < DefaultCDKeyUserFailureLimit; i++ {
		if err := limiter.RecordFailure(ctx, 7, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := limiter.Check(ctx, 7, ""); err == nil {
		t.Fatal("expected user bucket to be exhausted")
	}
	if err := limiter.Reset(ctx, 7, ""); err != nil {
		t.Fatal(err)
	}
	if err := limiter.Check(ctx, 7, ""); err != nil {
		t.Fatalf("reset should clear bucket: %v", err)
	}
	for i := int64(0); i < DefaultCDKeyUserFailureLimit; i++ {
		if err := limiter.RecordFailure(ctx, 7, ""); err != nil {
			t.Fatal(err)
		}
	}
	now = now.Add(DefaultCDKeyRateWindow + time.Second)
	if err := limiter.Check(ctx, 7, ""); err != nil {
		t.Fatalf("expired bucket should allow retry: %v", err)
	}
}
