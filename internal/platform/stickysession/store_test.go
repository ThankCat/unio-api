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

func TestBindIfAbsentWritesCanonicalValueAndTTL(t *testing.T) {
	store, client, server := newTestStore(t)
	ctx := context.Background()

	bound, result := store.BindIfAbsent(ctx, "session", 7, 30*time.Minute)
	if !result.Applied || result.Conflict || result.StoreUnavailable {
		t.Fatalf("first bind must apply: %+v", result)
	}
	if bound.ChannelID != 7 || bound.BindingVersion <= 0 || bound.LastSuccessAt.IsZero() {
		t.Fatalf("bind must return the full CAS identity: %+v", bound)
	}

	raw, err := client.Get(ctx, "sticky-test:session").Result()
	if err != nil {
		t.Fatalf("read raw binding: %v", err)
	}
	var stored binding
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		t.Fatalf("decode stored binding %q: %v", raw, err)
	}
	if stored.Version != bindingSchemaVersion || stored.ChannelID != 7 ||
		stored.BindingVersion != bound.BindingVersion || stored.LastSuccessAtMs <= 0 {
		t.Fatalf("stored value is not the canonical schema: %+v", stored)
	}
	if ttl := server.TTL("sticky-test:session"); ttl != 30*time.Minute {
		t.Fatalf("bind TTL=%v want=30m", ttl)
	}

	lookup := store.Lookup(ctx, "session")
	if !lookup.Found || lookup.Binding.ChannelID != 7 ||
		lookup.Binding.BindingVersion != bound.BindingVersion {
		t.Fatalf("lookup must round-trip the CAS identity: %+v", lookup)
	}
}

// TestBindIfAbsentLosesToExistingBinding 冻结 §10.5：首轮并发只有第一个 CAS 成功者建绑。
func TestBindIfAbsentLosesToExistingBinding(t *testing.T) {
	store, _, server := newTestStore(t)
	ctx := context.Background()

	first, _ := store.BindIfAbsent(ctx, "session", 7, 30*time.Minute)
	_, second := store.BindIfAbsent(ctx, "session", 8, time.Hour)
	if second.Applied || !second.Conflict {
		t.Fatalf("second concurrent bind must report CAS conflict: %+v", second)
	}
	lookup := store.Lookup(ctx, "session")
	if lookup.Binding.ChannelID != 7 || lookup.Binding.BindingVersion != first.BindingVersion {
		t.Fatalf("losing bind overwrote the winner: %+v", lookup)
	}
	if ttl := server.TTL("sticky-test:session"); ttl != 30*time.Minute {
		t.Fatalf("losing bind changed TTL: %v", ttl)
	}
}

func TestRefreshIfCurrentSlidesTTLAndKeepsVersion(t *testing.T) {
	store, client, server := newTestStore(t)
	ctx := context.Background()

	bound, _ := store.BindIfAbsent(ctx, "session", 7, 5*time.Minute)
	// 把 last_success 往前挪，才能观察续期确实推进了时间。
	older := binding{
		Version: bindingSchemaVersion, ChannelID: 7, BindingVersion: bound.BindingVersion,
		LastSuccessAtMs: time.Now().Add(-20 * time.Minute).UnixMilli(),
	}
	raw, err := json.Marshal(older)
	if err != nil {
		t.Fatalf("encode older binding: %v", err)
	}
	if err := client.Set(ctx, "sticky-test:session", raw, 5*time.Minute).Err(); err != nil {
		t.Fatalf("seed older binding: %v", err)
	}

	next, result := store.RefreshIfCurrent(ctx, "session", 7, bound.BindingVersion, 30*time.Minute)
	if !result.Applied {
		t.Fatalf("refresh with matching identity must apply: %+v", result)
	}
	if next.BindingVersion != bound.BindingVersion {
		t.Fatalf("refresh must keep the same binding_version: got %d want %d",
			next.BindingVersion, bound.BindingVersion)
	}
	lookup := store.Lookup(ctx, "session")
	if !lookup.Found || lookup.Binding.LastSuccessAt.Before(time.Now().Add(-time.Minute)) {
		t.Fatalf("refresh must advance last success: %+v", lookup)
	}
	if ttl := server.TTL("sticky-test:session"); ttl != 30*time.Minute {
		t.Fatalf("refresh TTL=%v want=30m", ttl)
	}
}

