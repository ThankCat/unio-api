package publicmodels

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

type fakeStore struct {
	models  []sqlc.ListPublicModelsRow
	caps    []sqlc.ListPublicModelCapabilitiesRow
	windows []sqlc.ListPublicModelPriceWindowsRow
	err     error
}

func (f *fakeStore) ListPublicModelPriceWindows(
	context.Context,
	pgtype.Timestamptz,
) ([]sqlc.ListPublicModelPriceWindowsRow, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.windows, nil
}

func (f *fakeStore) ListPublicModels(context.Context) ([]sqlc.ListPublicModelsRow, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.models, nil
}

func (f *fakeStore) ListPublicModelCapabilities(context.Context) ([]sqlc.ListPublicModelCapabilitiesRow, error) {
	return f.caps, nil
}

func num(t *testing.T, s string) pgtype.Numeric {
	t.Helper()
	var n pgtype.Numeric
	if err := n.Scan(s); err != nil {
		t.Fatalf("scan numeric %q: %v", s, err)
	}
	return n
}

// baseRow 是倍率定价（0.2）的最小在售行：in $4 / out $20 / cache read $0.4。
func baseRow(t *testing.T) sqlc.ListPublicModelsRow {
	t.Helper()
	return sqlc.ListPublicModelsRow{
		ID:                  2,
		ModelID:             "gpt-5.6-sol",
		DisplayName:         "GPT-5.6 Sol",
		OwnedBy:             "openai",
		Family:              "gpt-sol",
		Description:         "Frontier GPT-5.6 model",
		KnowledgeCutoff:     "2026-02-16",
		ContextWindowTokens: pgtype.Int8{Int64: 1050000, Valid: true},
		MaxOutputTokens:     pgtype.Int8{Int64: 128000, Valid: true},
		ReleaseDate:         pgtype.Date{Time: time.Date(2026, 7, 9, 0, 0, 0, 0, time.UTC), Valid: true},
		Currency:            "USD",
		PricingUnit:         "per_1m_tokens",
		UncachedInputPrice:  num(t, "4"),
		CacheReadInputPrice: num(t, "0.4"),
		OutputPrice:         num(t, "20"),
		SalePriceRatio:      num(t, "0.2"),
		LabHasLogo:          true,
		PriceEffectiveFrom:  pgtype.Timestamptz{Time: time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC), Valid: true},
	}
}

func strValue(t *testing.T, p *string, want string, field string) {
	t.Helper()
	if p == nil {
		t.Fatalf("%s = nil, want %q", field, want)
	}
	if *p != want {
		t.Fatalf("%s = %q, want %q", field, *p, want)
	}
}

func TestListResolvesRatioPricing(t *testing.T) {
	t.Parallel()
	row := baseRow(t)
	row.LongContextEnabled = true
	row.LongContextThreshold = pgtype.Int8{Int64: 272000, Valid: true}
	row.LongContextInputMultiplier = num(t, "2")
	row.LongContextOutputMultiplier = num(t, "1.5")

	svc := NewService(&fakeStore{models: []sqlc.ListPublicModelsRow{row}})
	models, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("want 1 model, got %d", len(models))
	}
	m := models[0]

	strValue(t, m.Standard.List.UncachedInput, "4", "list uncached input")
	strValue(t, m.Standard.Sale.UncachedInput, "0.8", "sale uncached input")
	strValue(t, m.Standard.Sale.Output, "4", "sale output")
	strValue(t, m.Standard.Sale.CacheRead, "0.08", "sale cache read")
	if m.Standard.Sale.CacheCreation30m != nil {
		t.Fatalf("unset cache write must stay nil, got %q", *m.Standard.Sale.CacheCreation30m)
	}
	strValue(t, m.SaleRatio, "0.2", "sale ratio")
	if m.Fast != nil {
		t.Fatal("fast tier not configured, want nil")
	}
	if m.LongContext == nil {
		t.Fatal("long context enabled, want non-nil")
	}
	if m.LongContext.ThresholdTokens != 272000 || m.LongContext.InputMultiplier != "2" || m.LongContext.OutputMultiplier != "1.5" {
		t.Fatalf("long context = %+v", *m.LongContext)
	}
	if m.Lab != "openai" || !m.LabHasLogo {
		t.Fatalf("lab fields = %q / %v", m.Lab, m.LabHasLogo)
	}
}

func TestListAbsoluteSaleOverridesRatio(t *testing.T) {
	t.Parallel()
	row := baseRow(t)
	// 绝对售价整组配置（两必填项），倍率不再参与，也不对外暴露。
	row.SaleUncachedInputPrice = num(t, "1.5")
	row.SaleOutputPrice = num(t, "7")

	svc := NewService(&fakeStore{models: []sqlc.ListPublicModelsRow{row}})
	models, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	m := models[0]

	strValue(t, m.Standard.Sale.UncachedInput, "1.5", "sale uncached input")
	strValue(t, m.Standard.Sale.Output, "7", "sale output")
	if m.Standard.Sale.CacheRead != nil {
		t.Fatalf("optional absolute item empty must stay nil, got %q", *m.Standard.Sale.CacheRead)
	}
	if m.SaleRatio != nil {
		t.Fatalf("sale ratio must be nil on absolute path, got %q", *m.SaleRatio)
	}
}

