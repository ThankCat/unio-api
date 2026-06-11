package price_test

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-api/internal/service/admin/price"
)

type fakeStore struct {
	model       sqlc.Model
	modelErr    error
	getPrice    sqlc.Price
	getPriceErr error
	listRows    []sqlc.Price
	createRow   sqlc.Price
	createErr   error
	createParam sqlc.CreatePriceParams
	createCalls int
	updateRow   sqlc.Price
	updateErr   error
}

func (s *fakeStore) LookupModelByID(context.Context, int64) (sqlc.Model, error) {
	return s.model, s.modelErr
}
func (s *fakeStore) GetPrice(context.Context, int64) (sqlc.Price, error) {
	return s.getPrice, s.getPriceErr
}
func (s *fakeStore) ListPricesByModel(context.Context, int64) ([]sqlc.Price, error) {
	return s.listRows, nil
}
func (s *fakeStore) CreatePrice(_ context.Context, arg sqlc.CreatePriceParams) (sqlc.Price, error) {
	s.createParam = arg
	s.createCalls++
	return s.createRow, s.createErr
}
func (s *fakeStore) UpdatePriceWindow(context.Context, sqlc.UpdatePriceWindowParams) (sqlc.Price, error) {
	return s.updateRow, s.updateErr
}

func baseCreate() price.CreateInput {
	return price.CreateInput{
		ModelID:            2,
		Currency:           "USD",
		PricingUnit:        price.PricingUnitPer1MTokens,
		UncachedInputPrice: "3.00",
		OutputPrice:        "9.00",
		Status:             price.StatusEnabled,
		EffectiveFrom:      time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

func TestCreateRejectsInvalidArguments(t *testing.T) {
	to := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		mutate func(in *price.CreateInput)
	}{
		{"empty currency", func(in *price.CreateInput) { in.Currency = " " }},
		{"bad pricing_unit", func(in *price.CreateInput) { in.PricingUnit = "per_token" }},
		{"bad status", func(in *price.CreateInput) { in.Status = "paused" }},
		{"missing effective_from", func(in *price.CreateInput) { in.EffectiveFrom = time.Time{} }},
		{"effective_to before from", func(in *price.CreateInput) { in.EffectiveTo = &to }},
		{"negative uncached", func(in *price.CreateInput) { in.UncachedInputPrice = "-1" }},
		{"bad output", func(in *price.CreateInput) { in.OutputPrice = "x" }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := baseCreate()
			tc.mutate(&in)
			store := &fakeStore{}
			_, err := price.NewService(store).Create(context.Background(), in)
			if got := failure.CodeOf(err); got != failure.CodeAdminInvalidArgument {
				t.Fatalf("expected %q, got %q", failure.CodeAdminInvalidArgument, got)
			}
			if store.createCalls != 0 {
				t.Fatalf("store should not be called on invalid argument")
			}
		})
	}
}

func TestCreateModelNotFound(t *testing.T) {
	store := &fakeStore{modelErr: pgx.ErrNoRows}
	_, err := price.NewService(store).Create(context.Background(), baseCreate())
	if got := failure.CodeOf(err); got != failure.CodeAdminNotFound {
		t.Fatalf("expected %q, got %q", failure.CodeAdminNotFound, got)
	}
}

func TestCreateOverlapMapsTo422Code(t *testing.T) {
	store := &fakeStore{createErr: &pgconn.PgError{Code: "23P01"}}
	_, err := price.NewService(store).Create(context.Background(), baseCreate())
	if got := failure.CodeOf(err); got != failure.CodeAdminPricingWindowOverlap {
		t.Fatalf("expected %q, got %q", failure.CodeAdminPricingWindowOverlap, got)
	}
}

func TestCreateSuccessParsesAndMaps(t *testing.T) {
	store := &fakeStore{createRow: validRow()}
	in := baseCreate()
	in.Currency = "  USD  "
	got, err := price.NewService(store).Create(context.Background(), in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if store.createParam.Currency != "USD" {
		t.Fatalf("expected trimmed currency, got %q", store.createParam.Currency)
	}
	if !store.createParam.UncachedInputPrice.Valid || !store.createParam.OutputPrice.Valid {
		t.Fatalf("expected required prices parsed to valid numerics")
	}
	if store.createParam.CacheReadInputPrice.Valid {
		t.Fatalf("expected nil optional price to be SQL NULL")
	}
	if got.UncachedInputPrice != "3.00" || got.OutputPrice != "9.00" {
		t.Fatalf("unexpected mapped prices: %+v", got)
	}
}

func TestUpdateNotFound(t *testing.T) {
	store := &fakeStore{getPriceErr: pgx.ErrNoRows}
	_, err := price.NewService(store).Update(context.Background(), price.UpdateInput{
		ID: 5, Status: price.StatusDisabled,
	})
	if got := failure.CodeOf(err); got != failure.CodeAdminNotFound {
		t.Fatalf("expected %q, got %q", failure.CodeAdminNotFound, got)
	}
}

func TestUpdateOverlapMapsTo422Code(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store := &fakeStore{
		getPrice:  sqlc.Price{ID: 5, ModelID: 2, EffectiveFrom: pgtype.Timestamptz{Time: from, Valid: true}},
		updateErr: &pgconn.PgError{Code: "23P01"},
	}
	_, err := price.NewService(store).Update(context.Background(), price.UpdateInput{
		ID: 5, Status: price.StatusEnabled,
	})
	if got := failure.CodeOf(err); got != failure.CodeAdminPricingWindowOverlap {
		t.Fatalf("expected %q, got %q", failure.CodeAdminPricingWindowOverlap, got)
	}
}

func validRow() sqlc.Price {
	return sqlc.Price{
		ID: 7, ModelID: 2, Currency: "USD", PricingUnit: price.PricingUnitPer1MTokens,
		UncachedInputPrice: pgtype.Numeric{Int: big.NewInt(300), Exp: -2, Valid: true},
		OutputPrice:        pgtype.Numeric{Int: big.NewInt(900), Exp: -2, Valid: true},
		Status:             price.StatusEnabled,
		EffectiveFrom:      pgtype.Timestamptz{Time: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Valid: true},
		CreatedAt:          pgtype.Timestamptz{Valid: true},
		UpdatedAt:          pgtype.Timestamptz{Valid: true},
	}
}
