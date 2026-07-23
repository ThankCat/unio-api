package sqlc_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
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
	endpointID := insertProviderEndpoint(t, ctx, tx, providerID, "primary-ep", fmt.Sprintf("https://api-%d.example.test", suffix), "enabled")
	endpoint2ID := insertProviderEndpoint(t, ctx, tx, providerID, "secondary-ep", fmt.Sprintf("https://api2-%d.example.test", suffix), "enabled")

	created, err := queries.CreateChannel(ctx, sqlc.CreateChannelParams{
		ProviderID:         providerID,
		ProviderEndpointID: endpointID,
		Name:               "primary",
		Protocol:           "openai",
		AdapterKey:         "openai",
		Credential:         "sk-admin-create",
		Status:             "enabled",
		Priority:           10,
		TimeoutMs:          pgtype.Int4{Int32: 15000, Valid: true},
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
		ID: created.ID, Name: "renamed", ProviderEndpointID: endpoint2ID,
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

	limited, err := queries.CommitChannelAdmissionLimitsAtRevision(ctx, sqlc.CommitChannelAdmissionLimitsAtRevisionParams{
		RpmLimit:         pgtype.Int4{Int32: 30, Valid: true},
		TpmLimit:         pgtype.Int4{},
		RpdLimit:         pgtype.Int4{Int32: 0, Valid: true},
		ConcurrencyLimit: pgtype.Int4{Int32: 2, Valid: true},
		NextRevision:     updated.AdmissionLimitsRevision + 1,
		ID:               updated.ID,
		CurrentRevision:  updated.AdmissionLimitsRevision,
	})
	if err != nil {
		t.Fatalf("commit channel admission limits: %v", err)
	}
	if limited.AdmissionLimitsRevision != updated.AdmissionLimitsRevision+1 || limited.RpmLimit.Int32 != 30 ||
		!limited.RpdLimit.Valid || limited.RpdLimit.Int32 != 0 || limited.TpmLimit.Valid || limited.ConcurrencyLimit.Int32 != 2 {
		t.Fatalf("unexpected committed admission limits: %+v", limited)
	}
	_, err = queries.CommitChannelAdmissionLimitsAtRevision(ctx, sqlc.CommitChannelAdmissionLimitsAtRevisionParams{
		RpmLimit:         limited.RpmLimit,
		TpmLimit:         limited.TpmLimit,
		RpdLimit:         limited.RpdLimit,
		ConcurrencyLimit: limited.ConcurrencyLimit,
		NextRevision:     limited.AdmissionLimitsRevision + 1,
		ID:               limited.ID,
		CurrentRevision:  limited.AdmissionLimitsRevision,
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("semantic no-op must not increment admission revision, got %v", err)
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

// TestChannelCredentialRotationRevisionCAS 验证 credential PUT 的保存与检测边界：
// 真变化只推进一次版本；同值重试不推进；Endpoint 版本变化后的迟到检测只能记 stale 日志。
func TestChannelCredentialRotationRevisionCAS(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	suffix := time.Now().UnixNano()
	providerID := insertProvider(t, ctx, tx, fmt.Sprintf("credential-cas-%d", suffix), "enabled")
	endpointID := insertProviderEndpoint(t, ctx, tx, providerID, "credential-cas", fmt.Sprintf("https://credential-%d.example.test", suffix), "enabled")
	created, err := queries.CreateChannel(ctx, sqlc.CreateChannelParams{
		ProviderID: providerID, ProviderEndpointID: endpointID, Name: "credential-cas",
		Protocol: "openai", AdapterKey: "openai", Credential: "sk-old", Status: "enabled", Priority: 1,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}

	prepared, err := queries.PrepareChannelCredentialRotation(ctx, sqlc.PrepareChannelCredentialRotationParams{
		ChannelID: created.ID, Credential: "sk-new",
	})
	if err != nil {
		t.Fatalf("prepare credential rotation: %v", err)
	}
	if !prepared.CredentialChanged || prepared.CredentialValid || prepared.ConfigRevision != created.ConfigRevision+1 {
		t.Fatalf("unexpected prepared rotation: %+v", prepared)
	}
	retried, err := queries.PrepareChannelCredentialRotation(ctx, sqlc.PrepareChannelCredentialRotationParams{
		ChannelID: created.ID, Credential: "sk-new",
	})
	if err != nil {
		t.Fatalf("retry same credential: %v", err)
	}
	if retried.CredentialChanged || retried.ConfigRevision != prepared.ConfigRevision || retried.CredentialValid {
		t.Fatalf("same invalid credential must retry verification without revision bump: %+v", retried)
	}

	applied, err := queries.ApplyChannelProbeResult(ctx, sqlc.ApplyChannelProbeResultParams{
		ChannelID: created.ID, ExpectedConfigRevision: prepared.ConfigRevision,
		ExpectedEndpointBaseUrlRevision: prepared.EndpointBaseUrlRevision,
		ExpectedEndpointStatusRevision:  prepared.EndpointStatusRevision,
		Success:                         pgtype.Bool{Bool: true, Valid: true},
		LastTestLatencyMs:               pgtype.Int4{Int32: 120, Valid: true},
		NextCredentialValid:             pgtype.Bool{Bool: true, Valid: true},
		Source:                          "credential_rotate", TestedModel: pgtype.Text{String: "gpt-test", Valid: true},
	})
	if err != nil {
		t.Fatalf("apply current probe result: %v", err)
	}
	if !applied.ResultApplied || !applied.StateChangeApplied || !applied.CredentialValidAfter || applied.CurrentConfigRevision != prepared.ConfigRevision+1 {
		t.Fatalf("current successful result must restore credential and bump revision: %+v", applied)
	}

	second, err := queries.PrepareChannelCredentialRotation(ctx, sqlc.PrepareChannelCredentialRotationParams{
		ChannelID: created.ID, Credential: "sk-second",
	})
	if err != nil {
		t.Fatalf("prepare second rotation: %v", err)
	}
	if !second.CredentialChanged || second.CredentialValid {
		t.Fatalf("second rotation must make credential unroutable before probe: %+v", second)
	}
	if _, err := tx.Exec(ctx, `UPDATE provider_endpoints SET base_url_revision = base_url_revision + 1 WHERE id = $1`, endpointID); err != nil {
		t.Fatalf("advance endpoint revision: %v", err)
	}

	stale, err := queries.ApplyChannelProbeResult(ctx, sqlc.ApplyChannelProbeResultParams{
		ChannelID: created.ID, ExpectedConfigRevision: second.ConfigRevision,
		ExpectedEndpointBaseUrlRevision: second.EndpointBaseUrlRevision,
		ExpectedEndpointStatusRevision:  second.EndpointStatusRevision,
		Success:                         pgtype.Bool{Bool: true, Valid: true},
		LastTestLatencyMs:               pgtype.Int4{Int32: 95, Valid: true},
		NextCredentialValid:             pgtype.Bool{Bool: true, Valid: true},
		Source:                          "credential_rotate", TestedModel: pgtype.Text{String: "gpt-test", Valid: true},
	})
	if err != nil {
		t.Fatalf("apply stale probe result: %v", err)
	}
	if stale.ResultApplied || stale.StateChangeApplied || stale.CredentialValidAfter || stale.CurrentConfigRevision != second.ConfigRevision {
		t.Fatalf("stale result must not restore current credential: %+v", stale)
	}
	current, err := queries.GetChannel(ctx, created.ID)
	if err != nil {
		t.Fatalf("get current channel: %v", err)
	}
	if current.CredentialValid || current.LastTestedAt.Valid || current.ConfigRevision != second.ConfigRevision {
		t.Fatalf("stale result changed current channel summary/state: %+v", current)
	}
	logs, err := queries.ListChannelTestLogsByChannel(ctx, sqlc.ListChannelTestLogsByChannelParams{
		ChannelID: created.ID, PageLimit: 10, PageOffset: 0,
	})
	if err != nil || len(logs) != 2 {
		t.Fatalf("list credential logs: len=%d err=%v", len(logs), err)
	}
	latest := logs[0]
	if latest.StateChangeApplied || !latest.TestedConfigRevision.Valid || latest.TestedConfigRevision.Int64 != second.ConfigRevision ||
		!latest.TestedEndpointBaseUrlRevision.Valid || latest.TestedEndpointBaseUrlRevision.Int64 != second.EndpointBaseUrlRevision {
		t.Fatalf("stale log did not preserve tested revisions: %+v", latest)
	}
}
