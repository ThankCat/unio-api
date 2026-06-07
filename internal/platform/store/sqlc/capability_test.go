package sqlc_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// insertCapabilityModel 插入一条带 Layer 1 元数据列的模型，返回 models.id。
func insertCapabilityModel(t *testing.T, ctx context.Context, tx pgx.Tx, suffix int64) int64 {
	t.Helper()

	modelID := fmt.Sprintf("openai/capability-model-%d", suffix)
	canonicalID := fmt.Sprintf("openai/capability-canonical-%d", suffix)

	var id int64
	err := tx.QueryRow(ctx, `
		INSERT INTO models (
			model_id, display_name, owned_by, status,
			canonical_id, lab, context_window_tokens, max_output_tokens,
			input_price_usd_per_million_tokens, output_price_usd_per_million_tokens,
			release_date, source
		)
		VALUES ($1, $2, $3, 'enabled', $4, 'openai', 200000, 8192, 2.5, 10, '2026-01-15', 'manual')
		RETURNING id
	`, modelID, modelID, "openai", canonicalID).Scan(&id)
	if err != nil {
		t.Fatalf("insert capability model: %v", err)
	}

	return id
}

func TestModelLookupReadsLayer1Columns(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	suffix := time.Now().UnixNano()
	id := insertCapabilityModel(t, ctx, tx, suffix)

	model, err := queries.LookupModelByID(ctx, id)
	if err != nil {
		t.Fatalf("lookup model by id: %v", err)
	}

	if !model.CanonicalID.Valid || model.CanonicalID.String == "" {
		t.Fatal("expected canonical_id to be set")
	}
	if !model.Lab.Valid || model.Lab.String != "openai" {
		t.Fatalf("expected lab openai, got valid=%v %q", model.Lab.Valid, model.Lab.String)
	}
	if !model.ContextWindowTokens.Valid || model.ContextWindowTokens.Int64 != 200000 {
		t.Fatalf("expected context_window_tokens 200000, got valid=%v %d", model.ContextWindowTokens.Valid, model.ContextWindowTokens.Int64)
	}
	if !model.MaxOutputTokens.Valid || model.MaxOutputTokens.Int64 != 8192 {
		t.Fatalf("expected max_output_tokens 8192, got valid=%v %d", model.MaxOutputTokens.Valid, model.MaxOutputTokens.Int64)
	}
	if !model.ReleaseDate.Valid {
		t.Fatal("expected release_date to be set")
	}
	if model.Source != "manual" {
		t.Fatalf("expected source manual, got %q", model.Source)
	}
	if model.RemovedUpstreamAt.Valid {
		t.Fatal("expected removed_upstream_at to be null")
	}

	byModelID, err := queries.LookupModelByModelID(ctx, model.ModelID)
	if err != nil {
		t.Fatalf("lookup model by model id: %v", err)
	}
	if byModelID.ID != id {
		t.Fatalf("expected same model id %d, got %d", id, byModelID.ID)
	}
}

func TestModelLookupNotFound(t *testing.T) {
	ctx, _, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	_, err := queries.LookupModelByModelID(ctx, fmt.Sprintf("openai/does-not-exist-%d", time.Now().UnixNano()))
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expected pgx.ErrNoRows, got %v", err)
	}
}

func TestModelDefaultSourceIsManual(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	suffix := time.Now().UnixNano()
	id := insertModel(t, ctx, tx, fmt.Sprintf("openai/default-source-%d", suffix), "openai", "enabled")

	model, err := queries.LookupModelByID(ctx, id)
	if err != nil {
		t.Fatalf("lookup model: %v", err)
	}
	if model.Source != "manual" {
		t.Fatalf("expected default source manual, got %q", model.Source)
	}
	if model.CanonicalID.Valid {
		t.Fatal("expected canonical_id to default to null")
	}
}

