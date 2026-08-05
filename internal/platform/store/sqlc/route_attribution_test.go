package sqlc_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

func TestRouteAggregatesKeepRequestOnSnapshotAfterAPIKeyRebind(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	suffix := time.Now().UnixNano()
	identity := createRequestRecordIdentity(t, ctx, queries)
	originalRouteID := identity.apiKey.RouteID
	record := createRequestRecordForTest(t, ctx, queries, identity, fmt.Sprintf("route-snapshot-request-%d", suffix))

	windowTime := time.Now().UTC().AddDate(20, 0, 0)
	completedAt := windowTime.Add(time.Second)
	if _, err := tx.Exec(ctx, `
		UPDATE request_records
		SET status = 'failed',
		    error_code = 'route_snapshot_test',
		    error_message = 'route snapshot test',
		    created_at = $1,
		    started_at = $1,
		    completed_at = $2,
		    updated_at = $2
		WHERE id = $3
	`, windowTime, completedAt, record.ID); err != nil {
		t.Fatalf("finalize request fixture: %v", err)
	}

	reboundRouteID := insertRouteWithChannels(t, ctx, tx)
	if _, err := queries.SetAPIKeyRoute(ctx, sqlc.SetAPIKeyRouteParams{
		RouteID: reboundRouteID,
		ID:      identity.apiKey.ID,
	}); err != nil {
		t.Fatalf("rebind api key: %v", err)
	}

	from := timestamptz(windowTime.Add(-time.Minute))
	to := timestamptz(windowTime.Add(time.Minute))

	breakdown, err := queries.DashboardBreakdownRoute(ctx, sqlc.DashboardBreakdownRouteParams{FromTime: from, ToTime: to})
	if err != nil {
		t.Fatalf("query route breakdown: %v", err)
	}
	if len(breakdown) != 1 || breakdown[0].RouteID != originalRouteID || breakdown[0].FailedTotal != 1 {
		t.Fatalf("route breakdown = %+v, want one failed request on original route %d", breakdown, originalRouteID)
	}

	originalDetail, err := queries.RouteOpsDetail(ctx, sqlc.RouteOpsDetailParams{RouteID: originalRouteID, FromTime: from, ToTime: to})
	if err != nil {
		t.Fatalf("query original route detail: %v", err)
	}
	reboundDetail, err := queries.RouteOpsDetail(ctx, sqlc.RouteOpsDetailParams{RouteID: reboundRouteID, FromTime: from, ToTime: to})
	if err != nil {
		t.Fatalf("query rebound route detail: %v", err)
	}
	if originalDetail.RequestTotal != 1 || reboundDetail.RequestTotal != 0 {
		t.Fatalf("route detail totals = original %d, rebound %d; want 1, 0", originalDetail.RequestTotal, reboundDetail.RequestTotal)
	}

	series, err := queries.RouteOpsPerformanceTimeseries(ctx, sqlc.RouteOpsPerformanceTimeseriesParams{
		Unit: "minute", RouteID: originalRouteID, FromTime: from, ToTime: to,
	})
	if err != nil {
		t.Fatalf("query route performance: %v", err)
	}
	if len(series) != 1 || series[0].RequestTotal != 1 {
		t.Fatalf("route performance = %+v, want one request on original route", series)
	}

	models, err := queries.RouteOpsModels(ctx, sqlc.RouteOpsModelsParams{RouteID: originalRouteID, FromTime: from, ToTime: to})
	if err != nil {
		t.Fatalf("query route models: %v", err)
	}
	if len(models) != 1 || models[0].ModelID != record.RequestedModelID || models[0].RequestTotal != 1 {
		t.Fatalf("route models = %+v, want request model on original route", models)
	}

	requests, err := queries.RouteOpsRequests(ctx, sqlc.RouteOpsRequestsParams{
		RouteID: originalRouteID, FromTime: from, ToTime: to, PageLimit: 20,
	})
	if err != nil {
		t.Fatalf("query original route requests: %v", err)
	}
	requestCount, err := queries.RouteOpsRequestsCount(ctx, sqlc.RouteOpsRequestsCountParams{
		RouteID: originalRouteID, FromTime: from, ToTime: to,
	})
	if err != nil {
		t.Fatalf("count original route requests: %v", err)
	}
	reboundCount, err := queries.RouteOpsRequestsCount(ctx, sqlc.RouteOpsRequestsCountParams{
		RouteID: reboundRouteID, FromTime: from, ToTime: to,
	})
	if err != nil {
		t.Fatalf("count rebound route requests: %v", err)
	}
	if len(requests) != 1 || requests[0].RequestID != record.RequestID || requestCount != 1 || reboundCount != 0 {
		t.Fatalf("route requests = rows %+v, original count %d, rebound count %d; want original request only", requests, requestCount, reboundCount)
	}
}
