package query_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-api/internal/service/admin/query"
)

type fakeChannelHealthStore struct {
	rows    []sqlc.SystemChannelHealthRow
	err     error
	lastArg sqlc.SystemChannelHealthParams
}

func (f *fakeChannelHealthStore) SystemChannelHealth(_ context.Context, arg sqlc.SystemChannelHealthParams) ([]sqlc.SystemChannelHealthRow, error) {
	f.lastArg = arg
	return f.rows, f.err
}

func TestChannelHealthServiceBuckets(t *testing.T) {
	last := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	store := &fakeChannelHealthStore{
		rows: []sqlc.SystemChannelHealthRow{
			{ChannelID: 1, Name: "healthy-ch", Status: "enabled", AttemptTotal: 100, AttemptSucceeded: 99, AttemptFailed: 1, AttemptUpstreamFailed: 1, LastAttemptAt: pgtype.Timestamptz{Time: last, Valid: true}},
			{ChannelID: 2, Name: "degraded-ch", Status: "enabled", AttemptTotal: 100, AttemptSucceeded: 85, AttemptFailed: 15, AttemptUpstreamFailed: 15},
			{ChannelID: 3, Name: "unhealthy-ch", Status: "enabled", AttemptTotal: 100, AttemptSucceeded: 50, AttemptFailed: 50, AttemptUpstreamFailed: 50},
			{ChannelID: 4, Name: "idle-ch", Status: "disabled", AttemptTotal: 0},
		},
	}
	svc := query.NewChannelHealthService(store)

	from := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	items, err := svc.List(context.Background(), &from, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 4 {
		t.Fatalf("expected 4 channels, got %d", len(items))
	}

	wantBucket := map[int64]string{1: "healthy", 2: "degraded", 3: "unhealthy", 4: "no_data"}
	for _, it := range items {
		if got := wantBucket[it.ChannelID]; got != it.Bucket {
			t.Fatalf("channel %d: want bucket %q, got %q (rate=%v)", it.ChannelID, got, it.Bucket, it.SuccessRate)
		}
	}

	var healthy, idle query.ChannelHealth
	for _, it := range items {
		switch it.ChannelID {
		case 1:
			healthy = it
		case 4:
			idle = it
		}
	}
	if healthy.SuccessRate != 0.99 {
		t.Fatalf("expected success rate 0.99, got %v", healthy.SuccessRate)
	}
	if healthy.LastAttemptAt == nil || !healthy.LastAttemptAt.Equal(last) {
		t.Fatalf("expected last_attempt_at passthrough, got %v", healthy.LastAttemptAt)
	}
	if idle.SuccessRate != 0 || idle.LastAttemptAt != nil {
		t.Fatalf("no_data channel must have zero rate and nil last_attempt_at: %+v", idle)
	}

	if !store.lastArg.FromTime.Valid || !store.lastArg.FromTime.Time.Equal(from) {
		t.Fatalf("from filter not forwarded: %+v", store.lastArg.FromTime)
	}
	if store.lastArg.ToTime.Valid {
		t.Fatalf("to should be NULL when unset: %+v", store.lastArg.ToTime)
	}
}
