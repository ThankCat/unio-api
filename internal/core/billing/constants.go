package billing

const (
	// PricingUnitPer1MTokens 表示每 100 万 token 的计价单位。
	PricingUnitPer1MTokens = "per_1m_tokens"

	// FormulaVersionV1 表示当前 token 计费公式版本。
	FormulaVersionV1 = "token_v1"

	// amountDecimalScale 表示金额写入 NUMERIC(20,10) 前保留的小数位数。
	amountDecimalScale = 10

	// priceDecimalScale 表示单价（如基准价 × 线路倍率后）写入 NUMERIC(20,10) 前保留的小数位数。
	priceDecimalScale = 10
)
