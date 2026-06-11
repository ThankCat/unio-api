package costprice_test

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-api/internal/service/admin/costprice"
)

type fakeStore struct {
	channel     sqlc.Channel
	channelErr  error
	binding     sqlc.ChannelModel
	bindingErr  error
	getPrice    sqlc.ChannelCostPrice
	getPriceErr error
	listRows    []sqlc.ListChannelCostPricesByChannelRow
	windows     []sqlc.ListEnabledChannelCostPriceWindowsRow
	createRow   sqlc.ChannelCostPrice
	createErr   error
	createParam sqlc.CreateChannelCostPriceParams
	createCalls int
	updateRow   sqlc.ChannelCostPrice
	updateErr   error
}

func (s *fakeStore) GetChannel(context.Context, int64) (sqlc.Channel, error) {
	return s.channel, s.channelErr
}
func (s *fakeStore) GetChannelModel(context.Context, sqlc.GetChannelModelParams) (sqlc.ChannelModel, error) {
	return s.binding, s.bindingErr
}
func (s *fakeStore) GetChannelCostPrice(context.Context, int64) (sqlc.ChannelCostPrice, error) {
	return s.getPrice, s.getPriceErr
}
func (s *fakeStore) ListChannelCostPricesByChannel(context.Context, int64) ([]sqlc.ListChannelCostPricesByChannelRow, error) {
	return s.listRows, nil
}
func (s *fakeStore) ListEnabledChannelCostPriceWindows(context.Context, sqlc.ListEnabledChannelCostPriceWindowsParams) ([]sqlc.ListEnabledChannelCostPriceWindowsRow, error) {
	return s.windows, nil
}
func (s *fakeStore) CreateChannelCostPrice(_ context.Context, arg sqlc.CreateChannelCostPriceParams) (sqlc.ChannelCostPrice, error) {
	s.createParam = arg
	s.createCalls++
	return s.createRow, s.createErr
}
func (s *fakeStore) UpdateChannelCostPriceWindow(context.Context, sqlc.UpdateChannelCostPriceWindowParams) (sqlc.ChannelCostPrice, error) {
	return s.updateRow, s.updateErr
}

func baseCreate() costprice.CreateInput {
	return costprice.CreateInput{
		ChannelID:         1,
		ModelID:           2,
		Currency:          "USD",
		PricingUnit:       costprice.PricingUnitPer1MTokens,
		UncachedInputCost: "1.25",
		OutputCost:        "2.50",
		Status:            costprice.StatusEnabled,
		EffectiveFrom:     time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

func TestCreateRejectsInvalidArguments(t *testing.T) {
	to := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		mutate func(in *costprice.CreateInput)
	}{
		{"empty currency", func(in *costprice.CreateInput) { in.Currency = "  " }},
		{"bad pricing_unit", func(in *costprice.CreateInput) { in.PricingUnit = "per_token" }},
		{"bad status", func(in *costprice.CreateInput) { in.Status = "paused" }},
		{"missing effective_from", func(in *costprice.CreateInput) { in.EffectiveFrom = time.Time{} }},
		{"effective_to before from", func(in *costprice.CreateInput) { in.EffectiveTo = &to }},
		{"negative uncached", func(in *costprice.CreateInput) { in.UncachedInputCost = "-1" }},
		{"bad output", func(in *costprice.CreateInput) { in.OutputCost = "abc" }},
		{"bad optional cache_read", func(in *costprice.CreateInput) { v := "1,5"; in.CacheReadInputCost = &v }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := baseCreate()
			tc.mutate(&in)
			store := &fakeStore{}
			_, err := costprice.NewService(store).Create(context.Background(), in)
			if got := failure.CodeOf(err); got != failure.CodeAdminInvalidArgument {
				t.Fatalf("expected %q, got %q", failure.CodeAdminInvalidArgument, got)
			}
			if store.createCalls != 0 {
				t.Fatalf("store should not be called on invalid argument")
			}
		})
	}
}

func TestCreateRequiresBinding(t *testing.T) {
	store := &fakeStore{bindingErr: pgx.ErrNoRows}
	_, err := costprice.NewService(store).Create(context.Background(), baseCreate())
	if got := failure.CodeOf(err); got != failure.CodeAdminInvalidArgument {
		t.Fatalf("expected %q, got %q", failure.CodeAdminInvalidArgument, got)
	}
}

