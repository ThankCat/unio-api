package stickysession

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// newRealRedisStore 连接 REDIS_ADDR 指向的隔离 Redis 实例。
//
// CAS 语义由 Lua 脚本实现，而 miniredis 的 Lua 是独立的 Go 实现（cjson / tonumber 行为可能不同），
// 因此 §10.4 的三个 CAS 写操作必须额外在真实 Redis 上验证一遍。
// 未设置 REDIS_ADDR 时跳过；每个子测试使用独立 key namespace 并在结束时清理自己创建的键。
func newRealRedisStore(t *testing.T) *Store {
	t.Helper()
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		t.Skip("REDIS_ADDR is not set; skipping the real-Redis sticky CAS contract test")
	}
	namespace := "sticky-cas-" + t.Name()
	client := redis.NewClient(&redis.Options{Addr: addr})
	ctx := context.Background()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Skipf("REDIS_ADDR is not reachable: %v", err)
	}
	t.Cleanup(func() {
		keys, err := client.Keys(ctx, namespace+":*").Result()
		if err == nil && len(keys) > 0 {
			_ = client.Del(ctx, keys...).Err()
		}
		_ = client.Close()
	})
	return NewStore(client, namespace, nil)
}

func TestRealRedisCASContract(t *testing.T) {
	store := newRealRedisStore(t)
	ctx := context.Background()

	bound, result := store.BindIfAbsent(ctx, "session", 7, 30*time.Minute)
	if !result.Applied || bound.BindingVersion <= 0 {
		t.Fatalf("bind_if_absent on real redis: binding=%+v result=%+v", bound, result)
	}
	if _, second := store.BindIfAbsent(ctx, "session", 8, time.Hour); !second.Conflict {
		t.Fatalf("second bind must conflict on real redis: %+v", second)
	}

	// Lua CAS 必须同时比较 channel_id 与 binding_version。
	if _, mismatch := store.RefreshIfCurrent(ctx, "session", 8, bound.BindingVersion, time.Hour); !mismatch.Conflict {
		t.Fatalf("refresh with a wrong channel must conflict: %+v", mismatch)
	}
	if _, mismatch := store.RefreshIfCurrent(ctx, "session", 7, bound.BindingVersion+1, time.Hour); !mismatch.Conflict {
		t.Fatalf("refresh with a wrong version must conflict: %+v", mismatch)
	}
	if _, ok := store.RefreshIfCurrent(ctx, "session", 7, bound.BindingVersion, time.Hour); !ok.Applied {
		t.Fatalf("refresh with the matching identity must apply: %+v", ok)
	}

	if lookup := store.Lookup(ctx, "session"); !lookup.Found ||
		lookup.Binding.ChannelID != 7 || lookup.Binding.BindingVersion != bound.BindingVersion {
		t.Fatalf("real-redis lookup must round-trip the CAS identity: %+v", lookup)
	}

	if mismatch := store.ClearIfCurrent(ctx, "session", 7, bound.BindingVersion+1); !mismatch.Conflict {
		t.Fatalf("clear with a wrong version must conflict: %+v", mismatch)
	}
	if lookup := store.Lookup(ctx, "session"); !lookup.Found {
		t.Fatal("a failed CAS clear must not delete the binding on real redis")
	}
	if ok := store.ClearIfCurrent(ctx, "session", 7, bound.BindingVersion); !ok.Applied {
		t.Fatalf("clear with the matching identity must apply: %+v", ok)
	}
	if lookup := store.Lookup(ctx, "session"); lookup.Found {
		t.Fatalf("binding must be gone after a matching clear: %+v", lookup)
	}
}

// TestRealRedisBindingVersionStaysLuaExact 验证 binding_version 落在 Lua double 可精确表示的范围内：
// 超过 2^53 时 Lua 会把相邻整数判为相等，CAS 就形同虚设。
func TestRealRedisBindingVersionStaysLuaExact(t *testing.T) {
	store := newRealRedisStore(t)
	ctx := context.Background()

	for i := 0; i < 50; i++ {
		bound, result := store.BindIfAbsent(ctx, "version-range", 7, time.Minute)
		if !result.Applied {
			t.Fatalf("bind %d: %+v", i, result)
		}
		if bound.BindingVersion <= 0 || bound.BindingVersion >= maxLuaExactBindingVersion {
			t.Fatalf("binding_version %d is outside the Lua-exact range", bound.BindingVersion)
		}
		// 相邻 version 必须被 Lua 区分开。
		if _, offByOne := store.RefreshIfCurrent(ctx, "version-range", 7, bound.BindingVersion+1, time.Minute); !offByOne.Conflict {
			t.Fatalf("version %d+1 was not distinguishable in Lua: %+v", bound.BindingVersion, offByOne)
		}
		if cleared := store.ClearIfCurrent(ctx, "version-range", 7, bound.BindingVersion); !cleared.Applied {
			t.Fatalf("cleanup clear %d: %+v", i, cleared)
		}
	}
}
