package workers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/appsettings"
)

// memSettingsQueries 是内存版 app_settings（单测用），满足 appsettings.Queries。
type memSettingsQueries struct {
	data map[string][]byte
}

func (m *memSettingsQueries) GetAppSetting(_ context.Context, key string) ([]byte, error) {
	v, ok := m.data[key]
	if !ok {
		return nil, pgx.ErrNoRows
	}
	return v, nil
}

func (m *memSettingsQueries) GetAppSettingRecord(ctx context.Context, key string) (sqlc.GetAppSettingRecordRow, error) {
	v, err := m.GetAppSetting(ctx, key)
	if err != nil {
		return sqlc.GetAppSettingRecordRow{}, err
	}
	return sqlc.GetAppSettingRecordRow{Key: key, Value: v, Revision: 1}, nil
}

func (m *memSettingsQueries) UpsertAppSetting(_ context.Context, arg sqlc.UpsertAppSettingParams) error {
	m.data[arg.Key] = arg.Value
	return nil
}

func (m *memSettingsQueries) SeedAppSetting(_ context.Context, arg sqlc.SeedAppSettingParams) error {
	if _, ok := m.data[arg.Key]; !ok {
		m.data[arg.Key] = arg.Value
	}
	return nil
}

func newCodexSyncWorkerForTest(t *testing.T, handler http.HandlerFunc) (*CodexClientVersionSyncWorker, *appsettings.SettingsStore, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	store := appsettings.NewSettingsStore(&memSettingsQueries{data: map[string][]byte{}}, nil, "test", appsettings.DefaultRegistry(), nil)
	worker := NewCodexClientVersionSyncWorker(server.Client(), store, nil)
	worker.endpoint = server.URL + "/releases"
	return worker, store, server
}

// TestCodexClientVersionSyncWritesLatestStable 冻结主路径：/latest 是 rust-v 正式版即写入同步快照，
// 之后在周期内不再重复请求。
func TestCodexClientVersionSyncWritesLatestStable(t *testing.T) {
	calls := 0
	worker, store, _ := newCodexSyncWorkerForTest(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/releases/latest" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"tag_name":"rust-v0.160.2","draft":false,"prerelease":false}`))
	})
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	worker.now = func() time.Time { return now }

	worked, err := worker.RunOnce(context.Background())
	if err != nil || !worked {
		t.Fatalf("RunOnce = (%v, %v), want (true, nil)", worked, err)
	}
	synced, err := appsettings.DecodeCodexClientVersionSynced(store.Raw(context.Background(), appsettings.GatewayCodexClientVersionSyncedKey))
	if err != nil {
		t.Fatalf("decode synced: %v", err)
	}
	if synced.Version != "0.160.2" || !synced.SyncedAt.Equal(now) || synced.Source != codexClientVersionSource {
		t.Fatalf("unexpected snapshot %+v", synced)
	}
	if got := appsettings.GatewayCodexClientVersion(context.Background(), store); got != "0.160.2" {
		t.Fatalf("effective version = %q, want synced 0.160.2", got)
	}

	// 周期内再跑不请求。
	if worked, _ := worker.RunOnce(context.Background()); worked || calls != 1 {
		t.Fatalf("second run within interval must be a no-op, worked=%v calls=%d", worked, calls)
	}
}