func TestCreateOverlapReturns422Code(t *testing.T) {
	store := &fakeStore{
		windows: []sqlc.ListEnabledChannelCostPriceWindowsRow{
			{ID: 9, EffectiveFrom: pgtype.Timestamptz{Time: time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC), Valid: true}},
		},
	}
	// 新窗口 [2026-01-01, +inf) 与现有 [2025-06-01, +inf) 重叠。
	_, err := costprice.NewService(store).Create(context.Background(), baseCreate())
	if got := failure.CodeOf(err); got != failure.CodeAdminPricingWindowOverlap {
		t.Fatalf("expected %q, got %q", failure.CodeAdminPricingWindowOverlap, got)
	}
}

func TestCreateDisabledSkipsOverlap(t *testing.T) {
	store := &fakeStore{
		createRow: validRow(),
		windows: []sqlc.ListEnabledChannelCostPriceWindowsRow{
			{ID: 9, EffectiveFrom: pgtype.Timestamptz{Time: time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC), Valid: true}},
		},
	}
	in := baseCreate()
	in.Status = costprice.StatusDisabled
	if _, err := costprice.NewService(store).Create(context.Background(), in); err != nil {
		t.Fatalf("disabled create should skip overlap, got %v", err)
	}
}

func TestCreateSuccessParsesAndMaps(t *testing.T) {
	store := &fakeStore{createRow: validRow()}
	in := baseCreate()
	in.Currency = "  USD  "
	got, err := costprice.NewService(store).Create(context.Background(), in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if store.createParam.Currency != "USD" {
		t.Fatalf("expected trimmed currency, got %q", store.createParam.Currency)
	}
	if !store.createParam.UncachedInputCost.Valid || !store.createParam.OutputCost.Valid {
		t.Fatalf("expected required costs parsed to valid numerics")
	}
	if store.createParam.CacheReadInputCost.Valid {
		t.Fatalf("expected nil optional cost to be SQL NULL")
	}
	if got.UncachedInputCost != "1.25" || got.OutputCost != "2.50" {
		t.Fatalf("unexpected mapped costs: %+v", got)
	}
}

func TestUpdateNotFound(t *testing.T) {
	store := &fakeStore{getPriceErr: pgx.ErrNoRows}
	_, err := costprice.NewService(store).Update(context.Background(), costprice.UpdateInput{
		ID: 5, Status: costprice.StatusDisabled,
	})
	if got := failure.CodeOf(err); got != failure.CodeAdminNotFound {
		t.Fatalf("expected %q, got %q", failure.CodeAdminNotFound, got)
	}
}

func TestUpdateEffectiveToBeforeFrom(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store := &fakeStore{getPrice: sqlc.ChannelCostPrice{
		ID: 5, ChannelID: 1, ModelID: 2,
		EffectiveFrom: pgtype.Timestamptz{Time: from, Valid: true},
	}}
	before := from.Add(-time.Hour)
	_, err := costprice.NewService(store).Update(context.Background(), costprice.UpdateInput{
		ID: 5, Status: costprice.StatusEnabled, EffectiveTo: &before,
	})
	if got := failure.CodeOf(err); got != failure.CodeAdminInvalidArgument {
		t.Fatalf("expected %q, got %q", failure.CodeAdminInvalidArgument, got)
	}
}

func validRow() sqlc.ChannelCostPrice {
	return sqlc.ChannelCostPrice{
		ID: 7, ChannelID: 1, ModelID: 2, Currency: "USD", PricingUnit: costprice.PricingUnitPer1MTokens,
		UncachedInputCost: pgtype.Numeric{Int: big.NewInt(125), Exp: -2, Valid: true},
		OutputCost:        pgtype.Numeric{Int: big.NewInt(250), Exp: -2, Valid: true},
		Status:            costprice.StatusEnabled,
		EffectiveFrom:     pgtype.Timestamptz{Time: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Valid: true},
		CreatedAt:         pgtype.Timestamptz{Valid: true},
		UpdatedAt:         pgtype.Timestamptz{Valid: true},
	}
}
