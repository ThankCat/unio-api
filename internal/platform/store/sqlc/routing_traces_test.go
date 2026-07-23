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

func TestRoutingDecisionTraceQueryAndRetention(t *testing.T) {
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
		RequestedModelID: record.RequestedModelID, Protocol: record.IngressProtocol, Operation: record.Operation,
		PoolSize: 2, CandidateCount: 2, Abnormal: false, AbnormalReasons: []string{},
		CandidateScores: []byte(fmt.Sprintf(`[{"channel_id":%d,"weight":0.8}]`, channelID)),
		SelectedOrder:   []int64{channelID}, FallbackChain: []byte(`[]`),
		AlgorithmVersion: "balanced_v1", Sampled: true,
	}
	if err := queries.UpsertRoutingDecisionTrace(ctx, params); err != nil {
		t.Fatalf("insert routing trace: %v", err)
	}
	params.Abnormal = true
	params.AbnormalReasons = []string{"fallback"}
	params.FallbackChain = []byte(fmt.Sprintf(`[%d]`, channelID))
	if err := queries.UpsertRoutingDecisionTrace(ctx, params); err != nil {
		t.Fatalf("update routing trace: %v", err)
	}

	got, err := queries.GetRoutingDecisionTraceByRequestID(ctx, requestID)
	if err != nil {
		t.Fatalf("get routing trace: %v", err)
	}
	if got.RequestRecordID != record.ID || got.RouteID != identity.apiKey.RouteID || !got.Abnormal || got.RequestID != requestID {
		t.Fatalf("unexpected routing trace: %+v", got)
	}
	rows, err := queries.ListRouteRoutingDecisionTraces(ctx, sqlc.ListRouteRoutingDecisionTracesParams{
		RouteID: identity.apiKey.RouteID, PageOffset: 0, PageLimit: 10,
	})
	if err != nil || len(rows) != 1 {
		t.Fatalf("list routing traces: rows=%d err=%v", len(rows), err)
	}

	expiredAt := time.Now().Add(-8 * 24 * time.Hour)
	if _, err := tx.Exec(ctx, `UPDATE routing_decision_traces SET created_at = $1 WHERE request_record_id = $2`, expiredAt, record.ID); err != nil {
		t.Fatalf("age routing trace: %v", err)
	}
	deleted, err := queries.DeleteExpiredRoutingDecisionTraces(ctx, sqlc.DeleteExpiredRoutingDecisionTracesParams{
		Cutoff: pgtype.Timestamptz{Time: time.Now().Add(-7 * 24 * time.Hour), Valid: true}, BatchLimit: 100,
	})
	if err != nil || deleted != 1 {
		t.Fatalf("delete expired routing trace: deleted=%d err=%v", deleted, err)
	}
	if _, err := queries.GetRoutingDecisionTraceByRequestID(ctx, requestID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expected trace deletion, got %v", err)
	}
	if _, err := queries.GetRequestRecordByRequestID(ctx, requestID); err != nil {
		t.Fatalf("retention must preserve request record: %v", err)
	}
	attempts, err := queries.ListRequestAttemptsByRequest(ctx, record.ID)
	if err != nil || len(attempts) != 1 || attempts[0].ID != attempt.ID {
		t.Fatalf("retention must preserve request attempts: attempts=%+v err=%v", attempts, err)
	}
}
