package modelcatalog

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
)

// newSyncStoreTx 在回滚事务上构造真实 SyncStore，避免污染本地库；未配 DATABASE_URL 时跳过。
func newSyncStoreTx(t *testing.T) (context.Context, pgx.Tx, SyncStore, func()) {
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

	return ctx, tx, NewSyncStore(sqlc.New(tx)), cleanup
}

func TestSyncStoreUpsertGuardsManualRows(t *testing.T) {
	ctx, tx, store, cleanup := newSyncStoreTx(t)
	defer cleanup()

	suffix := time.Now().UnixNano()
	manualCanonical := fmt.Sprintf("manualtest/keep-%d", suffix)
	_, err := tx.Exec(ctx, `
		INSERT INTO models (model_id, display_name, owned_by, status, canonical_id, source)
		VALUES ($1, 'Manual Keep', 'manualtest', 'enabled', $2, 'manual')
	`, fmt.Sprintf("manual-keep-%d", suffix), manualCanonical)
	if err != nil {
		t.Fatalf("seed manual row: %v", err)
	}

	_, applied, err := store.UpsertSeedModel(ctx, CanonicalModel{
		CanonicalID: manualCanonical,
		Lab:         "manualtest",
		DisplayName: "OVERWRITE ATTEMPT",
	})
	if err != nil {
		t.Fatalf("upsert seed model: %v", err)
	}
	if applied {
		t.Fatal("manual row must not be overwritten by seed upsert")
	}

	var displayName, source, status string
	err = tx.QueryRow(ctx, `SELECT display_name, source, status FROM models WHERE canonical_id = $1`, manualCanonical).
		Scan(&displayName, &source, &status)
	if err != nil {
		t.Fatalf("read manual row: %v", err)
	}
	if displayName != "Manual Keep" || source != "manual" || status != "enabled" {
		t.Fatalf("manual row mutated: display=%q source=%q status=%q", displayName, source, status)
	}
}

