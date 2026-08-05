package adminlogin

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

func newTestLimiter(t *testing.T, sourceLimit, accountLimit int, window time.Duration) (*Limiter, *miniredis.Miniredis) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return NewLimiter(client, "login-test", sourceLimit, accountLimit, window), server
}

func TestLimiterBlocksSourceAfterLimit(t *testing.T) {
	limiter, _ := newTestLimiter(t, 2, 20, 15*time.Minute)

	for i := 0; i < 2; i++ {
		allowed, retryAfter, err := limiter.Allow(context.Background(), "admin", "192.0.2.10:1234")
		if err != nil {
			t.Fatalf("allow attempt %d: %v", i+1, err)
		}
		if !allowed || retryAfter != 0 {
			t.Fatalf("attempt %d = allowed %v retry %v", i+1, allowed, retryAfter)
		}
	}

	allowed, retryAfter, err := limiter.Allow(context.Background(), "admin", "192.0.2.10:5678")
	if err != nil {
		t.Fatalf("allow blocked attempt: %v", err)
	}
	if allowed {
		t.Fatal("expected source to be blocked")
	}
	if retryAfter <= 0 || retryAfter > 15*time.Minute {
		t.Fatalf("retry after = %v", retryAfter)
	}
}

func TestLimiterBlocksAccountAcrossSources(t *testing.T) {
	limiter, _ := newTestLimiter(t, 10, 3, 15*time.Minute)

	for i, addr := range []string{"192.0.2.1:1", "192.0.2.2:2", "192.0.2.3:3"} {
		allowed, _, err := limiter.Allow(context.Background(), "admin", addr)
		if err != nil {
			t.Fatalf("allow account attempt %d: %v", i+1, err)
		}
		if !allowed {
			t.Fatalf("attempt %d unexpectedly blocked", i+1)
		}
	}

	allowed, retryAfter, err := limiter.Allow(context.Background(), "admin", "192.0.2.4:4")
	if err != nil {
		t.Fatalf("allow blocked account attempt: %v", err)
	}
	if allowed || retryAfter <= 0 {
		t.Fatalf("account attempt = allowed %v retry %v", allowed, retryAfter)
	}
}

func TestLimiterSharesStateAcrossInstances(t *testing.T) {
	server := miniredis.RunT(t)
	clientA := redis.NewClient(&redis.Options{Addr: server.Addr()})
	clientB := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() {
		_ = clientA.Close()
		_ = clientB.Close()
	})
	limiterA := NewLimiter(clientA, "shared-login-test", 1, 20, 15*time.Minute)
	limiterB := NewLimiter(clientB, "shared-login-test", 1, 20, 15*time.Minute)
	ctx := context.Background()

	if allowed, _, err := limiterA.Allow(ctx, "admin", "192.0.2.10:1"); err != nil || !allowed {
		t.Fatalf("instance A allow = allowed %v err %v", allowed, err)
	}
	if allowed, _, err := limiterB.Allow(ctx, "admin", "192.0.2.10:2"); err != nil || allowed {
		t.Fatalf("instance B allow = allowed %v err %v", allowed, err)
	}
}

func TestLimiterResetClearsSourceAndAccountWindows(t *testing.T) {
	limiter, _ := newTestLimiter(t, 1, 1, 15*time.Minute)
	ctx := context.Background()

	allowed, _, err := limiter.Allow(ctx, "admin", "192.0.2.10:1234")
	if err != nil || !allowed {
		t.Fatalf("first allow = allowed %v err %v", allowed, err)
	}
	if err := limiter.Reset(ctx, "admin", "192.0.2.10:9999"); err != nil {
		t.Fatalf("reset: %v", err)
	}

	allowed, retryAfter, err := limiter.Allow(ctx, "admin", "192.0.2.10:4321")
	if err != nil || !allowed || retryAfter != 0 {
		t.Fatalf("allow after reset = allowed %v retry %v err %v", allowed, retryAfter, err)
	}
}

func TestLimiterWindowExpires(t *testing.T) {
	limiter, server := newTestLimiter(t, 1, 10, time.Minute)
	ctx := context.Background()

	if allowed, _, err := limiter.Allow(ctx, "admin", "192.0.2.10:1"); err != nil || !allowed {
		t.Fatalf("first allow = allowed %v err %v", allowed, err)
	}
	if allowed, _, err := limiter.Allow(ctx, "admin", "192.0.2.10:2"); err != nil || allowed {
		t.Fatalf("second allow = allowed %v err %v", allowed, err)
	}

	server.FastForward(time.Minute)
	if allowed, _, err := limiter.Allow(ctx, "admin", "192.0.2.10:3"); err != nil || !allowed {
		t.Fatalf("allow after expiry = allowed %v err %v", allowed, err)
	}
}

func TestLimiterDoesNotStoreUsernameOrAddressInKeys(t *testing.T) {
	limiter, server := newTestLimiter(t, 5, 20, 15*time.Minute)
	if _, _, err := limiter.Allow(context.Background(), "SensitiveAdmin", "192.0.2.44:1234"); err != nil {
		t.Fatalf("allow: %v", err)
	}

	for _, key := range server.Keys() {
		if strings.Contains(strings.ToLower(key), "sensitiveadmin") || strings.Contains(key, "192.0.2.44") {
			t.Fatalf("key exposes login subject: %q", key)
		}
	}
}

func TestLimiterFailsClosedWhenRedisIsUnavailable(t *testing.T) {
	limiter, server := newTestLimiter(t, 5, 20, 15*time.Minute)
	server.Close()

	allowed, _, err := limiter.Allow(context.Background(), "admin", "192.0.2.10:1234")
	if err == nil || allowed {
		t.Fatalf("allow = allowed %v err %v", allowed, err)
	}
	if got := failure.CodeOf(err); got != failure.CodeAdminAuthLoginRateLimitStoreFailed {
		t.Fatalf("failure code = %q", got)
	}
}
