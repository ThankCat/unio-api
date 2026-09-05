package appsettings

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/core/codexidentity"
)

func newCodexVersionTestStore(t *testing.T) *SettingsStore {
	t.Helper()
	return NewSettingsStore(newFakeQueries(), nil, "test", DefaultRegistry(), nil)
}

// TestGatewayCodexClientVersionPrecedence 冻结生效版本的解析链：覆写 → 自动同步值 → 基线，
// 关闭自动同步时忽略同步值，低于基线的值回落基线。
func TestGatewayCodexClientVersionPrecedence(t *testing.T) {
	ctx := context.Background()
	store := newCodexVersionTestStore(t)

	if got := GatewayCodexClientVersion(ctx, store); got != codexidentity.BaselineVersion {
		t.Fatalf("fresh store must yield baseline, got %q", got)
	}

	synced := EncodeCodexClientVersionSynced(CodexClientVersionSynced{Version: "0.160.0", SyncedAt: time.Now(), Source: "github:openai/codex"})
	if err := store.Set(ctx, GatewayCodexClientVersionSyncedKey, synced); err != nil {
		t.Fatalf("set synced: %v", err)
	}
	if got := GatewayCodexClientVersion(ctx, store); got != "0.160.0" {
		t.Fatalf("synced version must apply, got %q", got)
	}

	if err := store.Set(ctx, GatewayCodexClientVersionAutoSyncKey, json.RawMessage("false")); err != nil {
		t.Fatalf("set auto sync: %v", err)
	}
	if got := GatewayCodexClientVersion(ctx, store); got != codexidentity.BaselineVersion {
		t.Fatalf("auto sync off must ignore synced value, got %q", got)
	}

	if err := store.Set(ctx, GatewayCodexClientVersionKey, json.RawMessage(`"0.155.0"`)); err != nil {
		t.Fatalf("set override: %v", err)
	}
	if got := GatewayCodexClientVersion(ctx, store); got != "0.155.0" {
		t.Fatalf("override must win, got %q", got)
	}

	if err := store.Set(ctx, GatewayCodexClientVersionKey, json.RawMessage(`"0.100.0"`)); err != nil {
		t.Fatalf("set stale override: %v", err)
	}
	if got := GatewayCodexClientVersion(ctx, store); got != codexidentity.BaselineVersion {
		t.Fatalf("override below baseline must floor, got %q", got)
	}
}

// TestCodexClientVersionValidation 冻结写入校验：覆写只接受空串或官方形态；同步快照拒绝未知字段与非法版本；
// 同步快照是只读项，通用写入口拒绝。
func TestCodexClientVersionValidation(t *testing.T) {
	ctx := context.Background()
	store := newCodexVersionTestStore(t)

	for _, ok := range []string{`""`, `"0.152.1"`, `" 0.153.0-alpha.5 "`} {
		if err := store.Set(ctx, GatewayCodexClientVersionKey, json.RawMessage(ok)); err != nil {
			t.Fatalf("override %s should be accepted: %v", ok, err)
		}
	}
	for _, bad := range []string{`"v1"`, `"0.152.1; x"`, `123`, `null`} {
		if err := store.Set(ctx, GatewayCodexClientVersionKey, json.RawMessage(bad)); err == nil {
			t.Fatalf("override %s should be rejected", bad)
		}
	}

	if err := store.Set(ctx, GatewayCodexClientVersionSyncedKey, json.RawMessage(`{"version":"x","synced_at":"2026-09-05T00:00:00Z","source":"s"}`)); err == nil {
		t.Fatal("invalid synced version must be rejected")
	}
	if err := store.Set(ctx, GatewayCodexClientVersionSyncedKey, json.RawMessage(`{"version":"0.152.1","synced_at":"2026-09-05T00:00:00Z","source":"s","extra":1}`)); err == nil {
		t.Fatal("unknown fields in synced snapshot must be rejected")
	}
	if err := store.Set(ctx, GatewayCodexClientVersionSyncedKey, json.RawMessage(`null`)); err != nil {
		t.Fatalf("null snapshot must be accepted: %v", err)
	}

	svc := NewService(store)
	if _, err := svc.SetRawWithResult(ctx, GatewayCodexClientVersionSyncedKey, json.RawMessage(`null`)); err == nil {
		t.Fatal("generic write path must reject the read-only synced snapshot")
	}
	found := false
	for _, item := range svc.List(ctx) {
		if item.Key == GatewayCodexClientVersionSyncedKey {
			found = true
			if !item.ReadOnly {
				t.Fatal("synced snapshot must be listed as read-only")
			}
		}
	}
	if !found {
		t.Fatal("synced snapshot must still be visible in the generic list")
	}
}
