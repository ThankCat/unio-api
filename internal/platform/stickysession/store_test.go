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

func TestStoreBindLookupAndRebindV3(t *testing.T) {
	store, client, server := newTestStore(t)
	ctx := context.Background()
	store.Bind(ctx, "session", 7, 30*time.Minute)

	channelID, lastSuccessAt, ok := store.Lookup(ctx, "session")
	if !ok || channelID != 7 || lastSuccessAt.IsZero() {
		t.Fatalf("unexpected v3 lookup: channel=%d last_success_at=%v ok=%v", channelID, lastSuccessAt, ok)
	}
	raw, err := client.Get(ctx, "sticky-test:session").Result()
	if err != nil {
		t.Fatalf("read raw binding: %v", err)
	}
	var binding stickyBindingV3
	if err := json.Unmarshal([]byte(raw), &binding); err != nil || binding.Version != 3 || binding.ChannelID != 7 {
		t.Fatalf("unexpected v3 payload %q: binding=%+v err=%v", raw, binding, err)
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
	var binding stickyBindingV3
	if err := json.Unmarshal([]byte(raw), &binding); err != nil || binding.Version != 3 || binding.ChannelID != 7 {
		t.Fatalf("legacy value was not upgraded: raw=%q binding=%+v err=%v", raw, binding, err)
	}
	if ttl := server.TTL(key); ttl != 12*time.Minute {
		t.Fatalf("legacy upgrade changed TTL: %v", ttl)
	}
}

func TestStoreV2LookupUpgradesWithoutChangingTTL(t *testing.T) {
	store, client, server := newTestStore(t)
	ctx := context.Background()
	key := "sticky-test:v2"
	boundAt := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	raw, err := json.Marshal(stickyBindingV2{Version: 2, ChannelID: 9, BoundAtMs: boundAt.UnixMilli()})
	if err != nil {
		t.Fatalf("encode v2 binding: %v", err)
	}
	server.Set(key, string(raw))
	server.SetTTL(key, 7*time.Minute)

	channelID, lastSuccessAt, ok := store.Lookup(ctx, "v2")
	if !ok || channelID != 9 || !lastSuccessAt.Equal(boundAt) {
		t.Fatalf("v2 lookup failed: channel=%d last_success=%v ok=%v", channelID, lastSuccessAt, ok)
	}
	upgraded, err := client.Get(ctx, key).Result()
	if err != nil {
		t.Fatalf("read upgraded v3 binding: %v", err)
	}
	var binding stickyBindingV3
	if err := json.Unmarshal([]byte(upgraded), &binding); err != nil || binding.Version != 3 || binding.ChannelID != 9 || binding.LastSuccessAtMs != boundAt.UnixMilli() {
		t.Fatalf("v2 value was not upgraded: raw=%q binding=%+v err=%v", upgraded, binding, err)
	}
	if ttl := server.TTL(key); ttl != 7*time.Minute {
		t.Fatalf("v2 upgrade changed TTL: %v", ttl)
	}
}

func TestStoreLookupTreatsCorruptionAsMiss(t *testing.T) {
	store, _, server := newTestStore(t)
	server.Set("sticky-test:broken", `{"v":2,"channel_id":0,"bound_at_ms":1}`)
	if channelID, boundAt, ok := store.Lookup(context.Background(), "broken"); ok || channelID != 0 || !boundAt.IsZero() {
		t.Fatalf("corrupt binding must be a miss: channel=%d bound_at=%v ok=%v", channelID, boundAt, ok)
	}
}

func TestStoreRefreshIfBoundSlidesSuccessTimeAndTTL(t *testing.T) {
	store, client, server := newTestStore(t)
	ctx := context.Background()
	oldSuccess := time.Now().Add(-20 * time.Minute).Truncate(time.Millisecond)
	raw, err := encodeBinding(7, oldSuccess)
	if err != nil {
		t.Fatalf("encode old binding: %v", err)
	}
	if err := client.Set(ctx, "sticky-test:session", raw, 5*time.Minute).Err(); err != nil {
		t.Fatalf("seed old binding: %v", err)
	}

	store.RefreshIfBound(ctx, "session", 7, 30*time.Minute)
	_, after, ok := store.Lookup(ctx, "session")
	if !ok || !after.After(oldSuccess) {
		t.Fatalf("refresh must advance last success: old=%v after=%v ok=%v", oldSuccess, after, ok)
	}
	if ttl := server.TTL("sticky-test:session"); ttl != 30*time.Minute {
		t.Fatalf("refresh TTL=%v want=30m", ttl)
	}
}

func TestStoreRefreshIfBoundDoesNotReviveStaleChannel(t *testing.T) {
	store, _, server := newTestStore(t)
	ctx := context.Background()
	store.Bind(ctx, "session", 8, 10*time.Minute)
	_, before, ok := store.Lookup(ctx, "session")
	if !ok {
		t.Fatal("expected current binding")
	}

	store.RefreshIfBound(ctx, "session", 7, 30*time.Minute)
	channelID, after, ok := store.Lookup(ctx, "session")
	if !ok || channelID != 8 || !after.Equal(before) {
		t.Fatalf("stale refresh changed binding: channel=%d before=%v after=%v ok=%v", channelID, before, after, ok)
	}
	if ttl := server.TTL("sticky-test:session"); ttl != 10*time.Minute {
		t.Fatalf("stale refresh TTL=%v want=10m", ttl)
	}
}
