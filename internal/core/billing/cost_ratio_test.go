package billing

import (
	"math"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestProviderCostToSaleRatioSelectsMaximumAcrossAllComponents(t *testing.T) {
	components := []struct {
		name string
		set  func(*ProviderCostSnapshot)
	}{
		{"uncached_input", func(cost *ProviderCostSnapshot) { cost.UncachedInputCost = numeric(9, 0) }},
		{"cache_read_input", func(cost *ProviderCostSnapshot) { cost.CacheReadInputCost = numeric(9, 0) }},
		{"cache_write_5m_input", func(cost *ProviderCostSnapshot) { cost.CacheWrite5mInputCost = numeric(9, 0) }},
		{"cache_write_1h_input", func(cost *ProviderCostSnapshot) { cost.CacheWrite1hInputCost = numeric(9, 0) }},
		{"cache_write_30m_input", func(cost *ProviderCostSnapshot) { cost.CacheWrite30mInputCost = numeric(9, 0) }},
		{"output", func(cost *ProviderCostSnapshot) { cost.OutputCost = numeric(9, 0) }},
		{"reasoning_output", func(cost *ProviderCostSnapshot) { cost.ReasoningOutputCost = numeric(9, 0) }},
	}

	for _, component := range components {
		t.Run(component.name, func(t *testing.T) {
			sale := allComponentSalePrice(10)
			cost := allComponentProviderCost(1)
			component.set(&cost)

			ratio, err := ProviderCostToSaleRatio(sale, cost)
			if err != nil {
				t.Fatalf("ProviderCostToSaleRatio returned error: %v", err)
			}
			if math.Abs(ratio-0.9) > 1e-12 {
				t.Fatalf("ratio = %v, want 0.9", ratio)
			}
		})
	}
}

func TestProviderCostToSaleRatioUsesBillingFallbacks(t *testing.T) {
	sale := CustomerPriceSnapshot{
		Currency:           "USD",
		PricingUnit:        PricingUnitPer1MTokens,
		UncachedInputPrice: numeric(8, 0),
		OutputPrice:        numeric(10, 0),
		FormulaVersion:     FormulaVersionV1,
	}
	cost := ProviderCostSnapshot{
		Currency:          "USD",
		PricingUnit:       PricingUnitPer1MTokens,
		UncachedInputCost: numeric(2, 0),
		OutputCost:        numeric(5, 0),
		FormulaVersion:    FormulaVersionV1,
	}

	ratio, err := ProviderCostToSaleRatio(sale, cost)
	if err != nil {
		t.Fatalf("ProviderCostToSaleRatio returned error: %v", err)
	}
	// Missing cache prices fall back to uncached input; missing reasoning falls back to output.
	if math.Abs(ratio-0.5) > 1e-12 {
		t.Fatalf("ratio = %v, want 0.5", ratio)
	}
}

func TestProviderCostToSaleRatioTreatsZeroOverZeroAsZero(t *testing.T) {
	ratio, err := ProviderCostToSaleRatio(allComponentSalePrice(0), allComponentProviderCost(0))
	if err != nil {
		t.Fatalf("ProviderCostToSaleRatio returned error: %v", err)
	}
	if ratio != 0 {
		t.Fatalf("ratio = %v, want 0", ratio)
	}
}

func TestProviderCostToSaleRatioRejectsPositiveCostOverZeroSale(t *testing.T) {
	sale := allComponentSalePrice(0)
	cost := allComponentProviderCost(0)
	cost.CacheWrite30mInputCost = numeric(1, 0)
	if _, err := ProviderCostToSaleRatio(sale, cost); err == nil {
		t.Fatal("positive cost over zero sale must fail closed")
	}
}

func TestProviderCostToSaleRatioRejectsSnapshotMismatch(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ProviderCostSnapshot)
	}{
		{"currency", func(cost *ProviderCostSnapshot) { cost.Currency = "CNY" }},
		{"pricing_unit", func(cost *ProviderCostSnapshot) { cost.PricingUnit = "per_token" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sale := allComponentSalePrice(10)
			cost := allComponentProviderCost(1)
			tt.mutate(&cost)
			if _, err := ProviderCostToSaleRatio(sale, cost); err == nil {
				t.Fatal("mismatched snapshots must fail closed")
			}
		})
	}
}

func TestProviderCostToSaleRatioRejectsInvalidNumeric(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*CustomerPriceSnapshot, *ProviderCostSnapshot)
	}{
		{"required_null", func(sale *CustomerPriceSnapshot, _ *ProviderCostSnapshot) {
			sale.UncachedInputPrice = pgtype.Numeric{}
		}},
		{"negative", func(_ *CustomerPriceSnapshot, cost *ProviderCostSnapshot) {
			cost.OutputCost = numeric(-1, 0)
		}},
		{"nan", func(sale *CustomerPriceSnapshot, _ *ProviderCostSnapshot) {
			sale.OutputPrice = pgtype.Numeric{NaN: true, Valid: true}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sale := allComponentSalePrice(10)
			cost := allComponentProviderCost(1)
			tt.mutate(&sale, &cost)
			if _, err := ProviderCostToSaleRatio(sale, cost); err == nil {
				t.Fatal("invalid numeric must fail closed")
			}
		})
	}
}

func allComponentSalePrice(value int64) CustomerPriceSnapshot {
	return CustomerPriceSnapshot{
		Currency:                "USD",
		PricingUnit:             PricingUnitPer1MTokens,
		UncachedInputPrice:      numeric(value, 0),
		CacheReadInputPrice:     numeric(value, 0),
		CacheWrite5mInputPrice:  numeric(value, 0),
		CacheWrite1hInputPrice:  numeric(value, 0),
		CacheWrite30mInputPrice: numeric(value, 0),
		OutputPrice:             numeric(value, 0),
		ReasoningOutputPrice:    numeric(value, 0),
		FormulaVersion:          FormulaVersionV1,
	}
}

func allComponentProviderCost(value int64) ProviderCostSnapshot {
	return ProviderCostSnapshot{
		Currency:               "USD",
		PricingUnit:            PricingUnitPer1MTokens,
		UncachedInputCost:      numeric(value, 0),
		CacheReadInputCost:     numeric(value, 0),
		CacheWrite5mInputCost:  numeric(value, 0),
		CacheWrite1hInputCost:  numeric(value, 0),
		CacheWrite30mInputCost: numeric(value, 0),
		OutputCost:             numeric(value, 0),
		ReasoningOutputCost:    numeric(value, 0),
		FormulaVersion:         FormulaVersionV1,
	}
}
