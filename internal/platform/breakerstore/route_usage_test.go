package breakerstore

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

func TestAggregateRouteUsageSumsCurrentBuckets(t *testing.T) {
	s, client, _ := newTestStore(t)
	ctx := context.Background()
	now := time.Now()
	nowMs := now.UnixMilli()
	minute := minuteBucket(now)
	day := dayBucket(now)
	routeID := int64(42)

	// user 1: 2 active leases, 1 expired
	if err := client.ZAdd(ctx, s.keys.requestConcurrency(routeID, 1),
		redis.Z{Score: float64(nowMs + 60_000), Member: "lease-a"},
		redis.Z{Score: float64(nowMs + 30_000), Member: "lease-b"},
		redis.Z{Score: float64(nowMs - 1_000), Member: "lease-expired"},
	).Err(); err != nil {
		t.Fatalf("seed conc user1: %v", err)
	}
	// user 2: 1 active lease
	if err := client.ZAdd(ctx, s.keys.requestConcurrency(routeID, 2),
		redis.Z{Score: float64(nowMs + 10_000), Member: "lease-c"},
	).Err(); err != nil {
		t.Fatalf("seed conc user2: %v", err)
	}
	// other route should be ignored
	if err := client.ZAdd(ctx, s.keys.requestConcurrency(99, 1),
		redis.Z{Score: float64(nowMs + 10_000), Member: "x"},
	).Err(); err != nil {
		t.Fatalf("seed other route: %v", err)
	}

	mustSet(t, client, s.keys.requestRPMBucket(routeID, 1, minute), "10")
	mustSet(t, client, s.keys.requestRPMBucket(routeID, 2, minute), "5")
	mustSet(t, client, s.keys.requestRPMBucket(routeID, 1, minute-1), "100") // previous minute
	mustSet(t, client, s.keys.requestRPDBucket(routeID, 1, day), "20")
	mustSet(t, client, s.keys.requestRPDBucket(routeID, 2, day), "7")
	mustSet(t, client, s.keys.requestRPMBucket(routeID, 3, minute), "-3")

	usage, err := s.AggregateRouteUsage(ctx, routeID)
	if err != nil {
		t.Fatalf("AggregateRouteUsage: %v", err)
	}
	if usage.Concurrency != 3 {
		t.Fatalf("concurrency=%d want 3", usage.Concurrency)
	}
	if usage.RPM != 15 {
		t.Fatalf("rpm=%d want 15", usage.RPM)
	}
	if usage.RPD != 27 {
		t.Fatalf("rpd=%d want 27", usage.RPD)
	}
	// conc keys for users 1 and 2 only (user 3 has no conc key)
	if usage.ActiveUsers != 2 {
		t.Fatalf("active_users=%d want 2", usage.ActiveUsers)
	}
}

func TestAggregateRouteUsageFailsClosedOnInfrastructureFault(t *testing.T) {
	s, _, _ := newTestStore(t)
	s.fault.latch()
	_, err := s.AggregateRouteUsage(context.Background(), 7)
	if err == nil {
		t.Fatal("expected store unavailable")
	}
	if failure.CodeOf(err) != failure.CodeDependencyRedisUnavailable {
		t.Fatalf("unexpected error: %v code=%q", err, failure.CodeOf(err))
	}
}

func mustSet(t *testing.T, client *redis.Client, key, value string) {
	t.Helper()
	if err := client.Set(context.Background(), key, value, 0).Err(); err != nil {
		t.Fatalf("set %s: %v", key, err)
	}
}
