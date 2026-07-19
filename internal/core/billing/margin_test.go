package billing

import "testing"

func TestValidateNonNegativeMarginChecksFallbackAndExplicitComponents(t *testing.T) {
	sale := defaultCustomerPriceSnapshot()
	cost := defaultProviderCostSnapshot()
	cost.UncachedInputCost = numeric(1, 0)
	cost.OutputCost = numeric(1, 0)
	violations, err := ValidateNonNegativeMargin(sale, cost)
	if err != nil || len(violations) != 0 {
		t.Fatalf("expected non-negative margin, violations=%v err=%v", violations, err)
	}

	cost.CacheReadInputCost = numeric(99, 0)
	violations, err = ValidateNonNegativeMargin(sale, cost)
	if err != nil {
		t.Fatalf("validate margin: %v", err)
	}
	if len(violations) != 1 || violations[0].Component != "cache_read_input" {
		t.Fatalf("expected cache_read_input violation, got %#v", violations)
	}
}

func TestValidateNonNegativeMarginRejectsCurrencyMismatch(t *testing.T) {
	sale := defaultCustomerPriceSnapshot()
	cost := defaultProviderCostSnapshot()
	cost.Currency = "CNY"
	if _, err := ValidateNonNegativeMargin(sale, cost); err == nil {
		t.Fatal("currency mismatch must fail closed")
	}
}
