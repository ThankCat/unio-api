package lifecycle

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/core/billing"
	"github.com/ThankCat/unio-gateway/internal/core/ledger"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

func TestFinalizeOrphanReservationRejectsMissingAttemptProof(t *testing.T) {
	d := newChatSettlementDBDeps(t)
	makeReservationOldEnoughForOrphanSweep(t, d)

	rows, err := d.queries.ListOrphanAuthorizedReservations(d.ctx, sqlc.ListOrphanAuthorizedReservationsParams{
		CreatedBefore: pgtype.Timestamptz{Time: time.Now().Add(-15 * time.Minute), Valid: true},
		BatchLimit:    100,
	})
	if err != nil {
		t.Fatalf("list orphan reservations: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != d.reservationID {
		t.Fatalf("expected reservation %d as permit-check candidate, got %+v", d.reservationID, rows)
	}

	reservation, err := d.queries.GetLedgerReservationByRequestRecordID(d.ctx, d.requestRecord.ID)
	if err != nil {
		t.Fatalf("get reservation: %v", err)
	}
	service := NewChatSettlementService(d.pool, d.queries, billing.Service{}, ledger.NewService(d.pool, d.queries))
	if finalized, err := service.FinalizeOrphanReservation(d.ctx, reservation, nil, nil); err != nil {
		t.Fatalf("finalize orphan reservation: %v", err)
	} else if finalized {
		t.Fatal("missing attempt proof must not finalize the request")
	}

	assertRequestAndReservationStatus(t, d, "running", "authorized")
}

func TestFinalizeOrphanReservationRechecksRecoveryJobAfterListing(t *testing.T) {
	d := newChatSettlementDBDeps(t)
	markSettlementAttemptFailed(t, d)
	makeReservationOldEnoughForOrphanSweep(t, d)

	rows, err := d.queries.ListOrphanAuthorizedReservations(d.ctx, sqlc.ListOrphanAuthorizedReservationsParams{
		CreatedBefore: pgtype.Timestamptz{Time: time.Now().Add(-15 * time.Minute), Valid: true},
		BatchLimit:    100,
	})
	if err != nil {
		t.Fatalf("list orphan reservations: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != d.reservationID {
		t.Fatalf("expected reservation %d as initial orphan candidate, got %+v", d.reservationID, rows)
	}

	recovery := NewChatSettlementRecoveryStore(d.queries, time.Minute, 20)
	if _, err := recovery.CreatePendingChatSettlementRecoveryJob(d.ctx, d.params()); err != nil {
		t.Fatalf("create recovery job after orphan listing: %v", err)
	}

	service := NewChatSettlementService(d.pool, d.queries, billing.Service{}, ledger.NewService(d.pool, d.queries))
	if finalized, err := service.FinalizeOrphanReservation(d.ctx, rows[0], nil, nil); err != nil {
		t.Fatalf("finalize stale orphan candidate: %v", err)
	} else if finalized {
		t.Fatal("recovery job must prevent orphan finalization")
	}

	assertRequestAndReservationStatus(t, d, "running", "authorized")
}

func TestFinalizeOrphanReservationClosesExpiredRunningAttempt(t *testing.T) {
	d := newChatSettlementDBDeps(t)
	makeReservationOldEnoughForOrphanSweep(t, d)

	reservation, err := d.queries.GetLedgerReservationByRequestRecordID(d.ctx, d.requestRecord.ID)
	if err != nil {
		t.Fatalf("get reservation: %v", err)
	}
	service := NewChatSettlementService(d.pool, d.queries, billing.Service{}, ledger.NewService(d.pool, d.queries))
	if finalized, err := service.FinalizeOrphanReservation(
		d.ctx,
		reservation,
		[]int64{d.attemptRecord.ID},
		[]string{d.attemptRecord.PermitID.String},
	); err != nil {
		t.Fatalf("finalize expired running attempt: %v", err)
	} else if !finalized {
		t.Fatal("expired running attempt must be finalized")
	}

	assertRequestAndReservationStatus(t, d, "failed", "released")
	var status string
	var errorCode, faultParty pgtype.Text
	var errorScoringSample, errorScoringFailure bool
	if err := d.pool.QueryRow(d.ctx, `
		SELECT status, error_code, fault_party, error_scoring_sample, error_scoring_failure
		FROM request_attempts
		WHERE id = $1
	`, d.attemptRecord.ID).Scan(&status, &errorCode, &faultParty, &errorScoringSample, &errorScoringFailure); err != nil {
		t.Fatalf("get finalized attempt: %v", err)
	}
	if status != "failed" || !errorCode.Valid || errorCode.String != string(failure.CodeGatewayRequestOrphanReclaimed) {
		t.Fatalf("unexpected finalized attempt: status=%q error_code=%v", status, errorCode)
	}
	if errorScoringSample || errorScoringFailure || !faultParty.Valid || faultParty.String != "platform" {
		t.Fatalf("orphaned gateway attempt must not count against channel: sample=%v failure=%v fault=%v", errorScoringSample, errorScoringFailure, faultParty)
	}
}

func TestFinalizeOrphanReservationClosesSafeLegacyRunningAttempt(t *testing.T) {
	d := newChatSettlementDBDeps(t)
	makeReservationOldEnoughForOrphanSweep(t, d)
	if _, err := d.pool.Exec(d.ctx, `UPDATE request_attempts SET permit_id = NULL WHERE id = $1`, d.attemptRecord.ID); err != nil {
		t.Fatalf("clear legacy permit id: %v", err)
	}

	reservation, err := d.queries.GetLedgerReservationByRequestRecordID(d.ctx, d.requestRecord.ID)
	if err != nil {
		t.Fatalf("get reservation: %v", err)
	}
	service := NewChatSettlementService(d.pool, d.queries, billing.Service{}, ledger.NewService(d.pool, d.queries))
	if finalized, err := service.FinalizeOrphanReservation(
		d.ctx,
		reservation,
		[]int64{d.attemptRecord.ID},
		[]string{""},
	); err != nil {
		t.Fatalf("finalize safe legacy running attempt: %v", err)
	} else if !finalized {
		t.Fatal("safe legacy running attempt must be finalized")
	}

	assertRequestAndReservationStatus(t, d, "failed", "released")
}

func TestCreateRecoveryJobRejectsInsertAfterOrphanFinalizeLock(t *testing.T) {
	d := newChatSettlementDBDeps(t)
	markSettlementAttemptFailed(t, d)

	tx, err := d.pool.Begin(d.ctx)
	if err != nil {
		t.Fatalf("begin orphan finalize transaction: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	txQueries := d.queries.WithTx(tx)
	if _, err := txQueries.GetRequestRecordForUpdate(d.ctx, d.requestRecord.ID); err != nil {
		t.Fatalf("lock request for orphan finalize: %v", err)
	}

	creatorConn, err := d.pool.Acquire(d.ctx)
	if err != nil {
		t.Fatalf("acquire recovery creator connection: %v", err)
	}
	defer creatorConn.Release()
	creatorPID := creatorConn.Conn().PgConn().PID()
	recovery := NewChatSettlementRecoveryStore(sqlc.New(creatorConn), time.Minute, 20)

	type createResult struct {
		err error
	}
	resultCh := make(chan createResult, 1)
	go func() {
		_, createErr := recovery.CreatePendingChatSettlementRecoveryJob(d.ctx, d.params())
		resultCh <- createResult{err: createErr}
	}()

	waitForPostgresBlocker(t, d, creatorPID)

	reservationID := d.reservationID
	ledgerService := ledger.NewService(d.pool, d.queries)
	if _, err := ledgerService.ReleaseWithQueries(d.ctx, txQueries, ledger.ReleaseParams{
		RequestRecordID: d.requestRecord.ID,
		ReservationID:   &reservationID,
	}); err != nil {
		t.Fatalf("release orphan reservation in locked transaction: %v", err)
	}
	if _, err := txQueries.MarkRequestFailed(d.ctx, sqlc.MarkRequestFailedParams{
		ErrorCode:           pgtype.Text{String: string(failure.CodeGatewayRequestOrphanReclaimed), Valid: true},
		ErrorMessage:        pgtype.Text{String: "Request failed.", Valid: true},
		InternalErrorDetail: pgtype.Text{String: "test orphan finalization", Valid: true},
		CompletedAt:         pgtype.Timestamptz{Time: time.Now(), Valid: true},
		RequestRecordID:     d.requestRecord.ID,
	}); err != nil {
		t.Fatalf("mark request failed in locked transaction: %v", err)
	}
	if err := tx.Commit(d.ctx); err != nil {
		t.Fatalf("commit orphan finalize transaction: %v", err)
	}

	select {
	case result := <-resultCh:
		if failure.CodeOf(result.err) != failure.CodeGatewayChatSettlementIdempotencyConflict {
			t.Fatalf("late recovery create must be rejected, got %v", result.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("late recovery create did not finish after orphan transaction commit")
	}

	if _, err := d.queries.GetSettlementRecoveryJobByRequest(d.ctx, d.requestRecord.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("late recovery job must not exist, got %v", err)
	}
	assertRequestAndReservationStatus(t, d, "failed", "released")
}

func makeReservationOldEnoughForOrphanSweep(t *testing.T, d *chatSettlementDBDeps) {
	t.Helper()
	if _, err := d.pool.Exec(d.ctx, `
		UPDATE ledger_reservations
		SET created_at = now() - interval '1 hour'
		WHERE id = $1
	`, d.reservationID); err != nil {
		t.Fatalf("age reservation: %v", err)
	}
}

func markSettlementAttemptFailed(t *testing.T, d *chatSettlementDBDeps) {
	t.Helper()
	if _, err := d.pool.Exec(d.ctx, `
		UPDATE request_attempts
		SET status = 'failed', completed_at = now()
		WHERE id = $1
	`, d.attemptRecord.ID); err != nil {
		t.Fatalf("mark attempt failed: %v", err)
	}
}

func assertRequestAndReservationStatus(t *testing.T, d *chatSettlementDBDeps, wantRequest, wantReservation string) {
	t.Helper()
	var requestStatus string
	if err := d.pool.QueryRow(d.ctx, `SELECT status FROM request_records WHERE id = $1`, d.requestRecord.ID).Scan(&requestStatus); err != nil {
		t.Fatalf("read request status: %v", err)
	}
	reservation, err := d.queries.GetLedgerReservationByRequestRecordID(d.ctx, d.requestRecord.ID)
	if err != nil {
		t.Fatalf("read reservation status: %v", err)
	}
	if requestStatus != wantRequest || reservation.Status != wantReservation {
		t.Fatalf("status mismatch: request=%q reservation=%q, want request=%q reservation=%q", requestStatus, reservation.Status, wantRequest, wantReservation)
	}
}

func waitForPostgresBlocker(t *testing.T, d *chatSettlementDBDeps, blockedPID uint32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var blocked bool
		if err := d.pool.QueryRow(d.ctx, `SELECT cardinality(pg_blocking_pids($1)) > 0`, int32(blockedPID)).Scan(&blocked); err != nil {
			t.Fatalf("inspect PostgreSQL blocker: %v", err)
		}
		if blocked {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("recovery job creation did not block on the request row lock")
}
