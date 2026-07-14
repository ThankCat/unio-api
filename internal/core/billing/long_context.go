package billing

import (
	"math/big"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-api/internal/core/usage"
)

// LongContextPolicy 是绑定在 model_prices 价格窗口上的长上下文阶梯策略。
//
// 对齐 OpenAI GPT-5.4+ / sub2api：当输入合计（未缓存 + cache_read + 各档 cache_write）
// 超过 Threshold 时，整单输入侧单价 × InputMultiplier、输出侧单价 × OutputMultiplier。
type LongContextPolicy struct {
	Enabled          bool
	Threshold        int64
	InputMultiplier  pgtype.Numeric
	OutputMultiplier pgtype.Numeric
}

// Active 表示策略已启用且参数齐全、可用于判定。
func (p LongContextPolicy) Active() bool {
	return p.Enabled &&
		p.Threshold > 0 &&
		p.InputMultiplier.Valid &&
		p.OutputMultiplier.Valid
}

// LongContextInputTokenSum 汇总用于长上下文判定的输入侧 token。
// unknown / not_applicable 维度按 0（与上游账单「未计入则不推过阈值」一致）。
func LongContextInputTokenSum(facts usage.Facts) int64 {
	sum := int64(0)
	for _, c := range []usage.TokenCount{
		facts.UncachedInputTokens,
		facts.CacheReadInputTokens,
		facts.CacheWrite5mInputTokens,
		facts.CacheWrite1hInputTokens,
		facts.CacheWrite30mInputTokens,
	} {
		if c.IsKnown() && c.Value > 0 {
			sum += c.Value
		}
	}
	return sum
}

// ShouldApplyLongContext 判断给定输入合计是否触发长上下文阶梯。
func ShouldApplyLongContext(policy LongContextPolicy, inputTokenSum int64) bool {
	return policy.Active() && inputTokenSum > policy.Threshold
}

// ApplyLongContextToCustomerPrice 按策略缩放客户售价向量；未触发则原样返回 applied=false。
func ApplyLongContextToCustomerPrice(price CustomerPriceSnapshot, policy LongContextPolicy, inputTokenSum int64) (CustomerPriceSnapshot, bool, error) {
	if !ShouldApplyLongContext(policy, inputTokenSum) {
		return price, false, nil
	}
	inRat, outRat, err := longContextMultiplierRats(policy)
	if err != nil {
		return CustomerPriceSnapshot{}, false, err
	}
	scaled := CustomerPriceSnapshot{
		Currency:       price.Currency,
		PricingUnit:    price.PricingUnit,
		FormulaVersion: price.FormulaVersion,
	}
	for _, field := range []struct {
		base   pgtype.Numeric
		mult   *big.Rat
		target *pgtype.Numeric
	}{
		{price.UncachedInputPrice, inRat, &scaled.UncachedInputPrice},
		{price.CacheReadInputPrice, inRat, &scaled.CacheReadInputPrice},
		{price.CacheWrite5mInputPrice, inRat, &scaled.CacheWrite5mInputPrice},
		{price.CacheWrite1hInputPrice, inRat, &scaled.CacheWrite1hInputPrice},
		{price.CacheWrite30mInputPrice, inRat, &scaled.CacheWrite30mInputPrice},
		{price.OutputPrice, outRat, &scaled.OutputPrice},
		{price.ReasoningOutputPrice, outRat, &scaled.ReasoningOutputPrice},
	} {
		v, err := scaleRate(field.base, field.mult)
		if err != nil {
			return CustomerPriceSnapshot{}, false, err
		}
		*field.target = v
	}
	return scaled, true, nil
}

// ApplyLongContextToProviderCost 按策略缩放渠道成本向量；未触发则原样返回 applied=false。
func ApplyLongContextToProviderCost(cost ProviderCostSnapshot, policy LongContextPolicy, inputTokenSum int64) (ProviderCostSnapshot, bool, error) {
	if !ShouldApplyLongContext(policy, inputTokenSum) {
		return cost, false, nil
	}
	inRat, outRat, err := longContextMultiplierRats(policy)
	if err != nil {
		return ProviderCostSnapshot{}, false, err
	}
	scaled := ProviderCostSnapshot{
		Currency:       cost.Currency,
		PricingUnit:    cost.PricingUnit,
		FormulaVersion: cost.FormulaVersion,
	}
	for _, field := range []struct {
		base   pgtype.Numeric
		mult   *big.Rat
		target *pgtype.Numeric
	}{
		{cost.UncachedInputCost, inRat, &scaled.UncachedInputCost},
		{cost.CacheReadInputCost, inRat, &scaled.CacheReadInputCost},
		{cost.CacheWrite5mInputCost, inRat, &scaled.CacheWrite5mInputCost},
		{cost.CacheWrite1hInputCost, inRat, &scaled.CacheWrite1hInputCost},
		{cost.CacheWrite30mInputCost, inRat, &scaled.CacheWrite30mInputCost},
		{cost.OutputCost, outRat, &scaled.OutputCost},
		{cost.ReasoningOutputCost, outRat, &scaled.ReasoningOutputCost},
	} {
		v, err := scaleRate(field.base, field.mult)
		if err != nil {
			return ProviderCostSnapshot{}, false, err
		}
		*field.target = v
	}
	return scaled, true, nil
}

func longContextMultiplierRats(policy LongContextPolicy) (input, output *big.Rat, err error) {
	input, err = requiredPositiveNumeric(policy.InputMultiplier)
	if err != nil {
		return nil, nil, err
	}
	output, err = requiredPositiveNumeric(policy.OutputMultiplier)
	if err != nil {
		return nil, nil, err
	}
	return input, output, nil
}
