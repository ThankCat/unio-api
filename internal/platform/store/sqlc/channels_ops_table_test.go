package sqlc_test

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

// opsAttempt 描述一次用于运维聚合的 attempt：成功时按 latencyMs 落 completed_at；失败时按 errorCode / status。
type opsAttempt struct {
	succeeded  bool
	latencyMs  int64
	errorCode  string
	statusCode int32
}

func seedOpsAttempts(t *testing.T, ctx context.Context, tx pgx.Tx, queries *sqlc.Queries, identity requestRecordIdentity, providerID, channelID int64, tag string, attempts []opsAttempt) {
	t.Helper()
	for index, attempt := range attempts {
		record := createRequestRecordForTest(t, ctx, queries, identity, fmt.Sprintf("ops-table-%s-%d", tag, index))
		if _, err := queries.MarkRequestRunning(ctx, record.ID); err != nil {
			t.Fatalf("mark request running: %v", err)
		}
		startedAt := time.Now().UTC().Add(-time.Minute)
		created, err := queries.CreateRequestAttempt(ctx, withRequestAttemptRuntimeIdentity(t, ctx, tx, channelID, sqlc.CreateRequestAttemptParams{
			RequestRecordID:  record.ID,
			AttemptIndex:     0,
			ProviderID:       providerID,
			ChannelID:        channelID,
			AdapterKey:       "openai",
			UpstreamModel:    "gpt-4.1",
			UpstreamProtocol: "openai",
			Status:           "running",
			StartedAt:        timestamptz(startedAt),
			CompletedAt:      nullTimestamptz(),
		}))
		if err != nil {
			t.Fatalf("create attempt: %v", err)
		}
		completedAt := pgtype.Timestamptz{Time: startedAt.Add(time.Duration(attempt.latencyMs) * time.Millisecond), Valid: true}
		if attempt.succeeded {
			if _, err := queries.MarkRequestAttemptSucceeded(ctx, sqlc.MarkRequestAttemptSucceededParams{
				UpstreamStatusCode: pgtype.Int4{Int32: 200, Valid: true},
				CompletedAt:        completedAt,
				AttemptID:          created.ID,
			}); err != nil {
				t.Fatalf("mark attempt succeeded: %v", err)
			}
			continue
		}
		if _, err := queries.MarkRequestAttemptFailed(ctx, sqlc.MarkRequestAttemptFailedParams{
			UpstreamStatusCode: pgtype.Int4{Int32: attempt.statusCode, Valid: attempt.statusCode != 0},
			ErrorCode:          pgtype.Text{String: attempt.errorCode, Valid: attempt.errorCode != ""},
			ErrorMessage:       pgtype.Text{String: "ops table fixture", Valid: true},
			CompletedAt:        completedAt,
			AttemptID:          created.ID,
		}); err != nil {
			t.Fatalf("mark attempt failed: %v", err)
		}
	}
}

