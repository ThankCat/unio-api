package billing

import (
	"fmt"
	"math/big"
)

// MarginViolation 定位一个售价低于上游成本的计价分项。
type MarginViolation struct {
	Component string
	Sale      string
	Cost      string
}

type normalizedRatePair struct {
	component string
	sale      *big.Rat
	cost      *big.Rat
}

// ValidateNonNegativeMargin 精确比较客户售价与渠道成本的全部归一化分项。
func ValidateNonNegativeMargin(sale CustomerPriceSnapshot, cost ProviderCostSnapshot) ([]MarginViolation, error) {
	pairs, err := normalizedSaleCostPairs(sale, cost)
	if err != nil {
		return nil, err
	}

	violations := make([]MarginViolation, 0)
	for _, pair := range pairs {
		if pair.sale.Cmp(pair.cost) < 0 {
			violations = append(violations, MarginViolation{
				Component: pair.component,
				Sale:      pair.sale.RatString(),
				Cost:      pair.cost.RatString(),
			})
		}
	}
	return violations, nil
}

// normalizedSaleCostPairs returns the seven normalized pricing components shared by
// margin validation and cost-aware routing. Keeping this pairing in one place prevents
// the routing score from drifting from billing fallback semantics.
func normalizedSaleCostPairs(sale CustomerPriceSnapshot, cost ProviderCostSnapshot) ([]normalizedRatePair, error) {
	if sale.Currency != cost.Currency || sale.PricingUnit != cost.PricingUnit {
		return nil, fmt.Errorf("billing: sale/cost currency or pricing unit mismatch")
	}
	saleRates, err := normalizeCustomerPriceRates(sale)
	if err != nil {
		return nil, err
	}
	costRates, err := normalizeProviderCostRates(cost)
	if err != nil {
		return nil, err
	}
	return []normalizedRatePair{
		{"uncached_input", saleRates.UncachedInputRate, costRates.UncachedInputRate},
		{"cache_read_input", saleRates.CacheReadInputRate, costRates.CacheReadInputRate},
		{"cache_write_5m_input", saleRates.CacheWrite5mInputRate, costRates.CacheWrite5mInputRate},
		{"cache_write_1h_input", saleRates.CacheWrite1hInputRate, costRates.CacheWrite1hInputRate},
		{"cache_write_30m_input", saleRates.CacheWrite30mInputRate, costRates.CacheWrite30mInputRate},
		{"output", saleRates.OutputRate, costRates.OutputRate},
		{"reasoning_output", saleRates.ReasoningOutputRate, costRates.ReasoningOutputRate},
	}, nil
}
