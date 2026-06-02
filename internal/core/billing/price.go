package billing

import (
	"math/big"

	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/jackc/pgx/v5/pgtype"
)

// tokenRateSnapshot 表示待校验的一组 token 单价快照。
// 它是 billing 内部的中性结构，可由客户售价或 provider 成本价转换而来。
type tokenRateSnapshot struct {
	Currency              string
	PricingUnit           string
	UncachedInputRate     pgtype.Numeric
	CacheReadInputRate    pgtype.Numeric
	CacheWrite5mInputRate pgtype.Numeric
	CacheWrite1hInputRate pgtype.Numeric
	OutputRate            pgtype.Numeric
	ReasoningOutputRate   pgtype.Numeric
	FormulaVersion        string
}

// tokenRates 表示已校验并转成有理数的 token 单价。
type tokenRates struct {
	Currency              string
	FormulaVersion        string
	UncachedInputRate     *big.Rat
	CacheReadInputRate    *big.Rat
	CacheWrite5mInputRate *big.Rat
	CacheWrite1hInputRate *big.Rat
	OutputRate            *big.Rat
	ReasoningOutputRate   *big.Rat
}

// normalizeCustomerPriceRates 校验客户侧售价快照，并转换为可计算的 token 单价。
func normalizeCustomerPriceRates(price CustomerPriceSnapshot) (tokenRates, error) {
	return normalizeTokenRates(tokenRateSnapshot{
		Currency:              price.Currency,
		PricingUnit:           price.PricingUnit,
		UncachedInputRate:     price.UncachedInputPrice,
		CacheReadInputRate:    price.CacheReadInputPrice,
		CacheWrite5mInputRate: price.CacheWrite5mInputPrice,
		CacheWrite1hInputRate: price.CacheWrite1hInputPrice,
		OutputRate:            price.OutputPrice,
		ReasoningOutputRate:   price.ReasoningOutputPrice,
		FormulaVersion:        price.FormulaVersion,
	})
}

// normalizeProviderCostRates 校验 provider/channel 成本价快照，并转换为可计算的 token 单价。
func normalizeProviderCostRates(cost ProviderCostSnapshot) (tokenRates, error) {
	return normalizeTokenRates(tokenRateSnapshot{
		Currency:              cost.Currency,
		PricingUnit:           cost.PricingUnit,
		UncachedInputRate:     cost.UncachedInputCost,
		CacheReadInputRate:    cost.CacheReadInputCost,
		CacheWrite5mInputRate: cost.CacheWrite5mInputCost,
		CacheWrite1hInputRate: cost.CacheWrite1hInputCost,
		OutputRate:            cost.OutputCost,
		ReasoningOutputRate:   cost.ReasoningOutputCost,
		FormulaVersion:        cost.FormulaVersion,
	})
}

// normalizeTokenRates 执行客户售价和 provider 成本价共用的基础单价校验。
func normalizeTokenRates(snapshot tokenRateSnapshot) (tokenRates, error) {
	if snapshot.PricingUnit != PricingUnitPer1MTokens {
		return tokenRates{}, failure.Wrap(
			failure.CodeBillingUnsupportedPricingUnit,
			ErrUnsupportedPricingUnit,
			failure.WithMessage(ErrUnsupportedPricingUnit.Error()),
		)
	}

	if snapshot.Currency == "" {
		return tokenRates{}, failure.Wrap(
			failure.CodeBillingInvalidPrice,
			ErrInvalidRate,
			failure.WithMessage(ErrInvalidRate.Error()),
		)
	}

	formulaVersion := snapshot.FormulaVersion
	if formulaVersion == "" {
		formulaVersion = FormulaVersionV1
	}
	if formulaVersion != FormulaVersionV1 {
		return tokenRates{}, failure.Wrap(
			failure.CodeBillingUnsupportedFormula,
			ErrUnsupportedFormula,
			failure.WithMessage(ErrUnsupportedFormula.Error()),
		)
	}

	uncachedInputRate, err := requiredNonNegativeNumeric(snapshot.UncachedInputRate)
	if err != nil {
		return tokenRates{}, err
	}

	outputRate, err := requiredNonNegativeNumeric(snapshot.OutputRate)
	if err != nil {
		return tokenRates{}, err
	}

	cacheReadInputRate := uncachedInputRate
	if snapshot.CacheReadInputRate.Valid {
		cacheReadInputRate, err = requiredNonNegativeNumeric(snapshot.CacheReadInputRate)
		if err != nil {
			return tokenRates{}, err
		}
	}

	cacheWrite5mInputRate := uncachedInputRate
	if snapshot.CacheWrite5mInputRate.Valid {
		cacheWrite5mInputRate, err = requiredNonNegativeNumeric(snapshot.CacheWrite5mInputRate)
		if err != nil {
			return tokenRates{}, err
		}
	}

	cacheWrite1hInputRate := uncachedInputRate
	if snapshot.CacheWrite1hInputRate.Valid {
		cacheWrite1hInputRate, err = requiredNonNegativeNumeric(snapshot.CacheWrite1hInputRate)
		if err != nil {
			return tokenRates{}, err
		}
	}

	reasoningOutputRate := outputRate
	if snapshot.ReasoningOutputRate.Valid {
		reasoningOutputRate, err = requiredNonNegativeNumeric(snapshot.ReasoningOutputRate)
		if err != nil {
			return tokenRates{}, err
		}
	}

	return tokenRates{
		Currency:              snapshot.Currency,
		FormulaVersion:        formulaVersion,
		UncachedInputRate:     uncachedInputRate,
		CacheReadInputRate:    cacheReadInputRate,
		CacheWrite5mInputRate: cacheWrite5mInputRate,
		CacheWrite1hInputRate: cacheWrite1hInputRate,
		OutputRate:            outputRate,
		ReasoningOutputRate:   reasoningOutputRate,
	}, nil
}