// TestCodexClientVersionSyncFallsBackToReleaseList 冻结回退路径：/latest 不是客户端 tag 时翻列表，
// 跳过预发布与非 rust-v tag，取首个正式版。
func TestCodexClientVersionSyncFallsBackToReleaseList(t *testing.T) {
	worker, store, _ := newCodexSyncWorkerForTest(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases/latest":
			_, _ = w.Write([]byte(`{"tag_name":"rusty-v8-1.0.0","draft":false,"prerelease":false}`))
		case "/releases":
			if r.URL.Query().Get("per_page") == "" {
				t.Errorf("list fallback must page explicitly")
			}
			_, _ = w.Write([]byte(`[
				{"tag_name":"rust-v0.161.0-alpha.2","draft":false,"prerelease":true},
				{"tag_name":"rusty-v8-1.0.0","draft":false,"prerelease":false},
				{"tag_name":"rust-v0.160.9","draft":true,"prerelease":false},
				{"tag_name":"rust-v0.160.8","draft":false,"prerelease":false},
				{"tag_name":"rust-v0.160.7","draft":false,"prerelease":false}
			]`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	})
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	synced, _ := appsettings.DecodeCodexClientVersionSynced(store.Raw(context.Background(), appsettings.GatewayCodexClientVersionSyncedKey))
	if synced.Version != "0.160.8" {
		t.Fatalf("synced version = %q, want first stable rust-v 0.160.8", synced.Version)
	}
}

// TestCodexClientVersionSyncKeepsPreviousOnFailure 冻结失败语义：GitHub 出错时保留旧快照、
// 不返回错误（避免 runner 反复告警），并按较短间隔重试。
func TestCodexClientVersionSyncKeepsPreviousOnFailure(t *testing.T) {
	worker, store, _ := newCodexSyncWorkerForTest(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	// 上次同步已超过一个周期，本轮到期。
	previous := appsettings.EncodeCodexClientVersionSynced(appsettings.CodexClientVersionSynced{Version: "0.155.0", SyncedAt: now.Add(-7 * time.Hour), Source: codexClientVersionSource})
	if err := store.Set(context.Background(), appsettings.GatewayCodexClientVersionSyncedKey, previous); err != nil {
		t.Fatalf("seed previous: %v", err)
	}
	worker.now = func() time.Time { return now }

	worked, err := worker.RunOnce(context.Background())
	if err != nil || !worked {
		t.Fatalf("RunOnce = (%v, %v), want (true, nil)", worked, err)
	}
	synced, _ := appsettings.DecodeCodexClientVersionSynced(store.Raw(context.Background(), appsettings.GatewayCodexClientVersionSyncedKey))
	if synced.Version != "0.155.0" {
		t.Fatalf("failure must keep previous snapshot, got %+v", synced)
	}
	if !worker.nextRunAt.Equal(now.Add(codexClientVersionSyncRetry)) {
		t.Fatalf("failure must schedule the short retry, next=%v", worker.nextRunAt)
	}
}

// TestCodexClientVersionSyncSkipsStartupWhenSnapshotFresh 冻结启动节流：持久化快照仍在周期内时，
// 进程重启不再立刻打 GitHub，而是顺延到 synced_at + 周期（热加载/滚动重启不会耗光匿名限额）。
func TestCodexClientVersionSyncSkipsStartupWhenSnapshotFresh(t *testing.T) {
	calls := 0
	worker, store, _ := newCodexSyncWorkerForTest(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"tag_name":"rust-v0.160.2"}`))
	})
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	syncedAt := now.Add(-2 * time.Hour)
	fresh := appsettings.EncodeCodexClientVersionSynced(appsettings.CodexClientVersionSynced{Version: "0.159.0", SyncedAt: syncedAt, Source: codexClientVersionSource})
	if err := store.Set(context.Background(), appsettings.GatewayCodexClientVersionSyncedKey, fresh); err != nil {
		t.Fatalf("seed fresh snapshot: %v", err)
	}
	worker.now = func() time.Time { return now }

	worked, err := worker.RunOnce(context.Background())
	if err != nil || worked || calls != 0 {
		t.Fatalf("fresh snapshot must defer the startup sync: worked=%v calls=%d err=%v", worked, calls, err)
	}
	if !worker.nextRunAt.Equal(syncedAt.Add(codexClientVersionSyncInterval)) {
		t.Fatalf("next run must be synced_at + interval, got %v", worker.nextRunAt)
	}

	// 到期后正常同步。
	worker.now = func() time.Time { return syncedAt.Add(codexClientVersionSyncInterval + time.Second) }
	if worked, err := worker.RunOnce(context.Background()); err != nil || !worked || calls != 1 {
		t.Fatalf("due sync must run: worked=%v calls=%d err=%v", worked, calls, err)
	}
}

// TestCodexClientVersionSyncRespectsDisabledSwitch 冻结开关：自动同步关闭时不请求 GitHub。
func TestCodexClientVersionSyncRespectsDisabledSwitch(t *testing.T) {
	calls := 0
	worker, store, _ := newCodexSyncWorkerForTest(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"tag_name":"rust-v0.160.2"}`))
	})
	if err := store.Set(context.Background(), appsettings.GatewayCodexClientVersionAutoSyncKey, json.RawMessage("false")); err != nil {
		t.Fatalf("disable auto sync: %v", err)
	}
	worked, err := worker.RunOnce(context.Background())
	if err != nil || worked || calls != 0 {
		t.Fatalf("disabled sync must not call GitHub: worked=%v calls=%d err=%v", worked, calls, err)
	}
}
