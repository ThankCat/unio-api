package auth

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestPasswordLoginLimiter(t *testing.T) (*PasswordLoginLimiter, *miniredis.Miniredis) {
	t.Helper()
	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	limiter, err := NewPasswordLoginLimiter(
		client,
		"test",
		"01234567890123456789012345678901",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	return limiter, mini
}

func TestPasswordLoginLimiterBlocksEmailIPAfterFailures(t *testing.T) {
	limiter, _ := newTestPasswordLoginLimiter(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if err := limiter.Check(ctx, "user@example.com", "192.0.2.10"); err != nil {
			t.Fatalf("check %d: %v", i+1, err)
		}
		if err := limiter.RecordFailure(ctx, "user@example.com", "192.0.2.10"); err != nil {
			t.Fatalf("record %d: %v", i+1, err)
		}
	}
	if err := limiter.Check(ctx, "user@example.com", "192.0.2.10"); err == nil || err.Code != CodePasswordLoginRateLimited || err.RetryAfter <= 0 {
		t.Fatalf("expected rate limit after failures, got %v", err)
	}
	if err := limiter.Check(ctx, "user@example.com", "192.0.2.11"); err != nil {
		t.Fatalf("different source should remain available: %v", err)
	}
}

func TestPasswordLoginLimiterSuccessfulPairResetKeepsIPFailures(t *testing.T) {
	limiter, _ := newTestPasswordLoginLimiter(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if err := limiter.RecordFailure(ctx, "user@example.com", "192.0.2.10"); err != nil {
			t.Fatal(err)
		}
	}
	if err := limiter.ResetEmailIP(ctx, "user@example.com", "192.0.2.10"); err != nil {
		t.Fatal(err)
	}
	if err := limiter.Check(ctx, "user@example.com", "192.0.2.10"); err != nil {
		t.Fatalf("successful pair should be reset: %v", err)
	}
	for i := 5; i < 30; i++ {
		if err := limiter.RecordFailure(ctx, fmt.Sprintf("user-%d@example.com", i), "192.0.2.10"); err != nil {
			t.Fatal(err)
		}
	}
	if err := limiter.Check(ctx, "another@example.com", "192.0.2.10"); err == nil || err.Code != CodePasswordLoginRateLimited {
		t.Fatalf("IP-wide failures must remain after pair reset, got %v", err)
	}
}

func TestPasswordLoginLimiterDoesNotExposeEmailOrIPInRedisKeys(t *testing.T) {
	limiter, mini := newTestPasswordLoginLimiter(t)
	if err := limiter.RecordFailure(context.Background(), "Sensitive@example.com", "192.0.2.44"); err != nil {
		t.Fatal(err)
	}
	for _, key := range mini.Keys() {
		if strings.Contains(strings.ToLower(key), "sensitive") || strings.Contains(key, "192.0.2.44") {
			t.Fatalf("rate-limit key exposes login subject: %q", key)
		}
	}
}

func TestPasswordLoginLimiterWindowExpires(t *testing.T) {
	limiter, _ := newTestPasswordLoginLimiter(t)
	ctx := context.Background()
	now := time.Now()
	limiter.now = func() time.Time { return now }
	for i := 0; i < 5; i++ {
		if err := limiter.RecordFailure(ctx, "user@example.com", "192.0.2.10"); err != nil {
			t.Fatal(err)
		}
	}
	now = now.Add(15 * time.Minute)
	if err := limiter.Check(ctx, "user@example.com", "192.0.2.10"); err != nil {
		t.Fatalf("expired failure window should allow retry: %v", err)
	}
}
