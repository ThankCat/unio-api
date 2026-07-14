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

func TestScaleProviderCostByFactorsCombinesPriceAndRecharge(t *testing.T) {
	base := ProviderCostSnapshot{
		Currency:            "USD",
		PricingUnit:         PricingUnitPer1MTokens,
		UncachedInputCost:   numeric(2_0000000000, -10),  // 2.0
		OutputCost:          numeric(10_0000000000, -10), // 10.0
		CacheReadInputCost:  nullNumeric(),               // 未配置，应保持 NULL
		ReasoningOutputCost: numeric(4_0000000000, -10),  // 4.0
		FormulaVersion:      FormulaVersionV1,
	}

	// 价格倍率 1.2 × 充值倍率 0.5 = 0.6。
	scaled, err := ScaleProviderCostByFactors(base, numeric(12, -1), numeric(5, -1))
	if err != nil {
		t.Fatalf("ScaleProviderCostByFactors: %v", err)
	}

	assertScaledRate(t, "uncached_input", scaled.UncachedInputCost, big.NewRat(12, 10))    // 2.0 × 0.6 = 1.2
	assertScaledRate(t, "output", scaled.OutputCost, big.NewRat(6, 1))                     // 10.0 × 0.6 = 6.0
	assertScaledRate(t, "reasoning_output", scaled.ReasoningOutputCost, big.NewRat(24, 10)) // 4.0 × 0.6 = 2.4
	if scaled.CacheReadInputCost.Valid {
		t.Fatal("expected NULL cache_read cost to stay NULL after scaling")
	}
	if scaled.Currency != "USD" || scaled.PricingUnit != PricingUnitPer1MTokens || scaled.FormulaVersion != FormulaVersionV1 {
		t.Fatalf("expected currency/unit/formula preserved, got %+v", scaled)
	}
}

func TestScaleProviderCostByFactorsRechargeOnePreservesPriceScaling(t *testing.T) {
	base := ProviderCostSnapshot{
		Currency:          "USD",
		PricingUnit:       PricingUnitPer1MTokens,
		UncachedInputCost: numeric(2_0000000000, -10), // 2.0
		OutputCost:        numeric(5_0000000000, -10), // 5.0
		FormulaVersion:    FormulaVersionV1,
	}

	// 充值倍率缺省 1.0 时，结果 = 纯价格倍率成本。
	scaled, err := ScaleProviderCostByFactors(base, numeric(15, -1), numeric(1, 0)) // 1.5 × 1.0
	if err != nil {
		t.Fatalf("ScaleProviderCostByFactors: %v", err)
	}
	assertScaledRate(t, "uncached_input", scaled.UncachedInputCost, big.NewRat(3, 1)) // 2.0 × 1.5
	assertScaledRate(t, "output", scaled.OutputCost, big.NewRat(75, 10))              // 5.0 × 1.5
}

func TestScaleProviderCostByFactorsRejectsInvalidFactors(t *testing.T) {
	base := ProviderCostSnapshot{
		Currency:          "USD",
		PricingUnit:       PricingUnitPer1MTokens,
		UncachedInputCost: numeric(2_0000000000, -10),
		OutputCost:        numeric(5_0000000000, -10),
		FormulaVersion:    FormulaVersionV1,
	}

	if _, err := ScaleProviderCostByFactors(base, numeric(-1, 0), numeric(1, 0)); err == nil {
		t.Fatal("expected error for negative price multiplier")
	}
	if _, err := ScaleProviderCostByFactors(base, numeric(1, 0), numeric(-1, 0)); err == nil {
		t.Fatal("expected error for negative recharge factor")
	}
	if _, err := ScaleProviderCostByFactors(base, nullNumeric(), numeric(1, 0)); err == nil {
		t.Fatal("expected error for NULL price multiplier")
	}
	if _, err := ScaleProviderCostByFactors(base, numeric(1, 0), nullNumeric()); err == nil {
		t.Fatal("expected error for NULL recharge factor")
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
