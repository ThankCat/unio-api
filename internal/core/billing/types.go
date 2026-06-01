package billing

import "github.com/jackc/pgx/v5/pgtype"

// CustomerPriceSnapshot 表示一次请求结算时冻结下来的客户侧售价副本。
type CustomerPriceSnapshot struct {
	Currency               string
	PricingUnit            string
	UncachedInputPrice     pgtype.Numeric
	CacheReadInputPrice    pgtype.Numeric
	CacheWrite5mInputPrice pgtype.Numeric
	CacheWrite1hInputPrice pgtype.Numeric
	OutputPrice            pgtype.Numeric
	ReasoningOutputPrice   pgtype.Numeric
	FormulaVersion         string
}

// ProviderCostSnapshot 表示一次请求结算时冻结下来的 provider/channel 成本价副本。
type ProviderCostSnapshot struct {
	Currency              string
	PricingUnit           string
	UncachedInputCost     pgtype.Numeric
	CacheReadInputCost    pgtype.Numeric
	CacheWrite5mInputCost pgtype.Numeric
	CacheWrite1hInputCost pgtype.Numeric
	OutputCost            pgtype.Numeric
	ReasoningOutputCost   pgtype.Numeric
	FormulaVersion        string
}

// ProviderCost 表示 billing service 计算出来的平台上游成本结果。
type ProviderCost struct {
	UncachedInputCostAmount     pgtype.Numeric
	CacheReadInputCostAmount    pgtype.Numeric
	CacheWrite5mInputCostAmount pgtype.Numeric
	CacheWrite1hInputCostAmount pgtype.Numeric
	OutputCostAmount            pgtype.Numeric
	ReasoningOutputCostAmount   pgtype.Numeric
	TotalCostAmount             pgtype.Numeric
	Currency                    string
	FormulaVersion              string
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
