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

// ModelPriceToProviderCost 把「模型基准价」向量映射为「成本基数」向量（DEC-031）。
//
// DEC-031 令 model_prices 成为售价与成本的唯一基数：真实成本 = 基准价 × 价格倍率 × 充值倍率。
// model_prices 列与旧 model_reference_costs 列 1:1 对应（*_price ↔ *_cost），故此处只做机械字段映射，
// 不改数值、NULL 分项保持 NULL。供路由 resolveCandidateCost / 结算 / 前端预览三处共用，避免手写映射漂移；
// 映射产出的向量再交给 ScaleProviderCostByFactors 缩放。Currency/PricingUnit/FormulaVersion 原样保留。
func ModelPriceToProviderCost(price CustomerPriceSnapshot) ProviderCostSnapshot {
	return ProviderCostSnapshot{
		Currency:               price.Currency,
		PricingUnit:            price.PricingUnit,
		UncachedInputCost:      price.UncachedInputPrice,
		CacheReadInputCost:     price.CacheReadInputPrice,
		CacheWrite5mInputCost:  price.CacheWrite5mInputPrice,
		CacheWrite1hInputCost:  price.CacheWrite1hInputPrice,
		CacheWrite30mInputCost: price.CacheWrite30mInputPrice,
		OutputCost:             price.OutputPrice,
		ReasoningOutputCost:    price.ReasoningOutputPrice,
		FormulaVersion:         price.FormulaVersion,
	}
}

// ScaleProviderCostByFactors 把参考成本按「价格倍率 × 充值倍率」缩放（DEC-027）。
//
// 两倍率先用 big.Rat 精确相乘，再对每个分项单次缩放并四舍五入到 NUMERIC(20,10)，避免「先各自舍入再相乘」
// 带来的双重舍入误差，保证路由/结算/快照三处成本一致。任一倍率无效或为负返回 ErrInvalidRate。
func ScaleProviderCostByFactors(base ProviderCostSnapshot, priceMultiplier, rechargeFactor pgtype.Numeric) (ProviderCostSnapshot, error) {
	priceRat, err := requiredNonNegativeNumeric(priceMultiplier)
	if err != nil {
		return ProviderCostSnapshot{}, err
	}
	rechargeRat, err := requiredNonNegativeNumeric(rechargeFactor)
	if err != nil {
		return ProviderCostSnapshot{}, err
	}
	return scaleProviderCostByRat(base, new(big.Rat).Mul(priceRat, rechargeRat))
}

// scaleProviderCostByRat 用已合并的 big.Rat 倍率逐分项缩放参考成本（NULL 分项保持 NULL）。
func scaleProviderCostByRat(base ProviderCostSnapshot, multiplierRat *big.Rat) (ProviderCostSnapshot, error) {
	scaled := ProviderCostSnapshot{
		Currency:       base.Currency,
		PricingUnit:    base.PricingUnit,
		FormulaVersion: base.FormulaVersion,
	}

	for _, field := range []struct {
		base   pgtype.Numeric
		target *pgtype.Numeric
	}{
		{base.UncachedInputCost, &scaled.UncachedInputCost},
		{base.CacheReadInputCost, &scaled.CacheReadInputCost},
		{base.CacheWrite5mInputCost, &scaled.CacheWrite5mInputCost},
		{base.CacheWrite1hInputCost, &scaled.CacheWrite1hInputCost},
		{base.CacheWrite30mInputCost, &scaled.CacheWrite30mInputCost},
		{base.OutputCost, &scaled.OutputCost},
		{base.ReasoningOutputCost, &scaled.ReasoningOutputCost},
	} {
		scaledRate, err := scaleRate(field.base, multiplierRat)
		if err != nil {
			return ProviderCostSnapshot{}, err
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
