package workers

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
)

type fakeOrphanReservationStore struct {
	rows []sqlc.LedgerReservation
	err  error

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

type fakeOrphanReservationFinalizer struct {
	finalized []int64
	failIDs   map[int64]error
}

func (f *fakeOrphanReservationFinalizer) FinalizeOrphanReservation(ctx context.Context, reservation sqlc.LedgerReservation) error {
	if err, ok := f.failIDs[reservation.ID]; ok {
		return err
	}
	f.finalized = append(f.finalized, reservation.ID)
	return nil
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestOrphanReservationSweeperFinalizesBatch(t *testing.T) {
	store := &fakeOrphanReservationStore{
		rows: []sqlc.LedgerReservation{
			{ID: 1, RequestRecordID: 11},
			{ID: 2, RequestRecordID: 22},
		},
	}
	finalizer := &fakeOrphanReservationFinalizer{}
	worker := NewOrphanReservationSweeperWorker(store, finalizer, discardLogger(), 15*time.Minute, 100)

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
	worker := NewOrphanReservationSweeperWorker(store, finalizer, discardLogger(), 15*time.Minute, 100)

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
	worker := NewOrphanReservationSweeperWorker(store, finalizer, discardLogger(), 0, 0)

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
	worker := NewOrphanReservationSweeperWorker(store, finalizer, discardLogger(), 15*time.Minute, 100)

	_, err := worker.RunOnce(context.Background())
	if err == nil {
		t.Fatal("expected list error to surface")
	}
}
