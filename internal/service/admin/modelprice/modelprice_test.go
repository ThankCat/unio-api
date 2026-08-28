package modelprice

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

// rejectingStore 让所有存储调用失败：定价校验发生在读库之前，
// 用它可以断言「请求被校验挡下」而不是「侥幸通过后死在别处」。
type rejectingStore struct{}

var errStoreUnreachable = errors.New("store must not be reached")

func (rejectingStore) LookupModelByID(context.Context, int64) (sqlc.Model, error) {
	return sqlc.Model{}, errStoreUnreachable
}

func (rejectingStore) GetModelPrice(context.Context, int64) (sqlc.ModelPrice, error) {
	return sqlc.ModelPrice{}, errStoreUnreachable
}

func (rejectingStore) ListModelPricesByModel(context.Context, int64) ([]sqlc.ListModelPricesByModelRow, error) {
	return nil, errStoreUnreachable
}

func (rejectingStore) ListEnabledModelPriceWindows(context.Context, sqlc.ListEnabledModelPriceWindowsParams) ([]sqlc.ListEnabledModelPriceWindowsRow, error) {
	return nil, errStoreUnreachable
}

func (rejectingStore) CreateModelPrice(context.Context, sqlc.CreateModelPriceParams) (sqlc.CreateModelPriceRow, error) {
	return sqlc.CreateModelPriceRow{}, errStoreUnreachable
}

func (rejectingStore) UpdateModelPriceWindow(context.Context, sqlc.UpdateModelPriceWindowParams) (sqlc.ModelPrice, error) {
	return sqlc.ModelPrice{}, errStoreUnreachable
}

// capturingStore 让 Create 走到写库前记下参数，用来断言三种 intent 的复制/清空。
type capturingStore struct {
	rejectingStore
	model  sqlc.Model
	prices []sqlc.ListModelPricesByModelRow
	got    *sqlc.CreateModelPriceParams
}

func (s *capturingStore) LookupModelByID(context.Context, int64) (sqlc.Model, error) {
	return s.model, nil
}

func (s *capturingStore) ListModelPricesByModel(context.Context, int64) ([]sqlc.ListModelPricesByModelRow, error) {
	return s.prices, nil
}

func (s *capturingStore) ListEnabledModelPriceWindows(context.Context, sqlc.ListEnabledModelPriceWindowsParams) ([]sqlc.ListEnabledModelPriceWindowsRow, error) {
	return nil, nil
}

func (s *capturingStore) CreateModelPrice(_ context.Context, arg sqlc.CreateModelPriceParams) (sqlc.CreateModelPriceRow, error) {
	cp := arg
	s.got = &cp
	return sqlc.CreateModelPriceRow{}, errStoreUnreachable
}

func mustNumeric(s string) pgtype.Numeric {
	var n pgtype.Numeric
	if err := n.Scan(s); err != nil {
		panic(err)
	}
	return n
}

func testModel() sqlc.Model {
	return sqlc.Model{ID: 7, ModelID: "gpt-test", DisplayName: "GPT Test"}
}

func currentEnabledRow() sqlc.ListModelPricesByModelRow {
	now := time.Now().UTC()
	return sqlc.ListModelPricesByModelRow{
		ID:                 11,
		ModelID:            7,
		Currency:           "USD",
		PricingUnit:        PricingUnitPer1MTokens,
		UncachedInputPrice: mustNumeric("2.5"),
		OutputPrice:        mustNumeric("15"),
		SalePriceRatio:     mustNumeric("0.2"),
		Status:             StatusEnabled,
		EffectiveFrom:      pgtype.Timestamptz{Time: now.Add(-time.Hour), Valid: true},
		ModelExternalID:    "gpt-test",
		ModelDisplayName:   "GPT Test",
	}
}

// baseCreateInput 是一条只差 intent / 售价配置的合法入参。
func baseCreateInput() CreateInput {
	return CreateInput{
		ModelID:            7,
		Currency:           "USD",
		PricingUnit:        PricingUnitPer1MTokens,
		UncachedInputPrice: "2.5",
		OutputPrice:        "15",
		Status:             StatusEnabled,
		EffectiveFrom:      time.Now().UTC(),
	}
}

