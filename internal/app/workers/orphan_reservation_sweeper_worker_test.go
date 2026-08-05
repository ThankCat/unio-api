package workers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

type fakeOrphanReservationStore struct {
	rows        []sqlc.LedgerReservation
	attempts    map[int64][]sqlc.ListRunningRequestAttemptPermitsRow
	err         error
	attemptsErr error

	listCalls []sqlc.ListOrphanAuthorizedReservationsParams
}

func (s *fakeOrphanReservationStore) ListOrphanAuthorizedReservations(ctx context.Context, arg sqlc.ListOrphanAuthorizedReservationsParams) ([]sqlc.LedgerReservation, error) {
	s.listCalls = append(s.listCalls, arg)
	if s.err != nil {
		return nil, s.err
	}
	// 第一次返回 rows，后续返回空，避免 RunOnce 调用方误以为无限有活。
	rows := s.rows
	s.rows = nil
	return rows, nil
}

func (s *fakeOrphanReservationStore) ListRunningRequestAttemptPermits(_ context.Context, requestRecordID int64) ([]sqlc.ListRunningRequestAttemptPermitsRow, error) {
	if s.attemptsErr != nil {
		return nil, s.attemptsErr
	}
	return s.attempts[requestRecordID], nil
}

type fakeOrphanReservationFinalizer struct {
	finalized []int64
	failIDs   map[int64]error
}

func (f *fakeOrphanReservationFinalizer) FinalizeOrphanReservation(_ context.Context, reservation sqlc.LedgerReservation, _ []int64, _ []string) (bool, error) {
	if err, ok := f.failIDs[reservation.ID]; ok {
		return false, err
	}
	f.finalized = append(f.finalized, reservation.ID)
	return true, nil
}

type fakeOrphanAttemptPermitReader struct {
	active map[string]bool
	err    error
}

func (r *fakeOrphanAttemptPermitReader) IsAttemptPermitActive(_ context.Context, permitID string) (bool, error) {
	if r.err != nil {
		return false, r.err
	}
	return r.active[permitID], nil
}

func discardLogger() *zap.Logger {
	return zap.NewNop()
}

func TestOrphanReservationSweeperFinalizesBatch(t *testing.T) {
	store := &fakeOrphanReservationStore{
		rows: []sqlc.LedgerReservation{
			{ID: 1, RequestRecordID: 11},
			{ID: 2, RequestRecordID: 22},
		},
	}
	finalizer := &fakeOrphanReservationFinalizer{}
	worker := NewOrphanReservationSweeperWorker(store, finalizer, &fakeOrphanAttemptPermitReader{}, discardLogger(), 15*time.Minute, 100)

	worked, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce returned error: %v", err)
	}
	if !worked {
		t.Fatal("expected worked=true for non-empty batch")
	}
	if len(finalizer.finalized) != 2 {
		t.Fatalf("expected 2 finalized reservations, got %d", len(finalizer.finalized))
	}

	// 校验扫描参数：cutoff = now - ageThreshold（在合理时间窗内），batch 透传。
	if len(store.listCalls) != 1 {
		t.Fatalf("expected 1 list call, got %d", len(store.listCalls))
	}
	call := store.listCalls[0]
	if call.BatchLimit != 100 {
		t.Fatalf("expected batch limit 100, got %d", call.BatchLimit)
	}
	if !call.CreatedBefore.Valid {
		t.Fatal("expected cutoff timestamp to be valid")
	}
	expectedCutoff := time.Now().Add(-15 * time.Minute)
	if diff := call.CreatedBefore.Time.Sub(expectedCutoff); diff > time.Minute || diff < -time.Minute {
		t.Fatalf("cutoff out of expected window: got %v want ~%v", call.CreatedBefore.Time, expectedCutoff)
	}
}

func TestOrphanReservationSweeperContinuesPastSingleFailure(t *testing.T) {
	boom := errors.New("finalize boom")
	store := &fakeOrphanReservationStore{
		rows: []sqlc.LedgerReservation{
			{ID: 1, RequestRecordID: 11},
			{ID: 2, RequestRecordID: 22},
			{ID: 3, RequestRecordID: 33},
		},
	}
	finalizer := &fakeOrphanReservationFinalizer{failIDs: map[int64]error{2: boom}}
	worker := NewOrphanReservationSweeperWorker(store, finalizer, &fakeOrphanAttemptPermitReader{}, discardLogger(), 15*time.Minute, 100)

	worked, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce should not surface single-item failure: %v", err)
	}
	if !worked {
		t.Fatal("expected worked=true")
	}
	// 1 和 3 仍被收口，2 失败被跳过。
	if len(finalizer.finalized) != 2 {
		t.Fatalf("expected 2 finalized (1 and 3), got %v", finalizer.finalized)
	}
}

