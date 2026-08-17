package providerops

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

type providerOpsTableStore struct {
	Store
	rows  []sqlc.ProvidersOpsTableRow
	total int64
}

type providerOpsDetailStore struct {
	Store
	row sqlc.ProviderOpsDetailRow
}

func (store *providerOpsDetailStore) ProviderOpsDetail(context.Context, sqlc.ProviderOpsDetailParams) (sqlc.ProviderOpsDetailRow, error) {
	return store.row, nil
}

func (store *providerOpsTableStore) ProvidersOpsTable(context.Context, sqlc.ProvidersOpsTableParams) ([]sqlc.ProvidersOpsTableRow, error) {
	return store.rows, nil
}

func (store *providerOpsTableStore) ProvidersOpsTableCount(context.Context, sqlc.ProvidersOpsTableCountParams) (int64, error) {
	return store.total, nil
}

func TestTableReturnsProviderOriginFact(t *testing.T) {
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	store := &providerOpsTableStore{
		total: 1,
		rows: []sqlc.ProvidersOpsTableRow{{
			ID: 1, Slug: "starapi", Name: "StarAPI", Status: "enabled",
			Origin: "https://api.example.com/v1", OriginRevision: 3, StatusRevision: 4,
			CreatedAt: pgtype.Timestamptz{Time: now, Valid: true},
		}},
	}

	rows, total, err := NewService(store).Table(context.Background(), TableParams{Limit: 20})
	if err != nil {
		t.Fatalf("Table returned error: %v", err)
	}
	if total != 1 || len(rows) != 1 {
		t.Fatalf("unexpected page: total=%d rows=%d", total, len(rows))
	}
	if rows[0].Origin != "https://api.example.com/v1" || rows[0].OriginRevision != 3 || rows[0].StatusRevision != 4 {
		t.Fatalf("unexpected provider origin fact: %+v", rows[0])
	}
}

func TestDetailMapsCacheRates(t *testing.T) {
	store := &providerOpsDetailStore{row: sqlc.ProviderOpsDetailRow{
		CacheUncachedInput:    380,
		CacheReadInput:        100,
		CacheWrite30mInput:    20,
		CacheUsageRecords:     4,
		CacheEvaluableRecords: 4,
	}}

	detail, err := NewService(store).Detail(context.Background(), 7, time.Time{}, time.Time{})
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
