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

// ValidateNonNegativeMarginFX 精确比较客户售价与渠道成本的全部归一化分项，并支持跨币种毛利校验（D5）：
// fxRate = 1 售价币种兑多少成本币种（如 USD 售价 vs CNY 成本时传 7.17）。比较采用乘法 sale × rate ≥ cost，
// 纯有理数运算零舍入。同币种时 fxRate 必须为 nil；跨币种缺 fxRate 返回 ErrMissingFxRate（保守拒绝，与 SQL 守卫同语义）。
func ValidateNonNegativeMarginFX(sale CustomerPriceSnapshot, cost ProviderCostSnapshot, fxRate *big.Rat) ([]MarginViolation, error) {
	pairs, err := normalizedSaleCostPairs(sale, cost, fxRate)
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
//
// 跨币种时售价侧逐分项乘以 fxRate，把比较统一到成本币种（D5：乘法路径，无除法舍入）；
// 由此 pair.sale 与 pair.cost 恒为同币种，可直接比较或求比值。
func normalizedSaleCostPairs(sale CustomerPriceSnapshot, cost ProviderCostSnapshot, fxRate *big.Rat) ([]normalizedRatePair, error) {
	if sale.PricingUnit != cost.PricingUnit {
		return nil, fmt.Errorf("billing: sale/cost pricing unit mismatch")
	}
	if sale.Currency != cost.Currency {
		if fxRate == nil || fxRate.Sign() <= 0 {
			return nil, ErrMissingFxRate
		}
	} else {
		// 同币种忽略传入汇率，杜绝「同币种还乘一遍」的误用。
		fxRate = nil
	}
	saleRates, err := normalizeCustomerPriceRates(sale)
	if err != nil {
		return nil, err
	}
	costRates, err := normalizeProviderCostRates(cost)
	if err != nil {
		return nil, err
	}
	pairs := []normalizedRatePair{
		{"uncached_input", saleRates.UncachedInputRate, costRates.UncachedInputRate},
		{"cache_read_input", saleRates.CacheReadInputRate, costRates.CacheReadInputRate},
		{"cache_creation_5m_input", saleRates.CacheCreation5mInputRate, costRates.CacheCreation5mInputRate},
		{"cache_creation_1h_input", saleRates.CacheCreation1hInputRate, costRates.CacheCreation1hInputRate},
		{"cache_creation_30m_input", saleRates.CacheCreation30mInputRate, costRates.CacheCreation30mInputRate},
		{"output", saleRates.OutputRate, costRates.OutputRate},
		{"reasoning_output", saleRates.ReasoningOutputRate, costRates.ReasoningOutputRate},
	}
	if fxRate != nil {
		for i := range pairs {
			pairs[i].sale = new(big.Rat).Mul(pairs[i].sale, fxRate)
		}
	}
	return pairs, nil
}