func TestCreateRequiresIntent(t *testing.T) {
	svc := NewService(rejectingStore{}, nil, nil)

	_, err := svc.Create(context.Background(), baseCreateInput())
	if err == nil || errors.Is(err, errStoreUnreachable) {
		t.Fatal("create without intent must be rejected before touching the store")
	}
	if failure.CodeOf(err) != failure.CodeAdminInvalidArgument {
		t.Fatalf("error code = %q, want invalid argument", failure.CodeOf(err))
	}
}

// 配基准价允许草稿：没有倍率也没有绝对售价时校验放行，售价留给后续入口。
func TestCreateAllowsBaseWithoutSale(t *testing.T) {
	svc := NewService(rejectingStore{}, nil, nil)

	in := baseCreateInput()
	in.Intent = IntentBase
	if _, err := svc.createInLock(context.Background(), nil, in); !errors.Is(err, errStoreUnreachable) {
		t.Fatalf("base-only draft must pass validation, got %v", err)
	}
}

func TestCreateIntentBaseCopiesCurrentSale(t *testing.T) {
	store := &capturingStore{model: testModel(), prices: []sqlc.ListModelPricesByModelRow{currentEnabledRow()}}
	svc := NewService(store, nil, nil)

	in := baseCreateInput()
	in.Intent = IntentBase
	in.UncachedInputPrice = "3"
	in.OutputPrice = "18"
	if _, err := svc.createInLock(context.Background(), nil, in); !errors.Is(err, errStoreUnreachable) {
		t.Fatalf("expected to reach the store, got %v", err)
	}
	if store.got == nil {
		t.Fatal("create params were not captured")
	}
	if got := numericString(store.got.UncachedInputPrice); got != "3" {
		t.Fatalf("base input = %s, want 3", got)
	}
	if got := numericString(store.got.OutputPrice); got != "18" {
		t.Fatalf("base output = %s, want 18", got)
	}
	if got := numericString(store.got.SalePriceRatio); got != "0.2" {
		t.Fatalf("copied sale ratio = %s, want 0.2", got)
	}
	if store.got.SaleUncachedInputPrice.Valid {
		t.Fatal("base intent must not invent absolute sale prices")
	}
}

func TestCreateIntentSaleRatioCopiesBaseAndKeepsAbsolute(t *testing.T) {
	current := currentEnabledRow()
	current.SaleUncachedInputPrice = mustNumeric("5")
	current.SaleOutputPrice = mustNumeric("30")
	store := &capturingStore{model: testModel(), prices: []sqlc.ListModelPricesByModelRow{current}}
	svc := NewService(store, nil, nil)

	ratio := "0.5"
	in := CreateInput{
		ModelID:        7,
		Intent:         IntentSaleRatio,
		SalePriceRatio: &ratio,
		Status:         StatusEnabled,
		EffectiveFrom:  time.Now().UTC(),
	}
	if _, err := svc.createInLock(context.Background(), nil, in); !errors.Is(err, errStoreUnreachable) {
		t.Fatalf("expected to reach the store, got %v", err)
	}
	if store.got == nil {
		t.Fatal("create params were not captured")
	}
	if got := numericString(store.got.UncachedInputPrice); got != "2.5" {
		t.Fatalf("copied base input = %s, want 2.5", got)
	}
	if got := numericString(store.got.SalePriceRatio); got != "0.5" {
		t.Fatalf("sale ratio = %s, want 0.5", got)
	}
	if got := numericString(store.got.SaleUncachedInputPrice); got != "5" {
		t.Fatalf("kept absolute input = %s, want 5", got)
	}
	if got := numericString(store.got.SaleOutputPrice); got != "30" {
		t.Fatalf("kept absolute output = %s, want 30", got)
	}
}

