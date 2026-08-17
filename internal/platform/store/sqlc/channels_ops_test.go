package sqlc_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

// TestChannelsOpsQueriesAgainstSchema 校验渠道作战台抽屉 SQL 在真实 schema 上 well-formed。
func TestChannelsOpsQueriesAgainstSchema(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("create postgres pool: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping postgres: %v", err)
	}

	q := sqlc.New(pool)
	now := time.Now().UTC()
	from := pgtype.Timestamptz{Time: now.Add(-24 * time.Hour), Valid: true}
	to := pgtype.Timestamptz{Time: now, Valid: true}

	if _, err := q.ChannelOpsModels(ctx, sqlc.ChannelOpsModelsParams{
		FromTime:  from,
		ToTime:    to,
		ChannelID: 1,
	}); err != nil {
		t.Fatalf("ChannelOpsModels: %v", err)
	}
	if _, err := q.ChannelOpsRoutes(ctx, 1); err != nil {
		t.Fatalf("ChannelOpsRoutes: %v", err)
	}
}

func TestChannelCacheStatsExcludeStickyCrossChannelTransitions(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	identity := createRequestRecordIdentity(t, ctx, queries)
	suffix := time.Now().UnixNano()
	providerID := insertProvider(t, ctx, tx, fmt.Sprintf("channel-cache-provider-%d", suffix), "enabled")
	channelAID := insertChannel(t, ctx, tx, providerID, fmt.Sprintf("channel-cache-a-%d", suffix), "enabled", 10, nil)
	channelBID := insertChannel(t, ctx, tx, providerID, fmt.Sprintf("channel-cache-b-%d", suffix), "enabled", 20, nil)

	type traceFixture struct {
		beforeChannelID *int64
		afterChannelID  *int64
		action          string
		status          string
	}
	type usageFixture struct {
		name          string
		uncached      int64
		cacheRead     int64
		cacheWrite5m  int64
		cacheWrite1h  int64
		cacheWrite30m int64
		trace         *traceFixture
	}

	fixtures := []usageFixture{
		{name: "legacy without trace", uncached: 10, cacheRead: 90},
		{
			name: "first sticky bind", uncached: 50, cacheWrite30m: 50,
			trace: &traceFixture{afterChannelID: &channelBID, action: "bind_if_absent"},
		},
		{
			name: "same channel sticky hit", uncached: 20, cacheRead: 80,
			trace: &traceFixture{beforeChannelID: &channelBID, afterChannelID: &channelBID, action: "refresh_if_current"},
		},
		{
			name: "sticky rebind", uncached: 1000, cacheRead: 100,
			cacheWrite5m: 200, cacheWrite1h: 300, cacheWrite30m: 400,
			trace: &traceFixture{beforeChannelID: &channelAID, afterChannelID: &channelBID, action: "bind_if_absent"},
		},
		{
			name: "sticky temporary bypass", uncached: 2000, cacheWrite5m: 2000,
			trace: &traceFixture{
				beforeChannelID: &channelAID, afterChannelID: &channelAID,
				action: "preserve_on_temporary_bypass", status: "partial",
			},
		},
	}

	for index, fixture := range fixtures {
		record := createRequestRecordForTest(t, ctx, queries, identity, fmt.Sprintf("channel-cache-%d-%d", suffix, index))
		if _, err := queries.MarkRequestRunning(ctx, record.ID); err != nil {
			t.Fatalf("%s: mark request running: %v", fixture.name, err)
		}

		usage := usageRecordParams(record.ID)
		usage.UncachedInputTokens = fixture.uncached
		usage.CacheReadInputTokens = fixture.cacheRead
		usage.CacheWrite5mInputTokens = fixture.cacheWrite5m
		usage.CacheWrite1hInputTokens = fixture.cacheWrite1h
		usage.CacheWrite30mInputTokens = fixture.cacheWrite30m
		if fixture.cacheWrite5m > 0 {
			usage.CacheWrite5mInputTokensState = "known"
		}
		if fixture.cacheWrite1h > 0 {
			usage.CacheWrite1hInputTokensState = "known"
		}
		if fixture.cacheWrite30m > 0 {
			usage.CacheWrite30mInputTokensState = "known"
		}
		if _, err := queries.CreateUsageRecord(ctx, usage); err != nil {
			t.Fatalf("%s: create usage: %v", fixture.name, err)
		}

		completedAt := time.Now().UTC()
		if _, err := queries.MarkRequestSucceeded(ctx, sqlc.MarkRequestSucceededParams{
			ResponseModelID:  pgtype.Text{String: record.RequestedModelID, Valid: true},
			ResponseProtocol: pgtype.Text{String: record.IngressProtocol, Valid: true},
			ResponseID:       pgtype.Text{String: fmt.Sprintf("response-%d-%d", suffix, index), Valid: true},
			FinalProviderID:  pgtype.Int8{Int64: providerID, Valid: true},
			FinalChannelID:   pgtype.Int8{Int64: channelBID, Valid: true},
			CompletedAt:      pgtype.Timestamptz{Time: completedAt, Valid: true},
			RequestRecordID:  record.ID,
		}); err != nil {
			t.Fatalf("%s: mark request succeeded: %v", fixture.name, err)
		}

		if fixture.trace == nil {
			continue
		}
		trace := fixture.trace
		traceStatus := trace.status
		if traceStatus == "" {
			traceStatus = "complete"
		}
		params := sqlc.UpsertRoutingDecisionTraceParams{
			RequestRecordID:     record.ID,
			RouteID:             identity.apiKey.RouteID,
			Mode:                "balanced",
			RequestedModelID:    record.RequestedModelID,
			Protocol:            record.IngressProtocol,
			Endpoint:            record.Endpoint,
			PoolSize:            2,
			AlgorithmVersion:    "objective_v1",
			StickyKeyPresent:    true,
			StickyAction:        pgtype.Text{String: trace.action, Valid: true},
			TraceStatus:         traceStatus,
			SchemaVersion:       1,
			EligibleCount:       2,
			BaselineOrder:       []int64{channelAID, channelBID},
			ActualScanOrder:     []int64{},
			AttemptedChannelIds: []int64{},
			TracePayload:        []byte(`{}`),
		}
		if traceStatus == "complete" {
			params.ActualScanOrder = []int64{channelBID}
			params.AttemptedChannelIds = []int64{channelBID}
			params.SelectedChannelID = pgtype.Int8{Int64: channelBID, Valid: true}
			params.FinalResult = pgtype.Text{String: "success", Valid: true}
		}
		if trace.beforeChannelID != nil {
			params.StickyBeforeChannelID = pgtype.Int8{Int64: *trace.beforeChannelID, Valid: true}
			params.StickyBeforeVersion = pgtype.Int8{Int64: 1, Valid: true}
		}
		if trace.afterChannelID != nil {
			params.StickyAfterChannelID = pgtype.Int8{Int64: *trace.afterChannelID, Valid: true}
			params.StickyAfterVersion = pgtype.Int8{Int64: 2, Valid: true}
		}
		if err := queries.UpsertRoutingDecisionTrace(ctx, params); err != nil {
			t.Fatalf("%s: create routing trace: %v", fixture.name, err)
		}
	}

	channelDetail, err := queries.ChannelOpsDetail(ctx, sqlc.ChannelOpsDetailParams{ChannelID: channelBID})
	if err != nil {
		t.Fatalf("ChannelOpsDetail: %v", err)
	}
	assertChannelCacheAggregate(t, "ChannelOpsDetail", channelDetail.CacheUncachedInput, channelDetail.CacheReadInput,
		channelDetail.CacheWrite5mInput, channelDetail.CacheWrite1hInput, channelDetail.CacheWrite30mInput,
		channelDetail.CacheUsageRecords, channelDetail.CacheEvaluableRecords)

	providerChannels, err := queries.ProviderOpsChannels(ctx, sqlc.ProviderOpsChannelsParams{ProviderID: providerID})
	if err != nil {
		t.Fatalf("ProviderOpsChannels: %v", err)
	}
	var channelRow *sqlc.ProviderOpsChannelsRow
	for index := range providerChannels {
		if providerChannels[index].ID == channelBID {
			channelRow = &providerChannels[index]
			break
		}
	}
	if channelRow == nil {
		t.Fatalf("ProviderOpsChannels missing channel %d: %+v", channelBID, providerChannels)
	}
	assertChannelCacheAggregate(t, "ProviderOpsChannels", channelRow.CacheUncachedInput, channelRow.CacheReadInput,
		channelRow.CacheWrite5mInput, channelRow.CacheWrite1hInput, channelRow.CacheWrite30mInput,
		channelRow.CacheUsageRecords, channelRow.CacheEvaluableRecords)

	providerDetail, err := queries.ProviderOpsDetail(ctx, sqlc.ProviderOpsDetailParams{ProviderID: providerID})
	if err != nil {
		t.Fatalf("ProviderOpsDetail: %v", err)
	}
	if providerDetail.CacheUncachedInput != 3080 || providerDetail.CacheReadInput != 270 ||
		providerDetail.CacheWrite5mInput != 2200 || providerDetail.CacheWrite1hInput != 300 ||
		providerDetail.CacheWrite30mInput != 450 ||
		providerDetail.CacheUsageRecords != 5 || providerDetail.CacheEvaluableRecords != 5 {
		t.Fatalf("ProviderOpsDetail must retain transition usage: %+v", providerDetail)
	}
}

func assertChannelCacheAggregate(
	t *testing.T,
	name string,
	uncached int64,
	cacheRead int64,
	cacheWrite5m int64,
	cacheWrite1h int64,
	cacheWrite30m int64,
	usageRecords int64,
	evaluableRecords int64,
) {
	t.Helper()

	if uncached != 80 || cacheRead != 170 || cacheWrite5m != 0 || cacheWrite1h != 0 || cacheWrite30m != 50 ||
		usageRecords != 3 || evaluableRecords != 3 {
		t.Fatalf("%s includes sticky cross-channel transition usage: uncached=%d read=%d write5m=%d write1h=%d write30m=%d usage=%d evaluable=%d",
			name, uncached, cacheRead, cacheWrite5m, cacheWrite1h, cacheWrite30m, usageRecords, evaluableRecords)
	}
}
