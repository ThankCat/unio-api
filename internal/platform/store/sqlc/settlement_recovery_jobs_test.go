package sqlc_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-api/internal/core/billing"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
)

type settlementRecoveryFixture struct {
	identity     requestRecordIdentity
	request      sqlc.RequestRecord
	attempt      sqlc.RequestAttempt
	reservation  sqlc.LedgerReservation
	channelPrice sqlc.ChannelPrice
	modelID      int64
	providerID   int64
	channelID    int64
}

func createSettlementRecoveryFixture(t *testing.T, ctx context.Context, tx pgx.Tx, queries *sqlc.Queries) settlementRecoveryFixture {
	t.Helper()

	suffix := time.Now().UnixNano()
	identity := createRequestRecordIdentity(t, ctx, queries)
	providerID := insertProvider(t, ctx, tx, fmt.Sprintf("settlement-recovery-provider-%d", suffix), "enabled")
	channelID := insertChannel(t, ctx, tx, providerID, fmt.Sprintf("settlement-recovery-channel-%d", suffix), "enabled", 10, nil)
	modelID := insertModel(t, ctx, tx, fmt.Sprintf("settlement-recovery-model-%d", suffix), "openai", "enabled")
	insertChannelModel(t, ctx, tx, channelID, modelID, "gpt-4.1", "enabled")
	channelPrice := createChannelPriceForTest(t, ctx, queries, channelID, modelID, time.Now().UTC())

	request := createRequestRecordForTest(t, ctx, queries, identity, fmt.Sprintf("settlement-recovery-request-%d", suffix))
	attempt, err := queries.CreateRequestAttempt(ctx, sqlc.CreateRequestAttemptParams{
		RequestRecordID:       request.ID,
		AttemptIndex:          0,
		ProviderID:            providerID,
		ChannelID:             channelID,
		AdapterKey:            "openai",
		UpstreamModel:         "gpt-4.1",
		UpstreamProtocol:      "openai",
		UpstreamResponseID:    pgtype.Text{Valid: false},
		UpstreamResponseModel: pgtype.Text{Valid: false},
		UpstreamFinishReason:  pgtype.Text{Valid: false},
		FinishClass:           pgtype.Text{Valid: false},
		Status:                "running",
		UpstreamStatusCode:    pgtype.Int4{Valid: false},
		UpstreamRequestID:     pgtype.Text{Valid: false},
		ErrorCode:             pgtype.Text{Valid: false},
		ErrorMessage:          pgtype.Text{Valid: false},
		InternalErrorDetail:   pgtype.Text{Valid: false},
		ResponseStartedAt:     pgtype.Timestamptz{Valid: false},
		FinalUsageReceived:    false,
		UsageMappingVersion:   pgtype.Text{Valid: false},
		StartedAt:             timestamptz(time.Now().UTC()),
		CompletedAt:           nullTimestamptz(),
	})
	if err != nil {
		t.Fatalf("create request attempt: %v", err)
	}

	reservation, err := queries.CreateLedgerReservation(ctx, sqlc.CreateLedgerReservationParams{
		UserID:           identity.user.ID,
		RequestRecordID:  request.ID,
		Currency:         "USD",
		EstimatedAmount:  numeric(10),
		AuthorizedAmount: numeric(8),
		IdempotencyKey:   fmt.Sprintf("settlement-recovery-reservation-%d", suffix),
		Reason:           "settlement recovery test",
	})
	if err != nil {
		t.Fatalf("create ledger reservation: %v", err)
	}

	return settlementRecoveryFixture{
		identity:     identity,
		request:      request,
		attempt:      attempt,
		reservation:  reservation,
		channelPrice: channelPrice,
		modelID:      modelID,
		providerID:   providerID,
		channelID:    channelID,
	}
}