func TestCreateIntentSaleAbsoluteCopiesBaseAndKeepsRatio(t *testing.T) {
	store := &capturingStore{model: testModel(), prices: []sqlc.ListModelPricesByModelRow{currentEnabledRow()}}
	svc := NewService(store, nil, nil)

	in := CreateInput{
		ModelID: 7,
		Intent:  IntentSaleAbsolute,
		SalePrices: &SalePriceVector{
			UncachedInputPrice: "5",
			OutputPrice:        "30",
		},
		Status:        StatusEnabled,
		EffectiveFrom: time.Now().UTC(),
	}
	if _, err := svc.createInLock(context.Background(), nil, in); !errors.Is(err, errStoreUnreachable) {
		t.Fatalf("expected to reach the store, got %v", err)
	}
	if store.got == nil {
		t.Fatal("create params were not captured")
	}
	if got := numericString(store.got.UncachedInputPrice); got != "2.5" {
		t.Fatalf("copied base input = %s, want 2.5", got)
	}
	if got := numericString(store.got.SalePriceRatio); got != "0.2" {
		t.Fatalf("kept sale ratio = %s, want 0.2", got)
	}
	if got := numericString(store.got.SaleUncachedInputPrice); got != "5" {
		t.Fatalf("sale input = %s, want 5", got)
	}
}

func TestCreateSaleIntentRequiresEffectiveBase(t *testing.T) {
	store := &capturingStore{model: testModel()}
	svc := NewService(store, nil, nil)

	ratio := "0.2"
	_, err := svc.createInLock(context.Background(), nil, CreateInput{
		ModelID:        7,
		Intent:         IntentSaleRatio,
		SalePriceRatio: &ratio,
		Status:         StatusEnabled,
		EffectiveFrom:  time.Now().UTC(),
	})
	if err == nil || errors.Is(err, errStoreUnreachable) || store.got != nil {
		t.Fatal("sale pricing without an effective base must be rejected before insert")
	}
}

// 有 Fast 基准价时，绝对售价必须连 Fast 一起给。倍率即使还在，也不能拿来补 Fast。
func TestCreateRequiresFastSalePricesWhenAbsoluteConfigured(t *testing.T) {
	current := currentEnabledRow()
	current.FastServiceTierID = 8
	current.FastUncachedInputPrice = mustNumeric("4")
	current.FastOutputPrice = mustNumeric("24")
	store := &capturingStore{model: testModel(), prices: []sqlc.ListModelPricesByModelRow{current}}
	svc := NewService(store, nil, nil)

	in := CreateInput{
		ModelID: 7,
		Intent:  IntentSaleAbsolute,
		SalePrices: &SalePriceVector{
			UncachedInputPrice: "5",
			OutputPrice:        "30",
		},
		Status:        StatusEnabled,
		EffectiveFrom: time.Now().UTC(),
	}
	_, err := svc.createInLock(context.Background(), nil, in)
	if err == nil || errors.Is(err, errStoreUnreachable) || store.got != nil {
		t.Fatal("fast tier without its own sale prices must be rejected even when ratio exists")
	}

	in.FastPrices = &FastPriceInput{
		SalePrices: &SalePriceVector{UncachedInputPrice: "8", OutputPrice: "48"},
	}
	if _, err := svc.createInLock(context.Background(), nil, in); !errors.Is(err, errStoreUnreachable) {
		t.Fatalf("expected to reach the store once fast sale prices are supplied, got %v", err)
	}
	if store.got == nil {
		t.Fatal("create params were not captured")
	}
	if got := numericString(store.got.SalePriceRatio); got != "0.2" {
		t.Fatalf("kept sale ratio = %s, want 0.2", got)
	}
	if got := numericString(store.got.FastSaleUncachedInputPrice); got != "8" {
		t.Fatalf("fast sale input = %s, want 8", got)
	}
}

