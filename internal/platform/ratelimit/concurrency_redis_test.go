package ratelimit

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

func TestRedisConcurrencyLimiterSharesChannelCapacityAcrossInstances(t *testing.T) {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		t.Skip("REDIS_ADDR is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := redis.NewClient(&redis.Options{Addr: addr})
	defer client.Close()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("ping redis: %v", err)
	}

	namespace := "unio:test:concurrency:" + time.Now().Format("150405.000000000")
	first := NewRedisConcurrencyLimiter(client, namespace, 0, 1, zap.NewNop())
	second := NewRedisConcurrencyLimiter(client, namespace, 0, 1, zap.NewNop())
	key := first.redisKey(ChannelInflightSubject(77))
	t.Cleanup(func() { _ = client.Del(context.Background(), key).Err() })

	release, ok := first.AcquireChannel(77, nil)
	if !ok {
		t.Fatal("first gateway instance should acquire shared channel")
	}
	if _, ok := second.AcquireChannel(77, nil); ok {
		t.Fatal("second gateway instance must observe the same global limit")
	}
	snapshot, err := second.ChannelSnapshot(ctx, 77, nil)
	if err != nil || !snapshot.Known || snapshot.Used != 1 || snapshot.Limit != 1 {
		t.Fatalf("unexpected shared snapshot: %+v err=%v", snapshot, err)
	}

	release()
	release2, ok := second.AcquireChannel(77, nil)
	if !ok {
		t.Fatal("capacity should become available globally after release")
	}
	release2()
}
