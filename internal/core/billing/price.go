package billing

import (
	"math/big"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/jackc/pgx/v5/pgtype"
)

// tokenRateSnapshot 表示待校验的一组 token 单价快照。
// 它是 billing 内部的中性结构，可由客户售价或 provider 成本价转换而来。
type tokenRateSnapshot struct {
	Currency                  string
	PricingUnit               string
	UncachedInputRate         pgtype.Numeric
	CacheReadInputRate        pgtype.Numeric
	CacheCreation5mInputRate  pgtype.Numeric
	CacheCreation1hInputRate  pgtype.Numeric
	CacheCreation30mInputRate pgtype.Numeric
	OutputRate                pgtype.Numeric
	ReasoningOutputRate       pgtype.Numeric
	FormulaVersion            string
}

// tokenRates 表示已校验并转成有理数的 token 单价。
type tokenRates struct {
	Currency                  string
	FormulaVersion            string
	UncachedInputRate         *big.Rat
	CacheReadInputRate        *big.Rat
	CacheCreation5mInputRate  *big.Rat
	CacheCreation1hInputRate  *big.Rat
	CacheCreation30mInputRate *big.Rat
	OutputRate                *big.Rat
	ReasoningOutputRate       *big.Rat
}

// normalizeCustomerPriceRates 校验客户侧售价快照，并转换为可计算的 token 单价。
func normalizeCustomerPriceRates(price CustomerPriceSnapshot) (tokenRates, error) {
	return normalizeTokenRates(tokenRateSnapshot{
		Currency:                  price.Currency,
		PricingUnit:               price.PricingUnit,
		UncachedInputRate:         price.UncachedInputPrice,
		CacheReadInputRate:        price.CacheReadInputPrice,
		CacheCreation5mInputRate:  price.CacheCreation5mInputPrice,
		CacheCreation1hInputRate:  price.CacheCreation1hInputPrice,
		CacheCreation30mInputRate: price.CacheCreation30mInputPrice,
		OutputRate:                price.OutputPrice,
		ReasoningOutputRate:       price.ReasoningOutputPrice,
		FormulaVersion:            price.FormulaVersion,
	})
}

// normalizeProviderCostRates 校验 provider/channel 成本价快照，并转换为可计算的 token 单价。
func normalizeProviderCostRates(cost ProviderCostSnapshot) (tokenRates, error) {
	return normalizeTokenRates(tokenRateSnapshot{
		Currency:                  cost.Currency,
		PricingUnit:               cost.PricingUnit,
		UncachedInputRate:         cost.UncachedInputCost,
		CacheReadInputRate:        cost.CacheReadInputCost,
		CacheCreation5mInputRate:  cost.CacheCreation5mInputCost,
		CacheCreation1hInputRate:  cost.CacheCreation1hInputCost,
		CacheCreation30mInputRate: cost.CacheCreation30mInputCost,
		OutputRate:                cost.OutputCost,
		ReasoningOutputRate:       cost.ReasoningOutputCost,
		FormulaVersion:            cost.FormulaVersion,
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

	cacheCreation5mInputRate := uncachedInputRate
	if snapshot.CacheCreation5mInputRate.Valid {
		cacheCreation5mInputRate, err = requiredNonNegativeNumeric(snapshot.CacheCreation5mInputRate)
		if err != nil {
			return tokenRates{}, err
		}
	}

	cacheCreation1hInputRate := uncachedInputRate
	if snapshot.CacheCreation1hInputRate.Valid {
		cacheCreation1hInputRate, err = requiredNonNegativeNumeric(snapshot.CacheCreation1hInputRate)
		if err != nil {
			return tokenRates{}, err
		}
	}

	cacheCreation30mInputRate := uncachedInputRate
	if snapshot.CacheCreation30mInputRate.Valid {
		cacheCreation30mInputRate, err = requiredNonNegativeNumeric(snapshot.CacheCreation30mInputRate)
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
		Currency:                  snapshot.Currency,
		FormulaVersion:            formulaVersion,
		UncachedInputRate:         uncachedInputRate,
		CacheReadInputRate:        cacheReadInputRate,
		CacheCreation5mInputRate:  cacheCreation5mInputRate,
		CacheCreation1hInputRate:  cacheCreation1hInputRate,
		CacheCreation30mInputRate: cacheCreation30mInputRate,
		OutputRate:                outputRate,
		ReasoningOutputRate:       reasoningOutputRate,
	}, nil
}