// 只改倍率时 Fast 基准价照抄；已有 Fast 绝对售价也照抄，不当成互斥清掉。
func TestCreateSaleRatioCopiesFastBaseAndKeepsFastAbsolute(t *testing.T) {
	current := currentEnabledRow()
	current.FastServiceTierID = 8
	current.FastUncachedInputPrice = mustNumeric("4")
	current.FastOutputPrice = mustNumeric("24")
	current.SaleUncachedInputPrice = mustNumeric("5")
	current.SaleOutputPrice = mustNumeric("30")
	current.FastSaleUncachedInputPrice = mustNumeric("8")
	current.FastSaleOutputPrice = mustNumeric("48")
	store := &capturingStore{model: testModel(), prices: []sqlc.ListModelPricesByModelRow{current}}
	svc := NewService(store, nil, nil)

	ratio := "0.4"
	in := CreateInput{
		ModelID:        7,
		Intent:         IntentSaleRatio,
		SalePriceRatio: &ratio,
		Status:         StatusEnabled,
		EffectiveFrom:  time.Now().UTC(),
	}
	if _, err := svc.createInLock(context.Background(), nil, in); !errors.Is(err, errStoreUnreachable) {
		t.Fatalf("expected to reach the store, got %v", err)
	}
	if store.got == nil || !store.got.FastConfigured {
		t.Fatal("sale_ratio must copy Fast base from the current window")
	}
	if got := numericString(store.got.FastSaleUncachedInputPrice); got != "8" {
		t.Fatalf("kept fast absolute input = %s, want 8", got)
	}
	if got := numericString(store.got.SaleUncachedInputPrice); got != "5" {
		t.Fatalf("kept standard absolute input = %s, want 5", got)
	}
}

// 倍率必须为正：0 或负数意味着白送或倒付钱。
func TestCreateRejectsNonPositiveSaleRatio(t *testing.T) {
	svc := NewService(rejectingStore{}, nil, nil)

	for _, ratio := range []string{"0", "-0.5"} {
		in := CreateInput{
			ModelID:        7,
			Intent:         IntentSaleRatio,
			SalePriceRatio: &ratio,
			Status:         StatusEnabled,
			EffectiveFrom:  time.Now().UTC(),
		}
		if _, err := svc.createInLock(context.Background(), nil, in); err == nil || errors.Is(err, errStoreUnreachable) {
			t.Fatalf("sale ratio %q must be rejected, got %v", ratio, err)
		}
	}
}

func TestParseFastPriceConfig(t *testing.T) {
	t.Run("missing is not configured", func(t *testing.T) {
		got, err := parseFastPriceConfig(nil)
		if err != nil {
			t.Fatalf("parseFastPriceConfig(nil) error = %v", err)
		}
		if got.configured {
			t.Fatal("missing Fast prices must not be configured")
		}
	})

	t.Run("preserves exact vector and reference", func(t *testing.T) {
		source := openAIFastPricingSource
		checkedAt := time.Date(2026, time.August, 15, 0, 0, 0, 0, time.UTC)
		cacheRead := "0.125"
		got, err := parseFastPriceConfig(&FastPriceInput{
			UncachedInputPrice:  "0.25",
			CacheReadInputPrice: &cacheRead,
			OutputPrice:         "1.00",
			ReferenceSource:     &source,
			ReferenceCheckedAt:  &checkedAt,
		})
		if err != nil {
			t.Fatalf("parseFastPriceConfig() error = %v", err)
		}
		if !got.configured || !got.uncachedInputPrice.Valid || !got.cacheReadInputPrice.Valid || !got.outputPrice.Valid {
			t.Fatalf("Fast vector was not fully parsed: %+v", got)
		}
		if got.referenceSource.String != source || !got.referenceCheckedAt.Valid || !got.referenceCheckedAt.Time.Equal(checkedAt) {
			t.Fatalf("Fast reference mismatch: %+v", got)
		}
	})

	t.Run("requires both primary prices", func(t *testing.T) {
		if _, err := parseFastPriceConfig(&FastPriceInput{OutputPrice: "1.00"}); err == nil {
			t.Fatal("missing Fast uncached input price must fail")
		}
		if _, err := parseFastPriceConfig(&FastPriceInput{UncachedInputPrice: "0.25"}); err == nil {
			t.Fatal("missing Fast output price must fail")
		}
	})

	t.Run("requires reference source and date together", func(t *testing.T) {
		source := openAIFastPricingSource
		if _, err := parseFastPriceConfig(&FastPriceInput{
			UncachedInputPrice: "0.25",
			OutputPrice:        "1.00",
			ReferenceSource:    &source,
		}); err == nil {
			t.Fatal("source without checked date must fail")
		}
	})
}

