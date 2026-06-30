package billing

import (
	"math/big"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestScaleCustomerPriceMultipliesEachRate(t *testing.T) {
	base := CustomerPriceSnapshot{
		Currency:             "USD",
		PricingUnit:          PricingUnitPer1MTokens,
		UncachedInputPrice:   numeric(2_0000000000, -10),  // 2.0
		OutputPrice:          numeric(8_0000000000, -10),  // 8.0
		CacheReadInputPrice:  nullNumeric(),               // 未配置，应保持 NULL
		ReasoningOutputPrice: numeric(10_0000000000, -10), // 10.0
		FormulaVersion:       FormulaVersionV1,
	}

	scaled, err := ScaleCustomerPrice(base, numeric(15, -1)) // 1.5
	if err != nil {
		t.Fatalf("ScaleCustomerPrice: %v", err)
	}

	assertScaledRate(t, "uncached_input", scaled.UncachedInputPrice, big.NewRat(3, 1))      // 2.0 × 1.5
	assertScaledRate(t, "output", scaled.OutputPrice, big.NewRat(12, 1))                    // 8.0 × 1.5
	assertScaledRate(t, "reasoning_output", scaled.ReasoningOutputPrice, big.NewRat(15, 1)) // 10.0 × 1.5

	if scaled.CacheReadInputPrice.Valid {
		t.Fatal("expected NULL cache_read price to stay NULL after scaling")
	}
	if scaled.Currency != "USD" || scaled.PricingUnit != PricingUnitPer1MTokens || scaled.FormulaVersion != FormulaVersionV1 {
		t.Fatalf("expected currency/unit/formula preserved, got %+v", scaled)
	}
}

func TestScaleCustomerPriceRatioOnePreservesValue(t *testing.T) {
	scaled, err := ScaleCustomerPrice(defaultCustomerPriceSnapshot(), numeric(1, 0)) // 1.0
	if err != nil {
		t.Fatalf("ScaleCustomerPrice: %v", err)
	}

	assertScaledRate(t, "uncached_input", scaled.UncachedInputPrice, big.NewRat(2, 1))
	assertScaledRate(t, "output", scaled.OutputPrice, big.NewRat(8, 1))
}

func TestScaleCustomerPriceRejectsNegativeRatio(t *testing.T) {
	if _, err := ScaleCustomerPrice(defaultCustomerPriceSnapshot(), numeric(-1, 0)); err == nil {
		t.Fatal("expected error for negative ratio")
	}
}

func TestScaleCustomerPriceRejectsInvalidRatio(t *testing.T) {
	if _, err := ScaleCustomerPrice(defaultCustomerPriceSnapshot(), nullNumeric()); err == nil {
		t.Fatal("expected error for NULL ratio")
	}
}

// assertScaledRate 校验缩放后的单价等于期望有理数。
func assertScaledRate(t *testing.T, field string, got pgtype.Numeric, want *big.Rat) {
	t.Helper()

	rat, err := numericToRat(got)
	if err != nil {
		t.Fatalf("%s: numericToRat: %v", field, err)
	}
	if rat.Cmp(want) != 0 {
		t.Fatalf("%s: got %s, want %s", field, rat.String(), want.String())
	}
}
