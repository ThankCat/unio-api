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

func TestRoutingDecisionTraceQueryCompletionAndRequestCascade(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	identity := createRequestRecordIdentity(t, ctx, queries)
	suffix := time.Now().UnixNano()
	providerID := insertProvider(t, ctx, tx, fmt.Sprintf("routing-trace-provider-%d", suffix), "enabled")
	channelID := insertChannel(t, ctx, tx, providerID, fmt.Sprintf("routing-trace-channel-%d", suffix), "enabled", 10, nil)
	if err := queries.AddRouteChannel(ctx, sqlc.AddRouteChannelParams{RouteID: identity.apiKey.RouteID, ChannelID: channelID}); err != nil {
		t.Fatalf("bind route channel: %v", err)
	}
	requestID := fmt.Sprintf("routing-trace-request-%d", suffix)
	record := createRequestRecordForTest(t, ctx, queries, identity, requestID)
	attempt, err := queries.CreateRequestAttempt(ctx, withRequestAttemptRuntimeIdentity(t, ctx, tx, channelID, sqlc.CreateRequestAttemptParams{
		RequestRecordID: record.ID, AttemptIndex: 0, ProviderID: providerID, ChannelID: channelID,
		AdapterKey: "openai", UpstreamModel: "deepseek-v4-pro", UpstreamProtocol: "openai",
		Status: "running", StartedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}))
	if err != nil {
		t.Fatalf("create request attempt: %v", err)
	}
	runtimePool, err := queries.RouteRuntimePool(ctx, sqlc.RouteRuntimePoolParams{
		RouteID: identity.apiKey.RouteID, ModelID: "", AtTime: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	})
	if err != nil || len(runtimePool) != 1 || runtimePool[0].ChannelID != channelID {
		t.Fatalf("runtime pool must follow explicit route channels: rows=%+v err=%v", runtimePool, err)
	}
	runtimeStats, err := queries.RouteRuntimeChannelStats(ctx, sqlc.RouteRuntimeChannelStatsParams{
		RouteID: identity.apiKey.RouteID, ObservedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	})
	if err != nil || len(runtimeStats) != 1 || runtimeStats[0].ChannelID != channelID {
		t.Fatalf("runtime stats must include explicit route channel: rows=%+v err=%v", runtimeStats, err)
	}

	params := sqlc.UpsertRoutingDecisionTraceParams{
		RequestRecordID: record.ID, RouteID: identity.apiKey.RouteID, Mode: "balanced",
		RequestedModelID: record.RequestedModelID, Protocol: record.IngressProtocol, Endpoint: record.Endpoint,
		PoolSize: 2, AlgorithmVersion: "objective_v1", TraceStatus: "partial", SchemaVersion: 1,
		EligibleCount: 1, BaselineOrder: []int64{channelID}, ActualScanOrder: []int64{},
		AttemptedChannelIds: []int64{}, FallbackCount: 0,
		TracePayload: []byte(fmt.Sprintf(`{"schema_version":1,"candidates":[{"channel_id":%d}]}`, channelID)),
	}
	if err := queries.UpsertRoutingDecisionTrace(ctx, params); err != nil {
		t.Fatalf("insert routing trace: %v", err)
	}
	params.TraceStatus = "complete"
	params.ActualScanOrder = []int64{channelID}
	params.AttemptedChannelIds = []int64{channelID}
	params.SelectedChannelID = pgtype.Int8{Int64: channelID, Valid: true}
	params.FinalResult = pgtype.Text{String: "success", Valid: true}
	params.TracePayload = []byte(fmt.Sprintf(`{"schema_version":1,"candidates":[{"channel_id":%d}],"final_result":"success"}`, channelID))
	if err := queries.UpsertRoutingDecisionTrace(ctx, params); err != nil {
		t.Fatalf("update routing trace: %v", err)
	}

	got, err := queries.GetRoutingDecisionTraceByRequestID(ctx, requestID)
	if err != nil {
		t.Fatalf("get routing trace: %v", err)
	}
	if got.RequestRecordID != record.ID || got.RouteID != identity.apiKey.RouteID || got.RequestID != requestID ||
		got.TraceStatus != "complete" || !got.SelectedChannelID.Valid || got.SelectedChannelID.Int64 != channelID ||
		!got.FinalResult.Valid || got.FinalResult.String != "success" {
		t.Fatalf("unexpected routing trace: %+v", got)
	}

	// Attempts are independent audit/billing facts and intentionally block request deletion.
	// Remove the fixture attempt first; the trace itself must cascade with the request.
	if _, err := tx.Exec(ctx, `DELETE FROM request_attempts WHERE id = $1`, attempt.ID); err != nil {
		t.Fatalf("delete request attempt fixture: %v", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM request_records WHERE id = $1`, record.ID); err != nil {
		t.Fatalf("delete request record: %v", err)
	}
	if _, err := queries.GetRoutingDecisionTraceByRequestID(ctx, requestID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expected trace cascade deletion, got %v", err)
	}
	if _, err := queries.GetRequestRecordByRequestID(ctx, requestID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expected request deletion, got %v", err)
	}
}