// TestRefreshIfCurrentRejectsStaleIdentity 冻结 CAS 必须同时比较 channel_id 与 binding_version：
// 一个慢请求带着旧 version 回来，不得续活/覆盖新绑定，即使 channel_id 恰好相同。
func TestRefreshIfCurrentRejectsStaleIdentity(t *testing.T) {
	store, _, server := newTestStore(t)
	ctx := context.Background()

	stale, _ := store.BindIfAbsent(ctx, "session", 7, 10*time.Minute)
	if applied := store.ClearIfCurrent(ctx, "session", 7, stale.BindingVersion); !applied.Applied {
		t.Fatalf("precondition clear failed: %+v", applied)
	}
	// 同一个 channel 被重新绑定 → 新的 binding_version。
	fresh, _ := store.BindIfAbsent(ctx, "session", 7, 10*time.Minute)
	if fresh.BindingVersion == stale.BindingVersion {
		t.Fatal("rebinding the same channel must produce a new binding_version")
	}

	_, result := store.RefreshIfCurrent(ctx, "session", 7, stale.BindingVersion, time.Hour)
	if result.Applied || !result.Conflict {
		t.Fatalf("stale binding_version must lose the CAS even on the same channel: %+v", result)
	}
	if ttl := server.TTL("sticky-test:session"); ttl != 10*time.Minute {
		t.Fatalf("stale refresh changed TTL: %v", ttl)
	}

	_, wrongChannel := store.RefreshIfCurrent(ctx, "session", 8, fresh.BindingVersion, time.Hour)
	if wrongChannel.Applied || !wrongChannel.Conflict {
		t.Fatalf("mismatched channel must lose the CAS: %+v", wrongChannel)
	}
}

func TestClearIfCurrentOnlyRemovesTheMatchingBinding(t *testing.T) {
	store, _, _ := newTestStore(t)
	ctx := context.Background()

	bound, _ := store.BindIfAbsent(ctx, "session", 7, 30*time.Minute)

	if result := store.ClearIfCurrent(ctx, "session", 8, bound.BindingVersion); result.Applied || !result.Conflict {
		t.Fatalf("clearing a different channel must be a CAS conflict: %+v", result)
	}
	if result := store.ClearIfCurrent(ctx, "session", 7, bound.BindingVersion+1); result.Applied || !result.Conflict {
		t.Fatalf("clearing a different version must be a CAS conflict: %+v", result)
	}
	if lookup := store.Lookup(ctx, "session"); !lookup.Found {
		t.Fatal("failed CAS clears must not delete the binding")
	}

	if result := store.ClearIfCurrent(ctx, "session", 7, bound.BindingVersion); !result.Applied {
		t.Fatalf("matching clear must apply: %+v", result)
	}
	if lookup := store.Lookup(ctx, "session"); lookup.Found {
		t.Fatalf("binding must be gone after a matching clear: %+v", lookup)
	}
}

func TestClearIfCurrentOnMissingBindingIsConflictNotError(t *testing.T) {
	store, _, _ := newTestStore(t)
	result := store.ClearIfCurrent(context.Background(), "absent", 7, 1)
	if result.Applied || result.StoreUnavailable || !result.Conflict {
		t.Fatalf("clearing an absent binding must be a benign conflict: %+v", result)
	}
}

func TestLookupTreatsNonCanonicalValueAsMiss(t *testing.T) {
	store, _, server := newTestStore(t)
	ctx := context.Background()
	for name, value := range map[string]string{
		"legacy integer":  "7",
		"legacy v2":       `{"v":2,"channel_id":7,"bound_at_ms":1}`,
		"legacy v3":       `{"v":3,"channel_id":7,"last_success_at_ms":1}`,
		"missing version": `{"channel_id":7,"binding_version":1,"last_success_at_ms":1}`,
		"zero channel":    `{"v":1,"channel_id":0,"binding_version":1,"last_success_at_ms":1}`,
		"zero binding":    `{"v":1,"channel_id":7,"binding_version":0,"last_success_at_ms":1}`,
		"not json":        `not-json`,
	} {
		t.Run(name, func(t *testing.T) {
			server.Set("sticky-test:broken", value)
			if lookup := store.Lookup(ctx, "broken"); lookup.Found || lookup.StoreUnavailable {
				t.Fatalf("non-canonical value must be a plain miss: %+v", lookup)
			}
		})
	}
}

// TestStoreFailuresAreReportedButNeverFatal 冻结 §10.11 fail-open：
// Redis 故障必须被报告为 store_unavailable，而不是伪装成 miss 或冲突。
func TestStoreFailuresAreReportedButNeverFatal(t *testing.T) {
	store, client, server := newTestStore(t)
	ctx := context.Background()
	bound, _ := store.BindIfAbsent(ctx, "session", 7, 30*time.Minute)
	server.Close()
	_ = client

	if lookup := store.Lookup(ctx, "session"); lookup.Found || !lookup.StoreUnavailable {
		t.Fatalf("lookup on a dead store must report store_unavailable: %+v", lookup)
	}
	if _, result := store.BindIfAbsent(ctx, "session", 8, time.Minute); !result.StoreUnavailable {
		t.Fatalf("bind on a dead store must report store_unavailable: %+v", result)
	}
	if _, result := store.RefreshIfCurrent(ctx, "session", 7, bound.BindingVersion, time.Minute); !result.StoreUnavailable {
		t.Fatalf("refresh on a dead store must report store_unavailable: %+v", result)
	}
	if result := store.ClearIfCurrent(ctx, "session", 7, bound.BindingVersion); !result.StoreUnavailable {
		t.Fatalf("clear on a dead store must report store_unavailable: %+v", result)
	}
}
