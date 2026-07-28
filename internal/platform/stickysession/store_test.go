package stickysession

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestStore(t *testing.T) (*Store, *redis.Client, *miniredis.Miniredis) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return NewStore(client, "sticky-test", nil), client, server
}

func TestStoreBindLookupAndRebindV2(t *testing.T) {
	store, client, server := newTestStore(t)
	ctx := context.Background()
	store.Bind(ctx, "session", 7, 30*time.Minute)

	channelID, boundAt, ok := store.Lookup(ctx, "session")
	if !ok || channelID != 7 || boundAt.IsZero() {
		t.Fatalf("unexpected v2 lookup: channel=%d bound_at=%v ok=%v", channelID, boundAt, ok)
	}
	raw, err := client.Get(ctx, "sticky-test:session").Result()
	if err != nil {
		t.Fatalf("read raw binding: %v", err)
	}
	var binding stickyBindingV2
	if err := json.Unmarshal([]byte(raw), &binding); err != nil || binding.Version != 2 || binding.ChannelID != 7 {
		t.Fatalf("unexpected v2 payload %q: binding=%+v err=%v", raw, binding, err)
	}
	if ttl := server.TTL("sticky-test:session"); ttl != 30*time.Minute {
		t.Fatalf("bind TTL=%v want=30m", ttl)
	}

	store.Bind(ctx, "session", 8, time.Hour)
	if got, _, _ := store.Lookup(ctx, "session"); got != 7 {
		t.Fatalf("SETNX bind overwrote existing channel: %d", got)
	}
	store.Rebind(ctx, "session", 8, time.Hour)
	if got, _, _ := store.Lookup(ctx, "session"); got != 8 {
		t.Fatalf("rebind channel=%d want=8", got)
	}
	if ttl := server.TTL("sticky-test:session"); ttl != time.Hour {
		t.Fatalf("rebind TTL=%v want=1h", ttl)
	}
}

func TestStoreLegacyLookupUpgradesWithoutChangingTTL(t *testing.T) {
	store, client, server := newTestStore(t)
	ctx := context.Background()
	key := "sticky-test:legacy"
	server.Set(key, "7")
	server.SetTTL(key, 12*time.Minute)

	channelID, boundAt, ok := store.Lookup(ctx, "legacy")
	if !ok || channelID != 7 || boundAt.IsZero() {
		t.Fatalf("legacy lookup failed: channel=%d bound_at=%v ok=%v", channelID, boundAt, ok)
	}
	raw, err := client.Get(ctx, key).Result()
	if err != nil {
		t.Fatalf("read upgraded binding: %v", err)
	}
	var binding stickyBindingV2
	if err := json.Unmarshal([]byte(raw), &binding); err != nil || binding.Version != 2 || binding.ChannelID != 7 {
		t.Fatalf("legacy value was not upgraded: raw=%q binding=%+v err=%v", raw, binding, err)
	}
	if ttl := server.TTL(key); ttl != 12*time.Minute {
		t.Fatalf("legacy upgrade changed TTL: %v", ttl)
	}
}

func TestStoreLookupTreatsCorruptionAsMiss(t *testing.T) {
	store, _, server := newTestStore(t)
	server.Set("sticky-test:broken", `{"v":2,"channel_id":0,"bound_at_ms":1}`)
	if channelID, boundAt, ok := store.Lookup(context.Background(), "broken"); ok || channelID != 0 || !boundAt.IsZero() {
		t.Fatalf("corrupt binding must be a miss: channel=%d bound_at=%v ok=%v", channelID, boundAt, ok)
	}
}

func TestStoreRefreshAdjustsOnlyPhysicalExpiry(t *testing.T) {
	store, _, server := newTestStore(t)
	ctx := context.Background()
	store.Bind(ctx, "session", 7, 30*time.Minute)
	_, before, ok := store.Lookup(ctx, "session")
	if !ok {
		t.Fatal("expected binding before refresh")
	}
	store.Refresh(ctx, "session", 5*time.Minute)
	_, after, ok := store.Lookup(ctx, "session")
	if !ok || !after.Equal(before) {
		t.Fatalf("refresh changed bound_at: before=%v after=%v ok=%v", before, after, ok)
	}
	if ttl := server.TTL("sticky-test:session"); ttl != 5*time.Minute {
		t.Fatalf("refresh TTL=%v want=5m", ttl)
	}
}
