package capability_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/ThankCat/unio-api/internal/core/capability"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// newCapabilityStoreTx 创建带回滚事务的 capability.Store，避免污染本地库；未配置 DATABASE_URL 时跳过。
func newCapabilityStoreTx(t *testing.T) (context.Context, pgx.Tx, capability.Store, func()) {
	t.Helper()

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		cancel()
		t.Fatalf("create postgres pool: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		cancel()
		t.Fatalf("ping postgres: %v", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		pool.Close()
		cancel()
		t.Fatalf("begin transaction: %v", err)
	}

	cleanup := func() {
		_ = tx.Rollback(context.Background())
		pool.Close()
		cancel()
	}

	return ctx, tx, capability.NewStore(sqlc.New(tx)), cleanup
}

func insertModelRow(t *testing.T, ctx context.Context, tx pgx.Tx, suffix int64) int64 {
	t.Helper()

	modelID := fmt.Sprintf("openai/cap-store-model-%d", suffix)
	var id int64
	err := tx.QueryRow(ctx, `
		INSERT INTO models (
			model_id, display_name, owned_by, status,
			context_window_tokens, max_output_tokens,
			input_price_usd_per_million_tokens, output_price_usd_per_million_tokens,
			source
		)
		VALUES ($1, $2, 'openai', 'enabled', 128000, 4096, 2.5, 10, 'manual')
		RETURNING id
	`, modelID, modelID).Scan(&id)
	if err != nil {
		t.Fatalf("insert model row: %v", err)
	}

	return id
}

func TestStoreLookupModelMapsLayer1(t *testing.T) {
	ctx, tx, store, cleanup := newCapabilityStoreTx(t)
	defer cleanup()

	suffix := time.Now().UnixNano()
	id := insertModelRow(t, ctx, tx, suffix)

	model, err := store.LookupModelByID(ctx, id)
	if err != nil {
		t.Fatalf("lookup model by id: %v", err)
	}

	if model.MaxOutputTokens == nil || *model.MaxOutputTokens != 4096 {
		t.Fatalf("expected max_output_tokens 4096, got %v", model.MaxOutputTokens)
	}
	if model.ContextWindowTokens == nil || *model.ContextWindowTokens != 128000 {
		t.Fatalf("expected context_window 128000, got %v", model.ContextWindowTokens)
	}
	if model.InputPriceUSDPerMTokens == nil || *model.InputPriceUSDPerMTokens != "2.5000000000" {
		t.Fatalf("expected input price 2.5000000000, got %v", model.InputPriceUSDPerMTokens)
	}
	if model.OutputPriceUSDPerMTokens == nil || *model.OutputPriceUSDPerMTokens != "10.0000000000" {
		t.Fatalf("expected output price 10.0000000000, got %v", model.OutputPriceUSDPerMTokens)
	}
	if model.Source != "manual" {
		t.Fatalf("expected source manual, got %q", model.Source)
	}
}

func TestStoreModelCapabilityRoundTrip(t *testing.T) {
	ctx, tx, store, cleanup := newCapabilityStoreTx(t)
	defer cleanup()

	suffix := time.Now().UnixNano()
	id := insertModelRow(t, ctx, tx, suffix)

	written, err := store.UpsertModelCapability(ctx, capability.UpsertModelCapabilityParams{
		ModelID:      id,
		Key:          capability.KeyReasoningEffort,
		SupportLevel: capability.SupportLevelLimited,
		Limits:       json.RawMessage(`{"effort":["high","max"]}`),
	})
	if err != nil {
		t.Fatalf("upsert model capability: %v", err)
	}
	if written.Key != capability.KeyReasoningEffort || written.SupportLevel != capability.SupportLevelLimited {
		t.Fatalf("unexpected written capability: %#v", written)
	}
	if len(written.Limits) == 0 {
		t.Fatal("expected limits to round-trip")
	}

	caps, err := store.ListModelCapabilities(ctx, id)
	if err != nil {
		t.Fatalf("list model capabilities: %v", err)
	}
	if len(caps) != 1 || caps[0].Key != capability.KeyReasoningEffort {
		t.Fatalf("expected single reasoning.effort capability, got %#v", caps)
	}
}

func TestStoreChannelOverrideRejectsFull(t *testing.T) {
	ctx, _, store, cleanup := newCapabilityStoreTx(t)
	defer cleanup()

	_, err := store.UpsertChannelOverride(ctx, capability.UpsertChannelOverrideParams{
		ChannelID:    1,
		Key:          capability.KeyToolsFunction,
		SupportLevel: capability.SupportLevelFull,
	})
	if err == nil {
		t.Fatal("expected store to reject full on channel override before DB")
	}
}

func TestStoreSyncJobLifecycle(t *testing.T) {
	ctx, _, store, cleanup := newCapabilityStoreTx(t)
	defer cleanup()

	job, err := store.CreateSyncJob(ctx, capability.SourceModelsDev)
	if err != nil {
		t.Fatalf("create sync job: %v", err)
	}
	if job.Status != capability.SyncJobStatusPending {
		t.Fatalf("expected pending, got %q", job.Status)
	}

	if _, err := store.MarkSyncJobRunning(ctx, job.ID); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	done, err := store.MarkSyncJobSucceeded(ctx, job.ID, json.RawMessage(`{"upserted":1}`))
	if err != nil {
		t.Fatalf("mark succeeded: %v", err)
	}
	if done.Status != capability.SyncJobStatusSucceeded || len(done.Stats) == 0 {
		t.Fatalf("expected succeeded with stats, got %#v", done)
	}

	latest, err := store.GetLatestSyncJob(ctx, capability.SourceModelsDev)
	if err != nil {
		t.Fatalf("get latest sync job: %v", err)
	}
	if latest.ID != job.ID {
		t.Fatalf("expected latest job %d, got %d", job.ID, latest.ID)
	}
}
