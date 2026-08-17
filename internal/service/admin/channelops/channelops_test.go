package channelops

import (
	"context"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

type channelOpsDetailStore struct {
	Store
	row sqlc.ChannelOpsDetailRow
}

func (store *channelOpsDetailStore) ChannelOpsDetail(context.Context, sqlc.ChannelOpsDetailParams) (sqlc.ChannelOpsDetailRow, error) {
	return store.row, nil
}

func TestDetailMapsCacheRates(t *testing.T) {
	store := &channelOpsDetailStore{row: sqlc.ChannelOpsDetailRow{
		CacheUncachedInput:    380,
		CacheReadInput:        100,
		CacheWrite30mInput:    20,
		CacheUsageRecords:     4,
		CacheEvaluableRecords: 4,
	}}

	detail, err := NewService(store).Detail(context.Background(), 9, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("Detail returned error: %v", err)
	}
	if detail.Cache.ReadRate == nil || *detail.Cache.ReadRate != 0.2 {
		t.Fatalf("cache read rate = %v, want 0.2", detail.Cache.ReadRate)
	}
	if detail.Cache.WriteRate == nil || *detail.Cache.WriteRate != 0.04 {
		t.Fatalf("cache write rate = %v, want 0.04", detail.Cache.WriteRate)
	}
}
