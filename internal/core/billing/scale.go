package billing

import (
	"math/big"

	"github.com/jackc/pgx/v5/pgtype"
)

// ScaleCustomerPrice 把「模型基准售价」按「线路倍率」逐分项缩放，得到客户最终售价快照（DEC-026）。
//
// 客户售价 = 模型基准价（model_prices）× 线路倍率（routes.price_ratio）。每个单价分量独立相乘并
// 四舍五入到 NUMERIC(20,10)（与 model_prices / price_snapshots 单价同精度，可直接入库快照）。
// 未配置（NULL）的 cache / reasoning 分量保持 NULL —— 计费时由 normalizeTokenRates 回退到已缩放的
// uncached / output，与现有口径一致。Currency / PricingUnit / FormulaVersion 原样保留。
// ratio 无效（NaN/Inf/NULL）或为负时返回 ErrInvalidRate。
func ScaleCustomerPrice(base CustomerPriceSnapshot, ratio pgtype.Numeric) (CustomerPriceSnapshot, error) {
	ratioRat, err := requiredNonNegativeNumeric(ratio)
	if err != nil {
		return CustomerPriceSnapshot{}, err
	}

	scaled := CustomerPriceSnapshot{
		Currency:       base.Currency,
		PricingUnit:    base.PricingUnit,
		FormulaVersion: base.FormulaVersion,
	}

	for _, field := range []struct {
		base   pgtype.Numeric
		target *pgtype.Numeric
	}{
		{base.UncachedInputPrice, &scaled.UncachedInputPrice},
		{base.CacheReadInputPrice, &scaled.CacheReadInputPrice},
		{base.CacheWrite5mInputPrice, &scaled.CacheWrite5mInputPrice},
		{base.CacheWrite1hInputPrice, &scaled.CacheWrite1hInputPrice},
		{base.CacheWrite30mInputPrice, &scaled.CacheWrite30mInputPrice},
		{base.OutputPrice, &scaled.OutputPrice},
		{base.ReasoningOutputPrice, &scaled.ReasoningOutputPrice},
	} {
		scaledRate, err := scaleRate(field.base, ratioRat)
		if err != nil {
			return CustomerPriceSnapshot{}, err
		}
		*field.target = scaledRate
	}

	return scaled, nil
}

// scaleRate 把单个单价乘以倍率；未配置（NULL）单价保持 NULL，不参与缩放（计费时回退到 uncached/output）。
func scaleRate(rate pgtype.Numeric, ratio *big.Rat) (pgtype.Numeric, error) {
	if !rate.Valid {
		return rate, nil
	}

	rateRat, err := numericToRat(rate)
	if err != nil {
		return pgtype.Numeric{}, err
	}

	return ratToNumeric(new(big.Rat).Mul(rateRat, ratio), priceDecimalScale), nil
}
