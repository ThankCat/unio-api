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

func TestSyncAppliesAgainstCatalogDatabase(t *testing.T) {
	ctx, tx, store, cleanup := newSyncStoreTx(t)
	defer cleanup()

	// 测试隔离：清空回滚事务内可见的既有目录数据，使 removed 计数只反映本用例 seed 的下架行。
	// 否则本地已同步的 models.dev 目录会让 sync 把全部历史条目计入 removed（counts: removed=221）。
	for _, stmt := range []string{
		"DELETE FROM model_catalog_capabilities",
		"DELETE FROM model_catalog_links",
		"DELETE FROM model_catalog",
	} {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			t.Fatalf("isolate catalog (%s): %v", stmt, err)
		}
	}

	suffix := time.Now().UnixNano()
	goneCanonical := fmt.Sprintf("seedtest/gone-%d", suffix)
	newCanonical := fmt.Sprintf("seedtest/new-%d", suffix)

	// 既有目录条目（feed 已不含 → 标记下架，不删本地行）。
	if _, err := tx.Exec(ctx, `
		INSERT INTO model_catalog (canonical_id, lab, display_name, fingerprint)
		VALUES ($1, 'seedtest', 'Gone', 'old-fp')
	`, goneCanonical); err != nil {
		t.Fatalf("seed gone catalog row: %v", err)
	}

	modelsJSON := fmt.Sprintf(`{
		%q: {"id": %q, "name": "Seed New", "reasoning": true, "tool_call": true, "modalities": {"input":["text"],"output":["text"]}, "limit": {"context": 65536, "output": 4096}}
	}`, newCanonical, newCanonical)
	apiJSON := fmt.Sprintf(`{"seedtest": {"id": "seedtest", "models": {%q: {"cost": {"input": 0.1, "output": 0.2}}}}}`,
		fmt.Sprintf("new-%d", suffix))

	syncer := NewSyncer(fakeFetcher{raw: RawFeed{ModelsJSON: []byte(modelsJSON), APIJSON: []byte(apiJSON)}}, store)

	result, err := syncer.Sync(ctx, Options{})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if result.Upserted != 1 || result.Removed != 1 {
		t.Fatalf("counts: upserted=%d removed=%d (want 1/1)", result.Upserted, result.Removed)
	}
	// seedtest/new 粗能力位：text.input/text.output + reasoning.effort + tools.function = 4。
	if result.CapabilityHints != 4 {
		t.Fatalf("want 4 coarse caps, got %d", result.CapabilityHints)
	}

	// 新目录条目：fingerprint 写入、价格基线写入。
	var fingerprint string
	var inputPrice *string
	if err := tx.QueryRow(ctx, `SELECT fingerprint, input_price_usd_per_million_tokens::text FROM model_catalog WHERE canonical_id = $1`, newCanonical).
		Scan(&fingerprint, &inputPrice); err != nil {
		t.Fatalf("read new catalog row: %v", err)
	}
	if fingerprint == "" {
		t.Fatal("new catalog row missing fingerprint")
	}
	if inputPrice == nil || *inputPrice != "0.1000000000" {
		t.Fatalf("new catalog input price = %v, want 0.1000000000", inputPrice)
	}

	var capCount int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM model_catalog_capabilities WHERE canonical_id = $1`, newCanonical).Scan(&capCount); err != nil {
		t.Fatalf("count caps: %v", err)
	}
	if capCount != 4 {
		t.Fatalf("want 4 catalog capability hints, got %d", capCount)
	}

	// 上游删除：目录条目被标记 removed_upstream_at（不删行）。
	var removedAt *time.Time
	if err := tx.QueryRow(ctx, `SELECT removed_upstream_at FROM model_catalog WHERE canonical_id = $1`, goneCanonical).
		Scan(&removedAt); err != nil {
		t.Fatalf("read gone row: %v", err)
	}
	if removedAt == nil {
		t.Fatalf("gone catalog row not marked removed_upstream_at")
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