func TestModelCapabilityUpsertListAndReverseLookup(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	suffix := time.Now().UnixNano()
	modelID := insertCapabilityModel(t, ctx, tx, suffix)

	inserted, err := queries.UpsertModelCapability(ctx, sqlc.UpsertModelCapabilityParams{
		ModelID:       modelID,
		CapabilityKey: "reasoning.effort",
		SupportLevel:  "limited",
		Limits:        []byte(`{"effort":["high","max"]}`),
		Source:        "manual",
		UpdatedBy:     pgtype.Text{String: "admin", Valid: true},
	})
	if err != nil {
		t.Fatalf("insert model capability: %v", err)
	}
	if inserted.SupportLevel != "limited" {
		t.Fatalf("expected limited, got %q", inserted.SupportLevel)
	}

	var limits map[string][]string
	if err := json.Unmarshal(inserted.Limits, &limits); err != nil {
		t.Fatalf("unmarshal limits: %v", err)
	}
	if len(limits["effort"]) != 2 {
		t.Fatalf("expected 2 effort values, got %#v", limits)
	}

	updated, err := queries.UpsertModelCapability(ctx, sqlc.UpsertModelCapabilityParams{
		ModelID:       modelID,
		CapabilityKey: "reasoning.effort",
		SupportLevel:  "full",
		Limits:        nil,
		Source:        "models_dev",
		UpdatedBy:     pgtype.Text{Valid: false},
	})
	if err != nil {
		t.Fatalf("upsert model capability conflict: %v", err)
	}
	if updated.SupportLevel != "full" {
		t.Fatalf("expected conflict upsert to set full, got %q", updated.SupportLevel)
	}
	if updated.Source != "models_dev" {
		t.Fatalf("expected source models_dev after upsert, got %q", updated.Source)
	}
	if len(updated.Limits) != 0 {
		t.Fatalf("expected limits cleared to null, got %s", updated.Limits)
	}

	caps, err := queries.ListModelCapabilities(ctx, modelID)
	if err != nil {
		t.Fatalf("list model capabilities: %v", err)
	}
	if len(caps) != 1 {
		t.Fatalf("expected 1 capability after conflict upsert, got %d", len(caps))
	}

	byCap, err := queries.ListModelsByCapability(ctx, "reasoning.effort")
	if err != nil {
		t.Fatalf("list models by capability: %v", err)
	}
	found := false
	for _, row := range byCap {
		if row.ModelID == modelID {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected model %d in reverse capability lookup", modelID)
	}
}

func TestModelCapabilityRejectsInvalidSupportLevel(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	modelID := insertCapabilityModel(t, ctx, tx, time.Now().UnixNano())

	_, err := queries.UpsertModelCapability(ctx, sqlc.UpsertModelCapabilityParams{
		ModelID:       modelID,
		CapabilityKey: "text.input",
		SupportLevel:  "bogus",
		Source:        "manual",
	})
	if err == nil {
		t.Fatal("expected check violation for invalid support level")
	}
	if !isCheckViolation(err) {
		t.Fatalf("expected check violation, got %v", err)
	}
}

func TestModelCapabilityCascadeOnModelDelete(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	modelID := insertCapabilityModel(t, ctx, tx, time.Now().UnixNano())
	if _, err := queries.UpsertModelCapability(ctx, sqlc.UpsertModelCapabilityParams{
		ModelID:       modelID,
		CapabilityKey: "text.output",
		SupportLevel:  "full",
		Source:        "manual",
	}); err != nil {
		t.Fatalf("insert model capability: %v", err)
	}

	if _, err := tx.Exec(ctx, `DELETE FROM models WHERE id = $1`, modelID); err != nil {
		t.Fatalf("delete model: %v", err)
	}

	caps, err := queries.ListModelCapabilities(ctx, modelID)
	if err != nil {
		t.Fatalf("list model capabilities after delete: %v", err)
	}
	if len(caps) != 0 {
		t.Fatalf("expected capabilities cascade-deleted, got %d", len(caps))
	}
}

func TestChannelOverrideRejectsFullSupportLevel(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	suffix := time.Now().UnixNano()
	providerID := insertProvider(t, ctx, tx, fmt.Sprintf("cap-override-provider-%d", suffix), "enabled")
	channelID := insertChannel(t, ctx, tx, providerID, fmt.Sprintf("cap-override-channel-%d", suffix), "enabled", 10, nil)

	_, err := queries.UpsertChannelOverride(ctx, sqlc.UpsertChannelOverrideParams{
		ChannelID:     channelID,
		CapabilityKey: "tools.function",
		SupportLevel:  "full",
	})
	if err == nil {
		t.Fatal("expected check violation rejecting full on channel override")
	}
	if !isCheckViolation(err) {
		t.Fatalf("expected check violation, got %v", err)
	}
}

func TestChannelOverrideUpsertListDeleteAndCascade(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	suffix := time.Now().UnixNano()
	providerID := insertProvider(t, ctx, tx, fmt.Sprintf("cap-override-provider-%d", suffix), "enabled")
	channelID := insertChannel(t, ctx, tx, providerID, fmt.Sprintf("cap-override-channel-%d", suffix), "enabled", 10, nil)

	override, err := queries.UpsertChannelOverride(ctx, sqlc.UpsertChannelOverrideParams{
		ChannelID:     channelID,
		CapabilityKey: "tools.builtin.web_search",
		SupportLevel:  "unsupported",
		Reason:        pgtype.Text{String: "upstream lacks web search", Valid: true},
	})
	if err != nil {
		t.Fatalf("upsert channel override: %v", err)
	}
	if override.SupportLevel != "unsupported" {
		t.Fatalf("expected unsupported, got %q", override.SupportLevel)
	}

	list, err := queries.ListChannelOverrides(ctx, channelID)
	if err != nil {
		t.Fatalf("list channel overrides: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 override, got %d", len(list))
	}

	if err := queries.DeleteChannelOverride(ctx, sqlc.DeleteChannelOverrideParams{
		ChannelID:     channelID,
		CapabilityKey: "tools.builtin.web_search",
	}); err != nil {
		t.Fatalf("delete channel override: %v", err)
	}
	list, err = queries.ListChannelOverrides(ctx, channelID)
	if err != nil {
		t.Fatalf("list channel overrides after delete: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected override deleted, got %d", len(list))
	}

	if _, err := queries.UpsertChannelOverride(ctx, sqlc.UpsertChannelOverrideParams{
		ChannelID:     channelID,
		CapabilityKey: "tools.parallel",
		SupportLevel:  "limited",
	}); err != nil {
		t.Fatalf("re-insert channel override: %v", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM channels WHERE id = $1`, channelID); err != nil {
		t.Fatalf("delete channel: %v", err)
	}
	list, err = queries.ListChannelOverrides(ctx, channelID)
	if err != nil {
		t.Fatalf("list channel overrides after channel delete: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected overrides cascade-deleted, got %d", len(list))
	}
}

func TestSyncJobLifecycleSucceeded(t *testing.T) {
	ctx, _, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	job, err := queries.CreateSyncJob(ctx, "models_dev")
	if err != nil {
		t.Fatalf("create sync job: %v", err)
	}
	if job.Status != "pending" {
		t.Fatalf("expected pending, got %q", job.Status)
	}

	running, err := queries.MarkSyncJobRunning(ctx, job.ID)
	if err != nil {
		t.Fatalf("mark running: %v", err)
	}
	if running.Status != "running" || !running.StartedAt.Valid {
		t.Fatalf("expected running with started_at, got %q valid=%v", running.Status, running.StartedAt.Valid)
	}

	succeeded, err := queries.MarkSyncJobSucceeded(ctx, sqlc.MarkSyncJobSucceededParams{
		StatsJson: []byte(`{"upserted":3}`),
		ID:        job.ID,
	})
	if err != nil {
		t.Fatalf("mark succeeded: %v", err)
	}
	if succeeded.Status != "succeeded" || !succeeded.FinishedAt.Valid {
		t.Fatalf("expected succeeded with finished_at, got %q valid=%v", succeeded.Status, succeeded.FinishedAt.Valid)
	}

	latest, err := queries.GetLatestSyncJob(ctx, "models_dev")
	if err != nil {
		t.Fatalf("get latest sync job: %v", err)
	}
	if latest.ID != job.ID {
		t.Fatalf("expected latest job %d, got %d", job.ID, latest.ID)
	}
}

func TestSyncJobMarkRunningRequiresPending(t *testing.T) {
	ctx, _, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	job, err := queries.CreateSyncJob(ctx, "manual")
	if err != nil {
		t.Fatalf("create sync job: %v", err)
	}
	if _, err := queries.MarkSyncJobRunning(ctx, job.ID); err != nil {
		t.Fatalf("first mark running: %v", err)
	}

	if _, err := queries.MarkSyncJobRunning(ctx, job.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expected pgx.ErrNoRows marking already-running job, got %v", err)
	}
}

func TestSyncJobFailedFromPending(t *testing.T) {
	ctx, _, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	job, err := queries.CreateSyncJob(ctx, "models_dev")
	if err != nil {
		t.Fatalf("create sync job: %v", err)
	}

	failed, err := queries.MarkSyncJobFailed(ctx, sqlc.MarkSyncJobFailedParams{
		ErrorText: pgtype.Text{String: "boom", Valid: true},
		ID:        job.ID,
	})
	if err != nil {
		t.Fatalf("mark failed: %v", err)
	}
	if failed.Status != "failed" || !failed.ErrorText.Valid {
		t.Fatalf("expected failed with error_text, got %q valid=%v", failed.Status, failed.ErrorText.Valid)
	}
}

func TestSyncJobRejectsInvalidEnums(t *testing.T) {
	ctx, _, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	if _, err := queries.CreateSyncJob(ctx, "adapter_seed"); err == nil || !isCheckViolation(err) {
		t.Fatalf("expected check violation for invalid sync job source, got %v", err)
	}
}
