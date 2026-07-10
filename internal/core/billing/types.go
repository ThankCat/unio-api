package billing

import "github.com/jackc/pgx/v5/pgtype"

// CustomerPriceSnapshot 表示一次请求结算时冻结下来的客户侧售价副本。
type CustomerPriceSnapshot struct {
	Currency                string
	PricingUnit             string
	UncachedInputPrice      pgtype.Numeric
	CacheReadInputPrice     pgtype.Numeric
	CacheWrite5mInputPrice  pgtype.Numeric
	CacheWrite1hInputPrice  pgtype.Numeric
	CacheWrite30mInputPrice pgtype.Numeric
	OutputPrice             pgtype.Numeric
	ReasoningOutputPrice    pgtype.Numeric
	FormulaVersion          string
}

// IsEffectivelyFree 判断客户侧售价快照是否「全部非正」，即该渠道对客户实际免费（P2-4 零价渠道误配检测）。
//
// 任一价格分量为正即返回 false；无效/缺失/零/负值都视为非正（缺失的 cache/reasoning 单价会回退到
// uncached/output，因此只要 uncached 与 output 非正，整体即为免费）。仅用于观测告警，不参与计费。
func (p CustomerPriceSnapshot) IsEffectivelyFree() bool {
	for _, rate := range []pgtype.Numeric{
		p.UncachedInputPrice,
		p.CacheReadInputPrice,
		p.CacheWrite5mInputPrice,
		p.CacheWrite1hInputPrice,
		p.CacheWrite30mInputPrice,
		p.OutputPrice,
		p.ReasoningOutputPrice,
	} {
		if rat, err := numericToRat(rate); err == nil && rat.Sign() > 0 {
			return false
		}
	}
	return true
}

// ProviderCostSnapshot 表示一次请求结算时冻结下来的 provider/channel 成本价副本。
type ProviderCostSnapshot struct {
	Currency               string
	PricingUnit            string
	UncachedInputCost      pgtype.Numeric
	CacheReadInputCost     pgtype.Numeric
	CacheWrite5mInputCost  pgtype.Numeric
	CacheWrite1hInputCost  pgtype.Numeric
	CacheWrite30mInputCost pgtype.Numeric
	OutputCost             pgtype.Numeric
	ReasoningOutputCost    pgtype.Numeric
	FormulaVersion         string
}

// ProviderCost 表示 billing service 计算出来的平台上游成本结果。
type ProviderCost struct {
	UncachedInputCostAmount      pgtype.Numeric
	CacheReadInputCostAmount     pgtype.Numeric
	CacheWrite5mInputCostAmount  pgtype.Numeric
	CacheWrite1hInputCostAmount  pgtype.Numeric
	CacheWrite30mInputCostAmount pgtype.Numeric
	OutputCostAmount             pgtype.Numeric
	ReasoningOutputCostAmount    pgtype.Numeric
	TotalCostAmount              pgtype.Numeric
	Currency                     string
	FormulaVersion               string
}

// CustomerCharge 表示 billing service 计算出来的客户侧扣费结果。
type CustomerCharge struct {
	Amount         pgtype.Numeric
	Currency       string
	FormulaVersion string
}

// AuthorizationEstimate 表示调用上游前用于冻结金额计算的保守 token 估算。
type AuthorizationEstimate struct {
	InputTokens         int64
	MaxCompletionTokens int64
}
