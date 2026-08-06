package lifecycle

import (
	"fmt"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/core/billing"
	"github.com/ThankCat/unio-gateway/internal/core/ledger"
	"github.com/ThankCat/unio-gateway/internal/core/requestlog"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestFinalizeDeadChatSettlementClosesAttemptAndIsIdempotent(t *testing.T) {
	d := newChatSettlementDBDeps(t)
	recoveryStore := NewChatSettlementRecoveryStore(d.queries, time.Minute, 1)
	job, err := recoveryStore.CreatePendingChatSettlementRecoveryJob(d.ctx, d.params())
	if err != nil {
		t.Fatalf("create settlement recovery job: %v", err)
	}

	unrelatedAttempt, err := d.queries.CreateRequestAttempt(d.ctx, sqlc.CreateRequestAttemptParams{
		RequestRecordID:        d.requestRecord.ID,
		PermitID:               pgtype.Text{String: fmt.Sprintf("dead-finalizer-unrelated-%d", d.requestRecord.ID), Valid: true},
		AttemptIndex:           1,
		ProviderID:             d.providerID,
		ChannelID:              d.channelID,
		ProviderOriginRevision: 1,
		ProviderStatusRevision: 1,
		ChannelConfigRevision:  1,
		RoutingCandidateIndex:  1,
		AdapterKey:             "openai",
		UpstreamModel:          "gpt-4.1",
		UpstreamProtocol:       string(requestlog.ProtocolOpenAI),
		UpstreamEndpoint:       string(requestlog.UpstreamEndpointChatCompletions),
		UpstreamResponseModel:  pgtype.Text{Valid: false},
		Status:                 string(requestlog.AttemptStatusRunning),
		UpstreamStatusCode:     pgtype.Int4{Valid: false},
		UpstreamRequestID:      pgtype.Text{Valid: false},
		ErrorCode:              pgtype.Text{Valid: false},
		ErrorMessage:           pgtype.Text{Valid: false},
		StartedAt:              pgtype.Timestamptz{Time: time.Now(), Valid: true},
		CompletedAt:            pgtype.Timestamptz{Valid: false},
	})
	if err != nil {
		t.Fatalf("create unrelated attempt: %v", err)
	}
	if _, err := d.queries.MarkRequestAttemptFailed(d.ctx, sqlc.MarkRequestAttemptFailedParams{
		AttemptID:           unrelatedAttempt.ID,
		ErrorCode:           pgtype.Text{String: "unrelated_failure", Valid: true},
		ErrorMessage:        pgtype.Text{String: "Unrelated attempt failed.", Valid: true},
		InternalErrorDetail: pgtype.Text{String: "test fixture", Valid: true},
		UpstreamStatusCode:  pgtype.Int4{Int32: 502, Valid: true},
		UpstreamRequestID:   pgtype.Text{String: "req-unrelated", Valid: true},
		CompletedAt:         pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}); err != nil {
		t.Fatalf("finalize unrelated attempt: %v", err)
	}

	if _, err := d.pool.Exec(d.ctx, `
		UPDATE settlement_recovery_jobs
		SET status = 'dead',
		    attempt_count = max_attempts,
		    completed_at = now(),
		    updated_at = now()
		WHERE id = $1
	`, job.ID); err != nil {
		t.Fatalf("mark settlement recovery job dead: %v", err)
	}
	job, err = d.queries.GetSettlementRecoveryJobByRequest(d.ctx, d.requestRecord.ID)
	if err != nil {
		t.Fatalf("get dead settlement recovery job: %v", err)
	}
	if job.Status != "dead" {
		t.Fatalf("expected dead settlement recovery job, got %q", job.Status)
	}

	service := NewChatSettlementService(d.pool, d.queries, billing.Service{}, ledger.NewService(d.pool, d.queries))
	if err := service.FinalizeDeadChatSettlement(d.ctx, job); err != nil {
		t.Fatalf("finalize dead settlement: %v", err)
	}

	assertRequestAndReservationStatus(t, d, "failed", "released")
	assertDeadSettlementAttempt(t, d, job)
	assertUnrelatedDeadSettlementAttempt(t, d, unrelatedAttempt.ID)
	firstException, err := d.queries.GetLedgerBillingExceptionByRequest(d.ctx, d.requestRecord.ID)
	if err != nil {
		t.Fatalf("get dead settlement risk exception: %v", err)
	}
	if firstException.ReasonCode != "settlement_recovery_exhausted" {
		t.Fatalf("unexpected dead settlement risk exception: %+v", firstException)
	}

	if err := service.FinalizeDeadChatSettlement(d.ctx, job); err != nil {
		t.Fatalf("replay dead settlement finalize: %v", err)
	}

	assertRequestAndReservationStatus(t, d, "failed", "released")
	assertDeadSettlementAttempt(t, d, job)
	assertUnrelatedDeadSettlementAttempt(t, d, unrelatedAttempt.ID)
	secondException, err := d.queries.GetLedgerBillingExceptionByRequest(d.ctx, d.requestRecord.ID)
	if err != nil {
		t.Fatalf("get replayed dead settlement risk exception: %v", err)
	}
	if secondException.ID != firstException.ID {
		t.Fatalf("dead settlement replay created a second risk exception: first=%d second=%d", firstException.ID, secondException.ID)
	}
}

func TestFinalizeDeadChatSettlementClosesRequestWhenReservationIsMissing(t *testing.T) {
	d := newChatSettlementDBDeps(t)
	recoveryStore := NewChatSettlementRecoveryStore(d.queries, time.Minute, 1)
	job, err := recoveryStore.CreatePendingChatSettlementRecoveryJob(d.ctx, d.params())
	if err != nil {
		t.Fatalf("create settlement recovery job: %v", err)
	}

	// The schema prevents deleting a reservation while its recovery job exists. Keep the
	// worker's already-loaded job snapshot, then remove both rows to exercise the defensive
	// reservation-not-found path against the real ledger and request stores.
	deletedJob, err := d.pool.Exec(d.ctx, `DELETE FROM settlement_recovery_jobs WHERE id = $1`, job.ID)
	if err != nil {
		t.Fatalf("delete settlement recovery job fixture: %v", err)
	}
	if deletedJob.RowsAffected() != 1 {
		t.Fatalf("delete settlement recovery job fixture: affected %d rows", deletedJob.RowsAffected())
	}
	deletedReservation, err := d.pool.Exec(d.ctx, `DELETE FROM ledger_reservations WHERE id = $1`, job.ReservationID)
	if err != nil {
		t.Fatalf("delete settlement reservation fixture: %v", err)
	}
	if deletedReservation.RowsAffected() != 1 {
		t.Fatalf("delete settlement reservation fixture: affected %d rows", deletedReservation.RowsAffected())
	}

	service := NewChatSettlementService(d.pool, d.queries, billing.Service{}, ledger.NewService(d.pool, d.queries))
	if err := service.FinalizeDeadChatSettlement(d.ctx, job); err != nil {
		t.Fatalf("finalize dead settlement without reservation: %v", err)
	}

	assertDeadSettlementRequestStatus(t, d, string(requestlog.RequestStatusFailed))
	assertDeadSettlementAttempt(t, d, job)

	if err := service.FinalizeDeadChatSettlement(d.ctx, job); err != nil {
		t.Fatalf("replay dead settlement finalize without reservation: %v", err)
	}
	assertDeadSettlementRequestStatus(t, d, string(requestlog.RequestStatusFailed))
	assertDeadSettlementAttempt(t, d, job)
}

func assertDeadSettlementRequestStatus(t *testing.T, d *chatSettlementDBDeps, want string) {
	t.Helper()

	var status string
	if err := d.pool.QueryRow(d.ctx, `SELECT status FROM request_records WHERE id = $1`, d.requestRecord.ID).Scan(&status); err != nil {
		t.Fatalf("read dead settlement request status: %v", err)
	}
	if status != want {
		t.Fatalf("unexpected dead settlement request status: got %q want %q", status, want)
	}
}

func assertDeadSettlementAttempt(t *testing.T, d *chatSettlementDBDeps, job sqlc.SettlementRecoveryJob) {
	t.Helper()

	var (
		status, responseID, responseModel, finishReason, finishClass, errorCode string
		statusCode                                                              int
		requestID                                                               pgtype.Text
		finalUsageReceived                                                      bool
		usageMappingVersion                                                     string
	)
	if err := d.pool.QueryRow(d.ctx, `
		SELECT
		    status,
		    upstream_response_id,
		    upstream_response_model,
		    upstream_finish_reason,
		    finish_class,
		    upstream_status_code,
		    upstream_request_id,
		    error_code,
		    final_usage_received,
		    usage_mapping_version
		FROM request_attempts
		WHERE id = $1
	`, job.AttemptID).Scan(
		&status,
		&responseID,
		&responseModel,
		&finishReason,
		&finishClass,
		&statusCode,
		&requestID,
		&errorCode,
		&finalUsageReceived,
		&usageMappingVersion,
	); err != nil {
		t.Fatalf("read finalized dead settlement attempt: %v", err)
	}

	if status != string(requestlog.AttemptStatusFailed) || errorCode != string(failure.CodeGatewayChatSettlementFailed) {
		t.Fatalf("unexpected dead settlement attempt status: status=%q error_code=%q", status, errorCode)
	}
	if responseID != job.UpstreamResponseID || responseModel != job.UpstreamModel || finishReason != job.UpstreamFinishReason || finishClass != job.FinishClass {
		t.Fatalf("dead settlement attempt did not preserve upstream response facts: response=%q/%q finish=%q/%q", responseID, responseModel, finishReason, finishClass)
	}
	if statusCode != int(job.UpstreamStatusCode) || !requestID.Valid || requestID.String != job.UpstreamRequestID.String {
		t.Fatalf("dead settlement attempt did not preserve upstream metadata: status=%d request_id=%#v", statusCode, requestID)
	}
	if !finalUsageReceived || usageMappingVersion != job.UsageMappingVersion {
		t.Fatalf("dead settlement attempt did not persist final usage facts: final=%v mapping=%q", finalUsageReceived, usageMappingVersion)
	}
}

func assertUnrelatedDeadSettlementAttempt(t *testing.T, d *chatSettlementDBDeps, attemptID int64) {
	t.Helper()

	var status, errorCode string
	if err := d.pool.QueryRow(d.ctx, `SELECT status, error_code FROM request_attempts WHERE id = $1`, attemptID).Scan(&status, &errorCode); err != nil {
		t.Fatalf("read unrelated attempt: %v", err)
	}
	if status != string(requestlog.AttemptStatusFailed) || errorCode != "unrelated_failure" {
		t.Fatalf("dead settlement finalizer changed unrelated attempt: status=%q error_code=%q", status, errorCode)
	}
}