func TestListResolvesFastTier(t *testing.T) {
	t.Parallel()
	row := baseRow(t)
	row.FastConfigured = true
	row.FastUncachedInputPrice = num(t, "8")
	row.FastOutputPrice = num(t, "40")
	row.FastCacheReadInputPrice = num(t, "0.8")

	svc := NewService(&fakeStore{models: []sqlc.ListPublicModelsRow{row}})
	models, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	m := models[0]

	if m.Fast == nil {
		t.Fatal("fast configured, want non-nil")
	}
	strValue(t, m.Fast.List.UncachedInput, "8", "fast list uncached")
	strValue(t, m.Fast.Sale.UncachedInput, "1.6", "fast sale uncached")
	strValue(t, m.Fast.Sale.Output, "8", "fast sale output")
	strValue(t, m.Fast.Sale.CacheRead, "0.16", "fast sale cache read")
}

func TestListGroupsCapabilitiesByModel(t *testing.T) {
	t.Parallel()
	rowA := baseRow(t)
	rowB := baseRow(t)
	rowB.ID = 9
	rowB.ModelID = "claude-opus-4-6"
	rowB.OwnedBy = "anthropic"

	svc := NewService(&fakeStore{
		models: []sqlc.ListPublicModelsRow{rowA, rowB},
		caps: []sqlc.ListPublicModelCapabilitiesRow{
			{ModelID: 2, CapabilityKey: "text.input", SupportLevel: "full"},
			{ModelID: 2, CapabilityKey: "tools.function", SupportLevel: "full"},
			{ModelID: 9, CapabilityKey: "text.input", SupportLevel: "limited"},
		},
	})
	models, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("want 2 models, got %d", len(models))
	}
	if len(models[0].Capabilities) != 2 || models[0].Capabilities[1].Key != "tools.function" {
		t.Fatalf("model A capabilities = %+v", models[0].Capabilities)
	}
	if len(models[1].Capabilities) != 1 || models[1].Capabilities[0].SupportLevel != "limited" {
		t.Fatalf("model B capabilities = %+v", models[1].Capabilities)
	}
}

func TestListStoreError(t *testing.T) {
	t.Parallel()
	svc := NewService(&fakeStore{err: errors.New("boom")})
	if _, err := svc.List(context.Background()); err == nil {
		t.Fatal("want error from store")
	}
}

func TestMinSaleRatioPicksLowest(t *testing.T) {
	t.Parallel()
	cheap := baseRow(t)
	cheap.SalePriceRatio = num(t, "0.2")

	mid := baseRow(t)
	mid.ID = 3
	mid.ModelID = "claude-opus-4-6"
	mid.SalePriceRatio = num(t, "0.5")

	svc := NewService(&fakeStore{models: []sqlc.ListPublicModelsRow{mid, cheap}})
	got, err := svc.MinSaleRatio(context.Background())
	if err != nil {
		t.Fatalf("MinSaleRatio: %v", err)
	}
	if got == nil {
		t.Fatal("want min sale ratio, got nil")
	}
	if *got != 0.2 {
		t.Fatalf("min sale ratio = %v, want 0.2", *got)
	}
}

func TestMinSaleRatioIncludesAbsoluteSale(t *testing.T) {
	t.Parallel()
	// 绝对售价 1.5 / 4 = 0.375，应参与「最低折扣」，且比 0.5 更低。
	absolute := baseRow(t)
	absolute.SaleUncachedInputPrice = num(t, "1.5")
	absolute.SaleOutputPrice = num(t, "7")

	ratio := baseRow(t)
	ratio.ID = 3
	ratio.ModelID = "claude-opus-4-6"
	ratio.SalePriceRatio = num(t, "0.5")

	svc := NewService(&fakeStore{models: []sqlc.ListPublicModelsRow{absolute, ratio}})
	got, err := svc.MinSaleRatio(context.Background())
	if err != nil {
		t.Fatalf("MinSaleRatio: %v", err)
	}
	if got == nil {
		t.Fatal("want min sale ratio, got nil")
	}
	if *got != 0.375 {
		t.Fatalf("min sale ratio = %v, want 0.375", *got)
	}
}

func TestMinSaleRatioEmptyCatalog(t *testing.T) {
	t.Parallel()
	svc := NewService(&fakeStore{})
	got, err := svc.MinSaleRatio(context.Background())
	if err != nil {
		t.Fatalf("MinSaleRatio: %v", err)
	}
	if got != nil {
		t.Fatalf("empty catalog want nil, got %v", *got)
	}
}