func settlementRecoveryJobParams(f settlementRecoveryFixture, nextRunAt time.Time) sqlc.CreateSettlementRecoveryJobParams {
	return sqlc.CreateSettlementRecoveryJobParams{
		UserID:                            f.identity.user.ID,
		RequestRecordID:                   f.request.ID,
		AttemptID:                         f.attempt.ID,
		ReservationID:                     f.reservation.ID,
		ResponseProtocol:                  "openai",
		ResponseID:                        "chatcmpl-recovery-1",
		ResponseModelID:                   "openai/gpt-4.1",
		ModelID:                           f.modelID,
		ProviderID:                        f.providerID,
		ChannelID:                         f.channelID,
		UpstreamProtocol:                  "openai",
		UpstreamResponseID:                "chatcmpl-recovery-1",
		UpstreamModel:                     "gpt-4.1",
		FinishClass:                       "stop",
		UpstreamFinishReason:              "stop",
		UpstreamStatusCode:                200,
		UpstreamRequestID:                 pgtype.Text{String: "req-recovery-1", Valid: true},
		UsageUncachedInputTokens:          8,
		UsageUncachedInputTokensState:     "known",
		UsageCacheReadInputTokens:         2,
		UsageCacheReadInputTokensState:    "known",
		UsageCacheWrite5mInputTokens:      0,
		UsageCacheWrite5mInputTokensState: "not_applicable",
		UsageCacheWrite1hInputTokens:      0,
		UsageCacheWrite1hInputTokensState: "not_applicable",
		UsageOutputTokensTotal:            5,
		UsageOutputTokensTotalState:       "known",
		UsageReasoningOutputTokens:        1,
		UsageReasoningOutputTokensState:   "known",
		UsageSource:                       "upstream_response",
		UsageMappingVersion:               "openai_chat_usage_v1",
		PriceID:                           f.channelPrice.ID,
		Currency:                          f.channelPrice.Currency,
		PricingUnit:                       f.channelPrice.PricingUnit,
		UncachedInputPrice:                f.channelPrice.UncachedInputPrice,
		CacheReadInputPrice:               f.channelPrice.CacheReadInputPrice,
		CacheWrite5mInputPrice:            f.channelPrice.CacheWrite5mInputPrice,
		CacheWrite1hInputPrice:            f.channelPrice.CacheWrite1hInputPrice,
		OutputPrice:                       f.channelPrice.OutputPrice,
		ReasoningOutputPrice:              f.channelPrice.ReasoningOutputPrice,
		FormulaVersion:                    billing.FormulaVersionV1,
		EstimatedAmount:                   f.reservation.EstimatedAmount,
		AuthorizedAmount:                  f.reservation.AuthorizedAmount,
		NextRunAt:                         timestamptz(nextRunAt),
	}
}

func TestSettlementRecoveryJobCreateClaimRetryAndSucceed(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	fixture := createSettlementRecoveryFixture(t, ctx, tx, queries)
	params := settlementRecoveryJobParams(fixture, time.Now().Add(-time.Second))

	created, err := queries.CreateSettlementRecoveryJob(ctx, params)
	if err != nil {
		t.Fatalf("create settlement recovery job: %v", err)
	}
	if created.Status != "pending" || created.AttemptCount != 0 {
		t.Fatalf("expected pending attempt_count=0, got status=%q attempt_count=%d", created.Status, created.AttemptCount)
	}

	duplicated, err := queries.CreateSettlementRecoveryJob(ctx, params)
	if err != nil {
		t.Fatalf("create duplicate same recovery job: %v", err)
	}
	if duplicated.ID != created.ID {
		t.Fatalf("expected duplicate create to return job %d, got %d", created.ID, duplicated.ID)
	}

	conflicting := params
	conflicting.ResponseModelID = "openai/other-model"
	_, err = queries.CreateSettlementRecoveryJob(ctx, conflicting)
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expected conflicting recovery facts to return no rows, got %v", err)
	}

	claimed, err := queries.ClaimNextSettlementRecoveryJob(ctx, sqlc.ClaimNextSettlementRecoveryJobParams{
		LockedBy:    pgtype.Text{String: "worker-a", Valid: true},
		LockedUntil: timestamptz(time.Now().Add(time.Minute)),
		NowAt:       timestamptz(time.Now()),
	})
	if err != nil {
		t.Fatalf("claim recovery job: %v", err)
	}
	if claimed.ID != created.ID || claimed.Status != "running" || claimed.AttemptCount != 1 {
		t.Fatalf("expected claimed running job %d attempt 1, got id=%d status=%q attempt=%d", created.ID, claimed.ID, claimed.Status, claimed.AttemptCount)
	}
	if !claimed.LockedBy.Valid || claimed.LockedBy.String != "worker-a" || !claimed.LockedUntil.Valid {
		t.Fatalf("expected worker lock, got locked_by=%#v locked_until=%#v", claimed.LockedBy, claimed.LockedUntil)
	}

	_, err = queries.ClaimNextSettlementRecoveryJob(ctx, sqlc.ClaimNextSettlementRecoveryJobParams{
		LockedBy:    pgtype.Text{String: "worker-b", Valid: true},
		LockedUntil: timestamptz(time.Now().Add(time.Minute)),
		NowAt:       timestamptz(time.Now()),
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expected locked job not to be claimed again, got %v", err)
	}

	retried, err := queries.MarkSettlementRecoveryJobRetry(ctx, sqlc.MarkSettlementRecoveryJobRetryParams{
		ID:                      claimed.ID,
		LockedBy:                claimed.LockedBy,
		LockedUntil:             claimed.LockedUntil,
		AttemptCount:            claimed.AttemptCount,
		NextRunAt:               timestamptz(time.Now().Add(time.Second)),
		LastErrorCode:           pgtype.Text{String: "gateway_chat_settlement_failed", Valid: true},
		LastErrorMessage:        pgtype.Text{String: "Settlement recovery failed.", Valid: true},
		LastInternalErrorDetail: pgtype.Text{String: "temporary failure", Valid: true},
		UpdatedAt:               timestamptz(time.Now()),
	})
	if err != nil {
		t.Fatalf("mark recovery retry: %v", err)
	}
	if retried.Status != "pending" || retried.LockedBy.Valid || retried.LockedUntil.Valid {
		t.Fatalf("expected retry to clear lock and return pending, got status=%q locked_by=%#v locked_until=%#v", retried.Status, retried.LockedBy, retried.LockedUntil)
	}

	_, err = queries.MarkSettlementRecoveryJobSucceeded(ctx, sqlc.MarkSettlementRecoveryJobSucceededParams{
		ID:          retried.ID,
		CompletedAt: timestamptz(time.Now()),
	})
	if err != nil {
		t.Fatalf("mark pending recovery succeeded: %v", err)
	}

	again, err := queries.MarkSettlementRecoveryJobSucceeded(ctx, sqlc.MarkSettlementRecoveryJobSucceededParams{
		ID:          retried.ID,
		CompletedAt: timestamptz(time.Now().Add(time.Second)),
	})
	if err != nil {
		t.Fatalf("repeat mark recovery succeeded: %v", err)
	}
	if again.Status != "succeeded" || !again.CompletedAt.Valid {
		t.Fatalf("expected idempotent succeeded job, got status=%q completed_at=%#v", again.Status, again.CompletedAt)
	}
}

