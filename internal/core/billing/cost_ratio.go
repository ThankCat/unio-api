package billing

import (
	"math"
	"math/big"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

// ProviderCostToSaleRatio computes the largest provider-cost/customer-sale ratio
// across all seven normalized token-price components. Component ratios are compared
// exactly as rational numbers and converted to float64 only once for routing.
func ProviderCostToSaleRatio(sale CustomerPriceSnapshot, cost ProviderCostSnapshot) (float64, error) {
	pairs, err := normalizedSaleCostPairs(sale, cost)
	if err != nil {
		return 0, err
	}

	maximum := new(big.Rat)
	for _, pair := range pairs {
		if pair.sale.Sign() == 0 {
			if pair.cost.Sign() == 0 {
				continue
			}
			return 0, failure.Wrap(
				failure.CodeBillingInvalidPrice,
				ErrInvalidRate,
				failure.WithMessage("billing: positive provider cost requires a positive customer sale price"),
				failure.WithField("component", pair.component),
			)
		}

		ratio := new(big.Rat).Quo(pair.cost, pair.sale)
		if ratio.Cmp(maximum) > 0 {
			maximum.Set(ratio)
		}
	}

	ratio, _ := maximum.Float64()
	if math.IsNaN(ratio) || math.IsInf(ratio, 0) || ratio < 0 {
		return 0, failure.Wrap(
			failure.CodeBillingInvalidPrice,
			ErrInvalidRate,
			failure.WithMessage(ErrInvalidRate.Error()),
		)
	}
	return ratio, nil
}
