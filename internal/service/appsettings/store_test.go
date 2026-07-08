package appsettings

import (
	"context"
	"encoding/json"
	"testing"

	messagesadapter "github.com/ThankCat/unio-api/internal/core/adapter/anthropic/messages"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
	"github.com/jackc/pgx/v5"
)

// fakeQueries 是内存版 app_settings 存储(单测用)。
type fakeQueries struct {
	data    map[string][]byte
	getErr  error
	getHits int
	seedErr error
}

func newFakeQueries() *fakeQueries { return &fakeQueries{data: map[string][]byte{}} }

func (f *fakeQueries) GetAppSetting(_ context.Context, key string) ([]byte, error) {
	f.getHits++
	if f.getErr != nil {
		return nil, f.getErr
	}
	v, ok := f.data[key]
	if !ok {
		return nil, pgx.ErrNoRows
	}
	return v, nil
}

func (f *fakeQueries) UpsertAppSetting(_ context.Context, arg sqlc.UpsertAppSettingParams) error {
	f.data[arg.Key] = arg.Value
	return nil
}

// SeedAppSetting 复刻 ON CONFLICT DO NOTHING 语义:已有行绝不覆盖。
func (f *fakeQueries) SeedAppSetting(_ context.Context, arg sqlc.SeedAppSettingParams) error {
	if f.seedErr != nil {
		return f.seedErr
	}
	if _, exists := f.data[arg.Key]; exists {
		return nil
	}
	f.data[arg.Key] = arg.Value
	return nil
}

func newTestStore(q Queries) *SettingsStore {
	// redis=nil：退化为 DB + 本地缓存,足以覆盖读写/默认/校验逻辑。
	return NewSettingsStore(q, nil, "test", DefaultRegistry(), nil)
}

func TestBetaPolicyDefaultWhenAbsent(t *testing.T) {
	store := newTestStore(newFakeQueries())
	got := GetAnthropicBetaPolicy(context.Background(), store)
	if got.Mode != messagesadapter.BetaModeFilter {
		t.Fatalf("mode = %q, want filter (default)", got.Mode)
	}
}

func TestBetaPolicySetThenGetRoundtrip(t *testing.T) {
	q := newFakeQueries()
	store := newTestStore(q)
	want := messagesadapter.BetaPolicy{Mode: messagesadapter.BetaModePassthrough, List: []string{"x"}}
	if err := SetAnthropicBetaPolicy(context.Background(), store, want); err != nil {
		t.Fatalf("set: %v", err)
	}
	got := GetAnthropicBetaPolicy(context.Background(), store)
	if got.Mode != messagesadapter.BetaModePassthrough {
		t.Fatalf("mode = %q, want passthrough", got.Mode)
	}
	// 写入后 description 也应落库(注册表说明)。
	if len(q.data[AnthropicBetaPolicyKey]) == 0 {
		t.Fatal("expected value persisted")
	}
}

func TestSetRejectsInvalidMode(t *testing.T) {
	store := newTestStore(newFakeQueries())
	err := store.Set(context.Background(), AnthropicBetaPolicyKey, json.RawMessage(`{"mode":"bogus","list":[]}`))
	if err == nil {
		t.Fatal("expected validation error for bad mode")
	}
}

func TestSetRejectsUnknownKey(t *testing.T) {
	store := newTestStore(newFakeQueries())
	err := store.Set(context.Background(), "does.not.exist", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error for unknown key")
	}
}

func TestLocalCacheAvoidsRepeatDBReads(t *testing.T) {
	q := newFakeQueries()
	store := newTestStore(q)
	ctx := context.Background()

	_ = store.Raw(ctx, AnthropicBetaPolicyKey)
	_ = store.Raw(ctx, AnthropicBetaPolicyKey)
	_ = store.Raw(ctx, AnthropicBetaPolicyKey)

	// redis=nil 时首个 Raw 打一次 DB,随后命中本地缓存(TTL 内)。
	if q.getHits != 1 {
		t.Fatalf("db getHits = %d, want 1 (local cache)", q.getHits)
	}
}

func TestRawFallsBackToDefaultOnDBError(t *testing.T) {
	q := newFakeQueries()
	q.getErr = context.DeadlineExceeded
	store := newTestStore(q)
	got := GetAnthropicBetaPolicy(context.Background(), store)
	if got.Mode != messagesadapter.DefaultBetaPolicy().Mode {
		t.Fatalf("mode = %q, want default on error", got.Mode)
	}
}

// TestSeedDefaultsFillsMissingKeepsExisting 验证启动 seed:缺行补默认、已有行不覆盖。
func TestSeedDefaultsFillsMissingKeepsExisting(t *testing.T) {
	q := newFakeQueries()
	// 预置一个「运维已改过」的值(非默认,42s=42000ms)。
	custom := []byte(`42000`)
	q.data[GatewayDefaultChannelTimeoutKey] = custom

	store := newTestStore(q)
	if err := store.SeedDefaults(context.Background()); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// 全部注册 key 都应有行。
	for _, def := range DefaultRegistry().List() {
		v, ok := q.data[def.Key]
		if !ok {
			t.Errorf("key %q not seeded", def.Key)
			continue
		}
		if def.Key == GatewayDefaultChannelTimeoutKey {
			if string(v) != string(custom) {
				t.Errorf("existing row overwritten: got %s, want %s", v, custom)
			}
			continue
		}
		if string(v) != string(def.Default) {
			t.Errorf("key %q seeded value = %s, want default %s", def.Key, v, def.Default)
		}
	}

	// 幂等:再跑一次不改变任何行。
	if err := store.SeedDefaults(context.Background()); err != nil {
		t.Fatalf("second seed: %v", err)
	}
	if string(q.data[GatewayDefaultChannelTimeoutKey]) != string(custom) {
		t.Fatal("idempotent seed must not overwrite")
	}
}

// TestSeedDefaultsReturnsErrorButContinues 验证 seed 失败不 panic、返回首错供观测。
func TestSeedDefaultsReturnsErrorButContinues(t *testing.T) {
	q := newFakeQueries()
	q.seedErr = context.DeadlineExceeded
	store := newTestStore(q)
	if err := store.SeedDefaults(context.Background()); err == nil {
		t.Fatal("expected first seed error to be returned")
	}
}

func TestListReportsRegistryMetadata(t *testing.T) {
	svc := NewService(newTestStore(newFakeQueries()))
	items := svc.List(context.Background())
	if len(items) == 0 {
		t.Fatal("expected at least one registered setting")
	}
	var found bool
	for _, it := range items {
		if it.Key == AnthropicBetaPolicyKey {
			found = true
			if it.Description == "" {
				t.Fatal("expected description in list item")
			}
			if !it.HotReload {
				t.Fatal("beta policy should be hot-reloadable")
			}
			if it.Source == "" {
				t.Fatal("expected effective source")
			}
		}
	}
	if !found {
		t.Fatalf("beta policy key not in list")
	}
}