// ChannelsOpsTable 改为「先缩小渠道页、再补百分位」的三段式后，指标口径、默认成功率排序与分页必须与原单段查询一致。
func TestChannelsOpsTablePagesFirstThenEnrichesMetrics(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	identity := createRequestRecordIdentity(t, ctx, queries)
	suffix := time.Now().UnixNano()
	providerID := insertProvider(t, ctx, tx, fmt.Sprintf("ops-table-provider-%d", suffix), "enabled")
	search := fmt.Sprintf("ops-table-%d", suffix)
	healthy := insertChannel(t, ctx, tx, providerID, search+"-healthy", "enabled", 10, nil)
	flaky := insertChannel(t, ctx, tx, providerID, search+"-flaky", "enabled", 20, nil)
	idle := insertChannel(t, ctx, tx, providerID, search+"-idle", "enabled", 30, nil)
	modelID := insertModel(t, ctx, tx, fmt.Sprintf("ops-table-model-%d", suffix), "openai", "enabled")
	insertChannelModel(t, ctx, tx, idle, modelID, "gpt-4.1", "enabled")

	// healthy：4 成功（100/200/300/400ms）；flaky：1 成功（50ms）+ 2 上游失败（其中 1 次超时）+ 1 客户端 4xx（不计入合格 attempt）。
	seedOpsAttempts(t, ctx, tx, queries, identity, providerID, healthy, "healthy", []opsAttempt{
		{succeeded: true, latencyMs: 100}, {succeeded: true, latencyMs: 200},
		{succeeded: true, latencyMs: 300}, {succeeded: true, latencyMs: 400},
	})
	seedOpsAttempts(t, ctx, tx, queries, identity, providerID, flaky, "flaky", []opsAttempt{
		{succeeded: true, latencyMs: 50},
		{errorCode: "upstream_timeout", statusCode: 504, latencyMs: 30_000},
		{errorCode: "upstream_bad_gateway", statusCode: 502, latencyMs: 10},
		{errorCode: "invalid_request", statusCode: 400, latencyMs: 5},
	})

	rows, err := queries.ChannelsOpsTable(ctx, sqlc.ChannelsOpsTableParams{
		Search:     pgtype.Text{String: search, Valid: true},
		PageLimit:  10,
		PageOffset: 0,
	})
	if err != nil {
		t.Fatalf("ChannelsOpsTable: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 channels, got %d", len(rows))
	}
	// 默认按成功率升序（最需处理优先）：flaky 1/3 < healthy 4/4；无样本的 idle 成功率 NULL 排最后。
	if rows[0].ID != flaky || rows[1].ID != healthy || rows[2].ID != idle {
		t.Fatalf("default order = %d,%d,%d, want flaky=%d healthy=%d idle=%d", rows[0].ID, rows[1].ID, rows[2].ID, flaky, healthy, idle)
	}

	byID := map[int64]sqlc.ChannelsOpsTableRow{}
	for _, row := range rows {
		byID[row.ID] = row
	}
	got := byID[flaky]
	if got.AttemptTotal != 3 || got.AttemptSucceeded != 1 || got.TimeoutTotal != 1 || got.LatencySample != 1 {
		t.Fatalf("flaky metrics = total %d succeeded %d timeout %d sample %d", got.AttemptTotal, got.AttemptSucceeded, got.TimeoutTotal, got.LatencySample)
	}
	if math.Abs(got.LatencyAvg-50) > 0.5 || math.Abs(got.LatencyP50-50) > 0.5 || math.Abs(got.LatencyP99-50) > 0.5 {
		t.Fatalf("flaky latency = avg %.1f p50 %.1f p99 %.1f, want 50", got.LatencyAvg, got.LatencyP50, got.LatencyP99)
	}
	if !got.RecentErrorCode.Valid || got.RecentErrorCode.String == "" {
		t.Fatalf("flaky recent error code missing: %+v", got.RecentErrorCode)
	}

	got = byID[healthy]
	if got.AttemptTotal != 4 || got.AttemptSucceeded != 4 || got.TimeoutTotal != 0 || got.LatencySample != 4 {
		t.Fatalf("healthy metrics = total %d succeeded %d timeout %d sample %d", got.AttemptTotal, got.AttemptSucceeded, got.TimeoutTotal, got.LatencySample)
	}
	// 100/200/300/400 的线性插值百分位：p50=250，p90=370，p95=385，p99=397。
	if math.Abs(got.LatencyAvg-250) > 0.5 || math.Abs(got.LatencyP50-250) > 0.5 ||
		math.Abs(got.LatencyP90-370) > 0.5 || math.Abs(got.LatencyP95-385) > 0.5 || math.Abs(got.LatencyP99-397) > 0.5 {
		t.Fatalf("healthy latency = avg %.1f p50 %.1f p90 %.1f p95 %.1f p99 %.1f", got.LatencyAvg, got.LatencyP50, got.LatencyP90, got.LatencyP95, got.LatencyP99)
	}
	if got.RecentErrorCode.Valid {
		t.Fatalf("healthy channel must not carry a recent error code: %+v", got.RecentErrorCode)
	}

	got = byID[idle]
	if got.AttemptTotal != 0 || got.LatencyAvg != 0 || got.LatencyP50 != 0 || got.BoundModels != 1 {
		t.Fatalf("idle metrics = %+v", got)
	}

	// 分页发生在补百分位之前：第二页只应剩下 idle，且排序与整页一致。
	page2, err := queries.ChannelsOpsTable(ctx, sqlc.ChannelsOpsTableParams{
		Search:     pgtype.Text{String: search, Valid: true},
		PageLimit:  2,
		PageOffset: 2,
	})
	if err != nil {
		t.Fatalf("ChannelsOpsTable page 2: %v", err)
	}
	if len(page2) != 1 || page2[0].ID != idle {
		t.Fatalf("page 2 = %+v, want only idle channel %d", page2, idle)
	}

	// 按名称降序：idle > healthy > flaky（字母序）。
	byName, err := queries.ChannelsOpsTable(ctx, sqlc.ChannelsOpsTableParams{
		Search:     pgtype.Text{String: search, Valid: true},
		SortField:  pgtype.Text{String: "name", Valid: true},
		SortDesc:   pgtype.Bool{Bool: true, Valid: true},
		PageLimit:  10,
		PageOffset: 0,
	})
	if err != nil {
		t.Fatalf("ChannelsOpsTable by name: %v", err)
	}
	if len(byName) != 3 || byName[0].ID != idle || byName[1].ID != healthy || byName[2].ID != flaky {
		t.Fatalf("name desc order = %v", []int64{byName[0].ID, byName[1].ID, byName[2].ID})
	}

	// 时间窗把全部 attempt 排除后，指标归零但渠道仍在列表里。
	future := time.Now().UTC().Add(time.Hour)
	windowed, err := queries.ChannelsOpsTable(ctx, sqlc.ChannelsOpsTableParams{
		Search:     pgtype.Text{String: search, Valid: true},
		FromTime:   pgtype.Timestamptz{Time: future, Valid: true},
		PageLimit:  10,
		PageOffset: 0,
	})
	if err != nil {
		t.Fatalf("ChannelsOpsTable windowed: %v", err)
	}
	if len(windowed) != 3 {
		t.Fatalf("windowed rows = %d, want 3", len(windowed))
	}
	for _, row := range windowed {
		if row.AttemptTotal != 0 || row.LatencyP50 != 0 || row.RecentErrorCode.Valid {
			t.Fatalf("windowed metrics must be empty: %+v", row)
		}
	}

	total, err := queries.ChannelsOpsTableCount(ctx, sqlc.ChannelsOpsTableCountParams{Search: pgtype.Text{String: search, Valid: true}})
	if err != nil {
		t.Fatalf("ChannelsOpsTableCount: %v", err)
	}
	if total != 3 {
		t.Fatalf("count = %d, want 3", total)
	}
}