func TestOrphanReservationSweeperNoRowsReturnsIdle(t *testing.T) {
	store := &fakeOrphanReservationStore{}
	finalizer := &fakeOrphanReservationFinalizer{}
	worker := NewOrphanReservationSweeperWorker(store, finalizer, &fakeOrphanAttemptPermitReader{}, discardLogger(), 0, 0)

	worked, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce returned error: %v", err)
	}
	if worked {
		t.Fatal("expected worked=false when no orphans")
	}
	// 默认值生效。
	if len(store.listCalls) != 1 || store.listCalls[0].BatchLimit != defaultOrphanReservationBatchSize {
		t.Fatalf("expected default batch size %d, got %#v", defaultOrphanReservationBatchSize, store.listCalls)
	}
}

func TestOrphanReservationSweeperListErrorSurfaces(t *testing.T) {
	store := &fakeOrphanReservationStore{err: errors.New("db down")}
	finalizer := &fakeOrphanReservationFinalizer{}
	worker := NewOrphanReservationSweeperWorker(store, finalizer, &fakeOrphanAttemptPermitReader{}, discardLogger(), 15*time.Minute, 100)

	_, err := worker.RunOnce(context.Background())
	if err == nil {
		t.Fatal("expected list error to surface")
	}
}

func TestOrphanReservationSweeperKeepsActivePermit(t *testing.T) {
	store := &fakeOrphanReservationStore{
		rows: []sqlc.LedgerReservation{{ID: 1, RequestRecordID: 11}},
		attempts: map[int64][]sqlc.ListRunningRequestAttemptPermitsRow{
			11: {{ID: 101, PermitID: pgtype.Text{String: "permit-active", Valid: true}}},
		},
	}
	finalizer := &fakeOrphanReservationFinalizer{}
	reader := &fakeOrphanAttemptPermitReader{active: map[string]bool{"permit-active": true}}
	worker := NewOrphanReservationSweeperWorker(store, finalizer, reader, discardLogger(), time.Minute, 100)

	worked, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce returned error: %v", err)
	}
	if worked || len(finalizer.finalized) != 0 {
		t.Fatalf("active permit must be preserved: worked=%v finalized=%v", worked, finalizer.finalized)
	}
}

func TestOrphanReservationSweeperKeepsRequestWhenPermitReadFails(t *testing.T) {
	store := &fakeOrphanReservationStore{
		rows: []sqlc.LedgerReservation{{ID: 1, RequestRecordID: 11}},
		attempts: map[int64][]sqlc.ListRunningRequestAttemptPermitsRow{
			11: {{ID: 101, PermitID: pgtype.Text{String: "permit-unknown", Valid: true}}},
		},
	}
	finalizer := &fakeOrphanReservationFinalizer{}
	reader := &fakeOrphanAttemptPermitReader{err: errors.New("redis unavailable")}
	worker := NewOrphanReservationSweeperWorker(store, finalizer, reader, discardLogger(), time.Minute, 100)

	worked, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce returned error: %v", err)
	}
	if worked || len(finalizer.finalized) != 0 {
		t.Fatalf("unknown permit state must be preserved: worked=%v finalized=%v", worked, finalizer.finalized)
	}
}

func TestOrphanReservationSweeperFinalizesExpiredPermit(t *testing.T) {
	store := &fakeOrphanReservationStore{
		rows: []sqlc.LedgerReservation{{ID: 1, RequestRecordID: 11}},
		attempts: map[int64][]sqlc.ListRunningRequestAttemptPermitsRow{
			11: {{ID: 101, PermitID: pgtype.Text{String: "permit-expired", Valid: true}}},
		},
	}
	finalizer := &fakeOrphanReservationFinalizer{}
	worker := NewOrphanReservationSweeperWorker(store, finalizer, &fakeOrphanAttemptPermitReader{}, discardLogger(), time.Minute, 100)

	worked, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce returned error: %v", err)
	}
	if !worked || len(finalizer.finalized) != 1 {
		t.Fatalf("expired permit must be finalized: worked=%v finalized=%v", worked, finalizer.finalized)
	}
}

func TestOrphanReservationSweeperKeepsUnsafeLegacyAttempt(t *testing.T) {
	store := &fakeOrphanReservationStore{
		rows: []sqlc.LedgerReservation{{ID: 1, RequestRecordID: 11}},
		attempts: map[int64][]sqlc.ListRunningRequestAttemptPermitsRow{
			11: {{ID: 101, UpstreamStartedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true}}},
		},
	}
	finalizer := &fakeOrphanReservationFinalizer{}
	worker := NewOrphanReservationSweeperWorker(store, finalizer, &fakeOrphanAttemptPermitReader{}, discardLogger(), time.Minute, 100)

	worked, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce returned error: %v", err)
	}
	if worked || len(finalizer.finalized) != 0 {
		t.Fatalf("legacy attempt with transport facts must be preserved: worked=%v finalized=%v", worked, finalizer.finalized)
	}
}
