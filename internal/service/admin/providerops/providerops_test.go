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

func (s *providerOpsTableStore) ProvidersOpsTable(context.Context, sqlc.ProvidersOpsTableParams) ([]sqlc.ProvidersOpsTableRow, error) {
	return s.rows, nil
}

func (s *providerOpsTableStore) ProvidersOpsTableCount(context.Context, sqlc.ProvidersOpsTableCountParams) (int64, error) {
	return s.total, nil
}

func TestTableDecodesEndpointSummaries(t *testing.T) {
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	store := &providerOpsTableStore{
		total: 2,
		rows: []sqlc.ProvidersOpsTableRow{
			{
				ID: 1, Slug: "starapi", Name: "StarAPI", Status: "enabled",
				CreatedAt: pgtype.Timestamptz{Time: now, Valid: true},
				Endpoints: `[{"id":11,"name":"primary","base_url":"https://api.example.com/v1","status":"enabled"},{"id":12,"name":"backup","base_url":"https://backup.example.com/v1","status":"disabled"}]`,
			},
			{
				ID: 2, Slug: "empty", Name: "Empty", Status: "enabled",
				CreatedAt: pgtype.Timestamptz{Time: now, Valid: true},
				Endpoints: "[]",
			},
		},
	}

	rows, total, err := NewService(store).Table(context.Background(), TableParams{Limit: 20})
	if err != nil {
		t.Fatalf("Table returned error: %v", err)
	}
	if total != 2 || len(rows) != 2 {
		t.Fatalf("unexpected page: total=%d rows=%d", total, len(rows))
	}
	if got := rows[0].Endpoints; len(got) != 2 || got[0].ID != 11 || got[0].BaseURL != "https://api.example.com/v1" || got[1].Status != "disabled" {
		t.Fatalf("unexpected endpoint summaries: %+v", got)
	}
	if rows[1].Endpoints == nil || len(rows[1].Endpoints) != 0 {
		t.Fatalf("empty provider endpoints must be a non-nil empty slice: %#v", rows[1].Endpoints)
	}
}

func TestTableRejectsInvalidEndpointSummaryJSON(t *testing.T) {
	store := &providerOpsTableStore{
		total: 1,
		rows:  []sqlc.ProvidersOpsTableRow{{ID: 1, Endpoints: "not-json"}},
	}

	rows, total, err := NewService(store).Table(context.Background(), TableParams{Limit: 20})
	if err == nil {
		t.Fatal("expected invalid endpoint JSON to fail")
	}
	if rows != nil || total != 0 {
		t.Fatalf("failed table decode must not return partial data: rows=%v total=%d", rows, total)
	}
}