func TestSyncAppliesAgainstDatabase(t *testing.T) {
	ctx, tx, store, cleanup := newSyncStoreTx(t)
	defer cleanup()

	suffix := time.Now().UnixNano()
	manualCanonical := fmt.Sprintf("manualtest/keep-%d", suffix)
	goneCanonical := fmt.Sprintf("seedtest/gone-%d", suffix)
	newCanonical := fmt.Sprintf("seedtest/new-%d", suffix)

	// 既有 manual 行（feed 中存在 → 冲突跳过）。
	if _, err := tx.Exec(ctx, `
		INSERT INTO models (model_id, display_name, owned_by, status, canonical_id, source)
		VALUES ($1, 'Manual Keep', 'manualtest', 'enabled', $2, 'manual')
	`, fmt.Sprintf("manual-keep-%d", suffix), manualCanonical); err != nil {
		t.Fatalf("seed manual row: %v", err)
	}
	// 既有 seed 行（feed 已不含 → 标记删除）。
	if _, err := tx.Exec(ctx, `
		INSERT INTO models (model_id, display_name, owned_by, status, canonical_id, source)
		VALUES ($1, 'Seed Gone', 'seedtest', 'enabled', $2, 'seed_models_dev')
	`, fmt.Sprintf("seed-gone-%d", suffix), goneCanonical); err != nil {
		t.Fatalf("seed gone row: %v", err)
	}

	modelsJSON := fmt.Sprintf(`{
		%q: {"id": %q, "name": "Manual Keep", "modalities": {"input":["text"],"output":["text"]}},
		%q: {"id": %q, "name": "Seed New", "reasoning": true, "tool_call": true, "modalities": {"input":["text"],"output":["text"]}, "limit": {"context": 65536, "output": 4096}}
	}`, manualCanonical, manualCanonical, newCanonical, newCanonical)
	apiJSON := fmt.Sprintf(`{"seedtest": {"id": "seedtest", "models": {%q: {"cost": {"input": 0.1, "output": 0.2}}}}}`,
		fmt.Sprintf("new-%d", suffix))

	syncer := NewSyncer(fakeFetcher{raw: RawFeed{ModelsJSON: []byte(modelsJSON), APIJSON: []byte(apiJSON)}}, store)

	result, err := syncer.Sync(ctx, Options{})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if result.Inserted != 1 || result.Removed != 1 {
		t.Fatalf("counts: inserted=%d removed=%d (want 1/1)", result.Inserted, result.Removed)
	}
	if len(result.ManualConflicts) != 1 || result.ManualConflicts[0] != manualCanonical {
		t.Fatalf("want manual conflict %q, got %+v", manualCanonical, result.ManualConflicts)
	}
	// seedtest/new 粗能力位：text.input/text.output + reasoning.effort + tools.function = 4。
	if result.CapabilitiesSeeded != 4 {
		t.Fatalf("want 4 coarse caps, got %d", result.CapabilitiesSeeded)
	}

	// 新 seed 行：disabled + seed_models_dev + 价格基线写入。
	var newID int64
	var status, source string
	var inputPrice *string
	if err := tx.QueryRow(ctx, `SELECT id, status, source, input_price_usd_per_million_tokens::text FROM models WHERE canonical_id = $1`, newCanonical).
		Scan(&newID, &status, &source, &inputPrice); err != nil {
		t.Fatalf("read new seed row: %v", err)
	}
	if status != "disabled" || source != "seed_models_dev" {
		t.Fatalf("new seed row: status=%q source=%q", status, source)
	}
	if inputPrice == nil || *inputPrice != "0.1000000000" {
		t.Fatalf("new seed input price = %v, want 0.1000000000", inputPrice)
	}

	var capCount int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM model_capabilities WHERE model_id = $1 AND source = 'models_dev'`, newID).Scan(&capCount); err != nil {
		t.Fatalf("count caps: %v", err)
	}
	if capCount != 4 {
		t.Fatalf("want 4 models_dev caps in db, got %d", capCount)
	}

	// 上游删除：seed 行被标记 disabled + removed_upstream_at。
	var goneStatus string
	var removedAt *time.Time
	if err := tx.QueryRow(ctx, `SELECT status, removed_upstream_at FROM models WHERE canonical_id = $1`, goneCanonical).
		Scan(&goneStatus, &removedAt); err != nil {
		t.Fatalf("read gone row: %v", err)
	}
	if goneStatus != "disabled" || removedAt == nil {
		t.Fatalf("gone row not marked removed: status=%q removedAt=%v", goneStatus, removedAt)
	}

	// manual 行保持原样。
	var manualSource, manualStatus string
	if err := tx.QueryRow(ctx, `SELECT source, status FROM models WHERE canonical_id = $1`, manualCanonical).
		Scan(&manualSource, &manualStatus); err != nil {
		t.Fatalf("read manual row: %v", err)
	}
	if manualSource != "manual" || manualStatus != "enabled" {
		t.Fatalf("manual row mutated: source=%q status=%q", manualSource, manualStatus)
	}

	// sync_job 成功并落 license 审计。
	var jobStatus string
	var statsRaw []byte
	if err := tx.QueryRow(ctx, `SELECT status, stats_json FROM model_capability_sync_jobs WHERE source = 'models_dev' ORDER BY id DESC LIMIT 1`).
		Scan(&jobStatus, &statsRaw); err != nil {
		t.Fatalf("read sync job: %v", err)
	}
	if jobStatus != "succeeded" {
		t.Fatalf("sync job status = %q, want succeeded", jobStatus)
	}
	var stats struct {
		License           string `json:"license"`
		SourceFingerprint string `json:"source_fingerprint"`
	}
	if err := json.Unmarshal(statsRaw, &stats); err != nil {
		t.Fatalf("stats json: %v", err)
	}
	if stats.License != "MIT" || stats.SourceFingerprint == "" {
		t.Fatalf("sync job missing license audit: %+v", stats)
	}
}
