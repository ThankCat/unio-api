package sqlc_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

func TestDashboardBreakdownsDoNotMultiplyRequestFactsByLedgerRows(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	suffix := time.Now().UnixNano()
	identity := createRequestRecordIdentity(t, ctx, queries)
	providerID := insertProvider(t, ctx, tx, fmt.Sprintf("breakdown-provider-%d", suffix), "enabled")
	channelID := insertChannel(t, ctx, tx, providerID, fmt.Sprintf("breakdown-channel-%d", suffix), "enabled", 1, nil)
	modelName := fmt.Sprintf("breakdown-model-%d", suffix)
	modelID := insertModel(t, ctx, tx, modelName, "test", "enabled")
	insertChannelModel(t, ctx, tx, channelID, modelID, modelName, "enabled")

	record := createRequestRecordForTest(t, ctx, queries, identity, fmt.Sprintf("breakdown-request-%d", suffix))
	if _, err := tx.Exec(ctx, `UPDATE request_records SET requested_model_id = $1 WHERE id = $2`, modelName, record.ID); err != nil {
		t.Fatalf("set request model: %v", err)
	}
	if _, err := queries.MarkRequestRunning(ctx, record.ID); err != nil {
		t.Fatalf("mark request running: %v", err)
	}

	windowTime := time.Now().UTC().AddDate(10, 0, 0)
	completedAt := windowTime.Add(time.Second)
	attempt, err := queries.CreateRequestAttempt(ctx, withRequestAttemptRuntimeIdentity(t, ctx, tx, channelID, sqlc.CreateRequestAttemptParams{
		RequestRecordID:       record.ID,
		AttemptIndex:          0,
		ProviderID:            providerID,
		ChannelID:             channelID,
		AdapterKey:            "openai",
		UpstreamModel:         modelName,
		UpstreamProtocol:      "openai",
		Status:                "running",
		StartedAt:             timestamptz(windowTime),
		RoutingCandidateIndex: 0,
	}))
	if err != nil {
		t.Fatalf("create request attempt: %v", err)
	}
	if _, err := queries.MarkRequestAttemptSucceeded(ctx, sqlc.MarkRequestAttemptSucceededParams{
		UpstreamResponseID:    pgtype.Text{String: "breakdown-response", Valid: true},
		UpstreamResponseModel: pgtype.Text{String: modelName, Valid: true},
		UpstreamFinishReason:  pgtype.Text{String: "stop", Valid: true},
		FinishClass:           pgtype.Text{String: "stop", Valid: true},
		UpstreamStatusCode:    pgtype.Int4{Int32: 200, Valid: true},
		FinalUsageReceived:    true,
		UsageMappingVersion:   pgtype.Text{String: "openai_chat_usage_v1", Valid: true},
		CompletedAt:           timestamptz(completedAt),
		AttemptID:             attempt.ID,
	}); err != nil {
		t.Fatalf("mark request attempt succeeded: %v", err)
	}
	if _, err := queries.MarkRequestSucceeded(ctx, sqlc.MarkRequestSucceededParams{
		ResponseModelID:  pgtype.Text{String: modelName, Valid: true},
		ResponseProtocol: pgtype.Text{String: "openai", Valid: true},
		ResponseID:       pgtype.Text{String: "breakdown-response", Valid: true},
		FinalProviderID:  pgtype.Int8{Int64: providerID, Valid: true},
		FinalChannelID:   pgtype.Int8{Int64: channelID, Valid: true},
		CompletedAt:      timestamptz(completedAt),
		RequestRecordID:  record.ID,
	}); err != nil {
		t.Fatalf("mark request succeeded: %v", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE request_records SET created_at = $1, started_at = $1, completed_at = $2 WHERE id = $3`, windowTime, completedAt, record.ID); err != nil {
		t.Fatalf("move request into isolated time window: %v", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE request_attempts SET created_at = $1, started_at = $1, completed_at = $2 WHERE id = $3`, windowTime, completedAt, attempt.ID); err != nil {
		t.Fatalf("move attempt into isolated time window: %v", err)
	}

	if _, err := queries.CreateUsageRecord(ctx, usageRecordParams(record.ID)); err != nil {
		t.Fatalf("create usage record: %v", err)
	}
	if _, err := queries.CreateCostSnapshot(ctx, sqlc.CreateCostSnapshotParams{
		RequestRecordID:              record.ID,
		CostMultiplier:               numeric(1),
		RechargeFactor:               numeric(1),
		ProviderID:                   providerID,
		ChannelID:                    channelID,
		ModelID:                      modelID,
		UpstreamModel:                modelName,
		Currency:                     "USD",
		PricingUnit:                  "per_1m_tokens",
		UncachedInputCost:            numeric(1),
		OutputCost:                   numeric(1),
		UncachedInputCostAmount:      numeric(2),
		CacheReadInputCostAmount:     numeric(0),
		CacheWrite5mInputCostAmount:  numeric(0),
		CacheWrite1hInputCostAmount:  numeric(0),
		CacheWrite30mInputCostAmount: numeric(0),
		OutputCostAmount:             numeric(5),
		ReasoningOutputCostAmount:    numeric(0),
		TotalCostAmount:              numeric(7),
		FormulaVersion:               "breakdown_test_v1",
	}); err != nil {
		t.Fatalf("create cost snapshot: %v", err)
	}

	requestRecordID := pgtype.Int8{Int64: record.ID, Valid: true}
	createLedgerEntryForTest(t, queries, ctx, sqlc.CreateLedgerEntryParams{
		UserID:          identity.user.ID,
		RequestRecordID: requestRecordID,
		EntryType:       "debit",
		Amount:          numeric(11),
		Currency:        "USD",
		BalanceBefore:   numeric(100),
		BalanceAfter:    numeric(89),
		IdempotencyKey:  fmt.Sprintf("breakdown-debit-%d", suffix),
		Reason:          "breakdown test debit",
	})
	createLedgerEntryForTest(t, queries, ctx, sqlc.CreateLedgerEntryParams{
		UserID:          identity.user.ID,
		RequestRecordID: requestRecordID,
		EntryType:       "refund",
		Amount:          numeric(3),
		Currency:        "USD",
		BalanceBefore:   numeric(89),
		BalanceAfter:    numeric(92),
		IdempotencyKey:  fmt.Sprintf("breakdown-refund-%d", suffix),
		Reason:          "breakdown test refund",
	})

	from := timestamptz(windowTime.Add(-time.Minute))
	to := timestamptz(windowTime.Add(time.Minute))
	routes, err := queries.DashboardBreakdownRoute(ctx, sqlc.DashboardBreakdownRouteParams{FromTime: from, ToTime: to})
	if err != nil {
		t.Fatalf("query route breakdown: %v", err)
	}
	if len(routes) != 1 || routes[0].RouteID != identity.apiKey.RouteID {
		t.Fatalf("route breakdown rows = %+v", routes)
	}
	assertBreakdownFacts(t, "route", routes[0].TerminalTotal, routes[0].SucceededTotal, routes[0].TokensTotal, routes[0].RevenueUsd, routes[0].CostUsd)

	providers, err := queries.DashboardBreakdownProvider(ctx, sqlc.DashboardBreakdownProviderParams{FromTime: from, ToTime: to})
	if err != nil {
		t.Fatalf("query provider breakdown: %v", err)
	}
	if len(providers) != 1 || providers[0].ProviderID != providerID {
		t.Fatalf("provider breakdown rows = %+v", providers)
	}
	assertBreakdownFacts(t, "provider", providers[0].TerminalTotal, providers[0].SucceededTotal, providers[0].TokensTotal, providers[0].RevenueUsd, providers[0].CostUsd)

	channels, err := queries.DashboardBreakdownChannel(ctx, sqlc.DashboardBreakdownChannelParams{FromTime: from, ToTime: to})
	if err != nil {
		t.Fatalf("query channel breakdown: %v", err)
	}
	if len(channels) != 1 || channels[0].ChannelID != channelID {
		t.Fatalf("channel breakdown rows = %+v", channels)
	}
	assertBreakdownFacts(t, "channel", channels[0].TerminalTotal, channels[0].SucceededTotal, channels[0].TokensTotal, channels[0].RevenueUsd, channels[0].CostUsd)

	models, err := queries.DashboardBreakdownModel(ctx, sqlc.DashboardBreakdownModelParams{FromTime: from, ToTime: to})
	if err != nil {
		t.Fatalf("query model breakdown: %v", err)
	}
	if len(models) != 1 || models[0].ModelID != modelName {
		t.Fatalf("model breakdown rows = %+v", models)
	}
	assertBreakdownFacts(t, "model", models[0].TerminalTotal, models[0].SucceededTotal, models[0].TokensTotal, models[0].RevenueUsd, models[0].CostUsd)
}

func assertBreakdownFacts(t *testing.T, name string, terminal, succeeded, tokens int64, revenue, cost pgtype.Numeric) {
	t.Helper()

	if terminal != 1 || succeeded != 1 || tokens != 140 {
		t.Fatalf("%s facts = terminal %d, succeeded %d, tokens %d; want 1, 1, 140", name, terminal, succeeded, tokens)
	}
	assertNumericEquals(t, revenue, 11)
	assertNumericEquals(t, cost, 7)
}