func TestOfficialFastPriceReference(t *testing.T) {
	expected := map[string]struct {
		input         string
		cacheRead     string
		cacheCreation string
		output        string
	}{
		"gpt-5.6-sol":       {input: "10.00", cacheRead: "1.00", cacheCreation: "12.50", output: "60.00"},
		"gpt-5.6-terra":     {input: "4.00", cacheRead: "0.40", cacheCreation: "5.00", output: "24.00"},
		"gpt-5.6-luna":      {input: "0.40", cacheRead: "0.04", cacheCreation: "0.50", output: "2.40"},
		"gpt-5.5":           {input: "12.50", cacheRead: "1.25", output: "75.00"},
		"gpt-5.4":           {input: "5.00", cacheRead: "0.50", output: "30.00"},
		"gpt-5.4-mini":      {input: "1.50", cacheRead: "0.15", output: "9.00"},
		"gpt-5.2":           {input: "3.50", cacheRead: "0.35", output: "28.00"},
		"gpt-5.1":           {input: "2.50", cacheRead: "0.25", output: "20.00"},
		"gpt-5":             {input: "2.50", cacheRead: "0.25", output: "20.00"},
		"gpt-5-mini":        {input: "0.45", cacheRead: "0.045", output: "3.60"},
		"gpt-4.1":           {input: "3.50", cacheRead: "0.875", output: "14.00"},
		"gpt-4.1-mini":      {input: "0.70", cacheRead: "0.175", output: "2.80"},
		"gpt-4.1-nano":      {input: "0.20", cacheRead: "0.05", output: "0.80"},
		"gpt-4o":            {input: "4.25", cacheRead: "2.125", output: "17.00"},
		"gpt-4o-mini":       {input: "0.25", cacheRead: "0.125", output: "1.00"},
		"o4-mini":           {input: "2.00", cacheRead: "0.50", output: "8.00"},
		"o3":                {input: "3.50", cacheRead: "0.875", output: "14.00"},
		"gpt-4o-2024-05-13": {input: "8.75", output: "26.25"},
	}

	if len(openAIFastPriceReferences) != len(expected) {
		t.Fatalf("Fast reference count = %d, want %d", len(openAIFastPriceReferences), len(expected))
	}
	for model, want := range expected {
		t.Run(model, func(t *testing.T) {
			got := officialFastPriceReference("openai/" + model)
			if got == nil {
				t.Fatalf("expected %s Fast reference", model)
			}
			if got.UncachedInputPrice != want.input || got.OutputPrice != want.output {
				t.Fatalf("Fast reference = %+v, want input=%s output=%s", got, want.input, want.output)
			}
			assertOptionalReferencePrice(t, "cache read", got.CacheReadInputPrice, want.cacheRead)
			assertOptionalReferencePrice(t, "cache creation 30m", got.CacheCreation30mInputPrice, want.cacheCreation)
			if got.Source != openAIFastPricingSource || !got.CheckedAt.Equal(openAIFastPriceCheckedAt) {
				t.Fatalf("Fast reference audit facts = source %q checked %s", got.Source, got.CheckedAt)
			}
		})
	}
	if officialFastPriceReference("gpt-unknown") != nil {
		t.Fatal("unknown model must not receive a fabricated Fast reference")
	}
}

func assertOptionalReferencePrice(t *testing.T, field string, got *string, want string) {
	t.Helper()
	if want == "" {
		if got != nil {
			t.Fatalf("%s = %q, want nil", field, *got)
		}
		return
	}
	if got == nil || *got != want {
		t.Fatalf("%s = %v, want %q", field, got, want)
	}
}