func TestSettlementRecoveryJobRejectsStaleWorkerStateUpdate(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	fixture := createSettlementRecoveryFixture(t, ctx, tx, queries)
	created, err := queries.CreateSettlementRecoveryJob(ctx, settlementRecoveryJobParams(fixture, time.Now().Add(-time.Second)))
	if err != nil {
		t.Fatalf("create settlement recovery job: %v", err)
	}

	claimedA, err := queries.ClaimNextSettlementRecoveryJob(ctx, sqlc.ClaimNextSettlementRecoveryJobParams{
		LockedBy:    pgtype.Text{String: "worker-a", Valid: true},
		LockedUntil: timestamptz(time.Now().Add(time.Minute)),
		NowAt:       timestamptz(time.Now()),
	})
	if err != nil {
		t.Fatalf("claim recovery job by worker-a: %v", err)
	}

	_, err = tx.Exec(ctx, `
		UPDATE settlement_recovery_jobs
		SET locked_until = now() - interval '1 second',
		    updated_at = now()
		WHERE id = $1
	`, claimedA.ID)
	if err != nil {
		t.Fatalf("expire worker-a lock: %v", err)
	}

	claimedB, err := queries.ClaimNextSettlementRecoveryJob(ctx, sqlc.ClaimNextSettlementRecoveryJobParams{
		LockedBy:    pgtype.Text{String: "worker-b", Valid: true},
		LockedUntil: timestamptz(time.Now().Add(time.Minute)),
		NowAt:       timestamptz(time.Now()),
	})
	if err != nil {
		t.Fatalf("claim recovery job by worker-b: %v", err)
	}
	if claimedB.ID != claimedA.ID || claimedB.AttemptCount != claimedA.AttemptCount+1 {
		t.Fatalf("expected worker-b to reclaim same job with incremented attempt, got id=%d attempt=%d", claimedB.ID, claimedB.AttemptCount)
	}

	_, err = queries.MarkSettlementRecoveryJobRetry(ctx, sqlc.MarkSettlementRecoveryJobRetryParams{
		ID:                      claimedA.ID,
		LockedBy:                claimedA.LockedBy,
		LockedUntil:             claimedA.LockedUntil,
		AttemptCount:            claimedA.AttemptCount,
		NextRunAt:               timestamptz(time.Now().Add(time.Second)),
		LastErrorCode:           pgtype.Text{String: "gateway_chat_settlement_failed", Valid: true},
		LastErrorMessage:        pgtype.Text{String: "Settlement recovery failed.", Valid: true},
		LastInternalErrorDetail: pgtype.Text{String: "stale retry", Valid: true},
		UpdatedAt:               timestamptz(time.Now()),
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expected stale worker retry to be rejected, got %v", err)
	}

	_, err = queries.MarkSettlementRecoveryJobDead(ctx, sqlc.MarkSettlementRecoveryJobDeadParams{
		ID:                      claimedA.ID,
		LockedBy:                claimedA.LockedBy,
		LockedUntil:             claimedA.LockedUntil,
		AttemptCount:            claimedA.AttemptCount,
		LastErrorCode:           pgtype.Text{String: "gateway_chat_settlement_failed", Valid: true},
		LastErrorMessage:        pgtype.Text{String: "Settlement recovery failed.", Valid: true},
		LastInternalErrorDetail: pgtype.Text{String: "stale dead", Valid: true},
		CompletedAt:             timestamptz(time.Now()),
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expected stale worker dead mark to be rejected, got %v", err)
	}

	current, err := queries.GetSettlementRecoveryJobByRequest(ctx, created.RequestRecordID)
	if err != nil {
		t.Fatalf("get current recovery job: %v", err)
	}
	if current.Status != "running" || !current.LockedBy.Valid || current.LockedBy.String != "worker-b" {
		t.Fatalf("expected worker-b lock to remain, got status=%q locked_by=%#v", current.Status, current.LockedBy)
	}
	if current.AttemptCount != claimedB.AttemptCount {
		t.Fatalf("expected attempt_count %d, got %d", claimedB.AttemptCount, current.AttemptCount)
	}

	_, err = queries.MarkSettlementRecoveryJobRetry(ctx, sqlc.MarkSettlementRecoveryJobRetryParams{
		ID:                      claimedB.ID,
		LockedBy:                claimedB.LockedBy,
		LockedUntil:             claimedB.LockedUntil,
		AttemptCount:            claimedB.AttemptCount,
		NextRunAt:               timestamptz(time.Now().Add(time.Second)),
		LastErrorCode:           pgtype.Text{String: "gateway_chat_settlement_failed", Valid: true},
		LastErrorMessage:        pgtype.Text{String: "Settlement recovery failed.", Valid: true},
		LastInternalErrorDetail: pgtype.Text{String: "worker-b retry", Valid: true},
		UpdatedAt:               timestamptz(time.Now()),
	})
	if err != nil {
		t.Fatalf("expected current worker retry to succeed: %v", err)
	}
}

func TestSettlementRecoveryJobMarksExhaustedDueJobDead(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	fixture := createSettlementRecoveryFixture(t, ctx, tx, queries)
	created, err := queries.CreateSettlementRecoveryJob(ctx, settlementRecoveryJobParams(fixture, time.Now().Add(-time.Second)))
	if err != nil {
		t.Fatalf("create settlement recovery job: %v", err)
	}

	_, err = tx.Exec(ctx, `
		UPDATE settlement_recovery_jobs
		SET attempt_count = max_attempts,
		    updated_at = now()
		WHERE id = $1
	`, created.ID)
	if err != nil {
		t.Fatalf("force exhausted job: %v", err)
	}

	_, err = queries.ClaimNextSettlementRecoveryJob(ctx, sqlc.ClaimNextSettlementRecoveryJobParams{
		LockedBy:    pgtype.Text{String: "worker-a", Valid: true},
		LockedUntil: timestamptz(time.Now().Add(time.Minute)),
		NowAt:       timestamptz(time.Now()),
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expected exhausted job not to be claimed, got %v", err)
	}

	dead, err := queries.MarkExhaustedSettlementRecoveryJobDead(ctx, sqlc.MarkExhaustedSettlementRecoveryJobDeadParams{
		LastErrorCode:           pgtype.Text{String: "gateway_chat_settlement_failed", Valid: true},
		LastErrorMessage:        pgtype.Text{String: "Settlement recovery attempts exhausted.", Valid: true},
		LastInternalErrorDetail: pgtype.Text{String: "max attempts reached", Valid: true},
		CompletedAt:             timestamptz(time.Now()),
		NowAt:                   timestamptz(time.Now()),
	})
	if err != nil {
		t.Fatalf("mark exhausted recovery dead: %v", err)
	}
	if dead.ID != created.ID || dead.Status != "dead" || !dead.CompletedAt.Valid {
		t.Fatalf("expected exhausted job dead, got id=%d status=%q completed_at=%#v", dead.ID, dead.Status, dead.CompletedAt)
	}
}
