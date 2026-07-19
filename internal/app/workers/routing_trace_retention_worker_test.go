package workers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

type fakeRoutingTraceRetentionStore struct {
	results []int64
	err     error
	calls   []sqlc.DeleteExpiredRoutingDecisionTracesParams
}

func (s *fakeRoutingTraceRetentionStore) DeleteExpiredRoutingDecisionTraces(_ context.Context, params sqlc.DeleteExpiredRoutingDecisionTracesParams) (int64, error) {
	s.calls = append(s.calls, params)
	if s.err != nil {
		return 0, s.err
	}
	if len(s.results) == 0 {
		return 0, nil
	}
	result := s.results[0]
	s.results = s.results[1:]
	return result, nil
}

func TestRoutingTraceRetentionWorkerDrainsFullBatches(t *testing.T) {
	store := &fakeRoutingTraceRetentionStore{results: []int64{1000, 5}}
	worker := NewRoutingTraceRetentionWorker(store, nil)
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	worker.now = func() time.Time { return now }

	worked, err := worker.RunOnce(context.Background())
	if err != nil || !worked || !worker.draining {
		t.Fatalf("first full batch must keep draining: worked=%v draining=%v err=%v", worked, worker.draining, err)
	}
	worked, err = worker.RunOnce(context.Background())
	if err != nil || !worked || worker.draining {
		t.Fatalf("final partial batch must finish cycle: worked=%v draining=%v err=%v", worked, worker.draining, err)
	}
	if len(store.calls) != 2 {
		t.Fatalf("expected two delete batches, got %d", len(store.calls))
	}
	wantCutoff := now.Add(-7 * 24 * time.Hour)
	if !store.calls[0].Cutoff.Valid || !store.calls[0].Cutoff.Time.Equal(wantCutoff) || store.calls[0].BatchLimit != 1000 {
		t.Fatalf("unexpected default retention params: %+v", store.calls[0])
	}
	worked, err = worker.RunOnce(context.Background())
	if err != nil || worked || len(store.calls) != 2 {
		t.Fatalf("worker must wait until next cleanup interval: worked=%v calls=%d err=%v", worked, len(store.calls), err)
	}
}

func TestRoutingTraceRetentionWorkerSchedulesAfterFailure(t *testing.T) {
	store := &fakeRoutingTraceRetentionStore{err: errors.New("database unavailable")}
	worker := NewRoutingTraceRetentionWorker(store, nil)
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	worker.now = func() time.Time { return now }

	worked, err := worker.RunOnce(context.Background())
	if err == nil || worked || worker.draining {
		t.Fatalf("expected scheduled failure: worked=%v draining=%v err=%v", worked, worker.draining, err)
	}
	if !worker.nextRunAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("unexpected retry time: %v", worker.nextRunAt)
	}
}
