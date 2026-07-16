package sqlc_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

// TestProviderCRUDQueries 验证 admin provider CRUD 查询：创建、读取、更新、列出。
func TestProviderCRUDQueries(t *testing.T) {
	ctx, _, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	suffix := time.Now().UnixNano()
	slug := fmt.Sprintf("admin-prov-%d", suffix)

	created, err := queries.CreateProvider(ctx, sqlc.CreateProviderParams{
		Slug: slug, Name: "Admin Provider", Status: "enabled",
	})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	if created.ID == 0 || created.Slug != slug || created.Status != "enabled" {
		t.Fatalf("unexpected created provider: %+v", created)
	}

	got, err := queries.GetProvider(ctx, created.ID)
	if err != nil {
		t.Fatalf("get provider: %v", err)
	}
	if got.ID != created.ID || got.Slug != slug {
		t.Fatalf("unexpected provider read: %+v", got)
	}

	updated, err := queries.UpdateProvider(ctx, sqlc.UpdateProviderParams{
		ID: created.ID, Name: "Renamed", Status: "disabled",
	})
	if err != nil {
		t.Fatalf("update provider: %v", err)
	}
	if updated.Name != "Renamed" || updated.Status != "disabled" || updated.Slug != slug {
		t.Fatalf("unexpected updated provider: %+v", updated)
	}

	all, err := queries.ListProviders(ctx)
	if err != nil {
		t.Fatalf("list providers: %v", err)
	}
	found := false
	for _, p := range all {
		if p.ID == created.ID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected created provider %d in list", created.ID)
	}
}

// TestChannelCRUDQueries 验证 admin channel CRUD 查询：创建、读取、更新、轮换凭据、按 provider 列出。
func TestChannelCRUDQueries(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	suffix := time.Now().UnixNano()
	providerID := insertProvider(t, ctx, tx, fmt.Sprintf("admin-chan-prov-%d", suffix), "enabled")

	created, err := queries.CreateChannel(ctx, sqlc.CreateChannelParams{
		ProviderID: providerID,
		Name:       "primary",
		Protocol:   "openai",
		AdapterKey: "openai",
		BaseUrl:    "https://api.example.test/v1",
		Credential: "sk-admin-create",
		Status:     "enabled",
		Priority:   10,
		TimeoutMs:  pgtype.Int4{Int32: 15000, Valid: true},
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	if created.ID == 0 || created.ProviderID != providerID || !created.TimeoutMs.Valid || created.TimeoutMs.Int32 != 15000 {
		t.Fatalf("unexpected created channel: %+v", created)
	}
	// 渠道凭据明文存储（产品决策）：可回读。
	if created.Credential != "sk-admin-create" {
		t.Fatalf("expected plaintext credential persisted, got %q", created.Credential)
	}

	got, err := queries.GetChannel(ctx, created.ID)
	if err != nil {
		t.Fatalf("get channel: %v", err)
	}
	if got.ID != created.ID || got.Protocol != "openai" || got.AdapterKey != "openai" {
		t.Fatalf("unexpected channel read: %+v", got)
	}
	if got.Credential != "sk-admin-create" {
		t.Fatalf("expected plaintext credential readable, got %q", got.Credential)
	}

	updated, err := queries.UpdateChannel(ctx, sqlc.UpdateChannelParams{
		ID: created.ID, Name: "renamed", BaseUrl: "https://api2.example.test/v1",
		Status: "disabled", Priority: 20, TimeoutMs: pgtype.Int4{},
	})
	if err != nil {
		t.Fatalf("update channel: %v", err)
	}
	if updated.Name != "renamed" || updated.Status != "disabled" || updated.Priority != 20 || updated.TimeoutMs.Valid {
		t.Fatalf("unexpected updated channel: %+v", updated)
	}
	// protocol / adapter_key 不可在 UpdateChannel 修改，应保持原值。
	if updated.Protocol != "openai" || updated.AdapterKey != "openai" {
		t.Fatalf("update must not change protocol/adapter_key: %+v", updated)
	}

	affected, err := queries.UpdateChannelCredential(ctx, sqlc.UpdateChannelCredentialParams{
		ID: created.ID, Credential: "sk-admin-rotated",
	})
	if err != nil {
		t.Fatalf("rotate credential: %v", err)
	}
	if affected != 1 {
		t.Fatalf("expected 1 row affected on rotate, got %d", affected)
	}

	missing, err := queries.UpdateChannelCredential(ctx, sqlc.UpdateChannelCredentialParams{
		ID: -1, Credential: "sk-admin-rotated",
	})
	if err != nil {
		t.Fatalf("rotate missing credential: %v", err)
	}
	if missing != 0 {
		t.Fatalf("expected 0 rows affected for missing channel, got %d", missing)
	}

	byProvider, err := queries.ListChannelsByProvider(ctx, providerID)
	if err != nil {
		t.Fatalf("list channels by provider: %v", err)
	}
	if len(byProvider) != 1 || byProvider[0].ID != created.ID {
		t.Fatalf("expected 1 channel for provider, got %#v", byProvider)
	}
}
