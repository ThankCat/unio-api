package billing

import (
	"math/big"

	"github.com/ThankCat/unio-api/internal/core/usage"
	"github.com/ThankCat/unio-api/internal/platform/failure"
)

// Service 负责根据 usage 和 token 单价快照计算客户扣费与平台成本。
type Service struct{}

// CalculateCustomerCharge 根据 usage 和客户侧售价快照计算本次请求应扣金额。
func (s Service) CalculateCustomerCharge(facts usage.Facts, price CustomerPriceSnapshot) (CustomerCharge, error) {
	billableUsage, err := normalizeUsageFacts(facts)
	if err != nil {
		return CustomerCharge{}, err
	}

	rates, err := normalizeCustomerPriceRates(price)
	if err != nil {
		return CustomerCharge{}, err
	}

	amounts := calculateTokenAmountBreakdown(billableUsage, rates)

	return CustomerCharge{
		Amount:         ratToNumeric(amounts.TotalAmount, amountDecimalScale),
		Currency:       rates.Currency,
		FormulaVersion: rates.FormulaVersion,
	}, nil
}

// CalculateProviderCost 根据 usage 和 provider/channel 成本价快照计算本次请求的平台成本分项。
func (s Service) CalculateProviderCost(facts usage.Facts, cost ProviderCostSnapshot) (ProviderCost, error) {
	billableUsage, err := normalizeUsageFacts(facts)
	if err != nil {
		return ProviderCost{}, err
	}

	rates, err := normalizeProviderCostRates(cost)
	if err != nil {
		return ProviderCost{}, err
	}

	amounts := calculateTokenAmountBreakdown(billableUsage, rates)

	return ProviderCost{
		UncachedInputCostAmount:      ratToNumeric(amounts.UncachedInputAmount, amountDecimalScale),
		CacheReadInputCostAmount:     ratToNumeric(amounts.CacheReadInputAmount, amountDecimalScale),
		CacheWrite5mInputCostAmount:  ratToNumeric(amounts.CacheWrite5mInputAmount, amountDecimalScale),
		CacheWrite1hInputCostAmount:  ratToNumeric(amounts.CacheWrite1hInputAmount, amountDecimalScale),
		CacheWrite30mInputCostAmount: ratToNumeric(amounts.CacheWrite30mInputAmount, amountDecimalScale),
		OutputCostAmount:             ratToNumeric(amounts.OutputAmount, amountDecimalScale),
		ReasoningOutputCostAmount:    ratToNumeric(amounts.ReasoningOutputAmount, amountDecimalScale),
		TotalCostAmount:              ratToNumeric(amounts.TotalAmount, amountDecimalScale),
		Currency:                     rates.Currency,
		FormulaVersion:               rates.FormulaVersion,
	}, nil
}

// EstimateAuthorizationAmount 根据预估最大 token 用量计算调用上游前需要冻结的金额。
func (s Service) EstimateAuthorizationAmount(estimate AuthorizationEstimate, price CustomerPriceSnapshot) (CustomerCharge, error) {
	if estimate.InputTokens < 0 || estimate.MaxCompletionTokens < 0 {
		return CustomerCharge{}, failure.Wrap(
			failure.CodeBillingInvalidUsage,
			ErrInvalidUsage,
			failure.WithMessage(ErrInvalidUsage.Error()),
		)
	}

	rates, err := normalizeCustomerPriceRates(price)
	if err != nil {
		return CustomerCharge{}, err
	}

	maxInputRate := maxRat(
		maxRat(rates.UncachedInputRate, rates.CacheReadInputRate),
		maxRat(
			maxRat(rates.CacheWrite5mInputRate, rates.CacheWrite1hInputRate),
			rates.CacheWrite30mInputRate,
		),
	)
	maxCompletionRate := maxRat(rates.OutputRate, rates.ReasoningOutputRate)

	amount := new(big.Rat)
	amount.Add(amount, tokenCost(maxInputRate, estimate.InputTokens))
	amount.Add(amount, tokenCost(maxCompletionRate, estimate.MaxCompletionTokens))
	amount.Quo(amount, big.NewRat(1_000_000, 1))

	return CustomerCharge{
		Amount:         ratToNumeric(amount, amountDecimalScale),
		Currency:       rates.Currency,
		FormulaVersion: rates.FormulaVersion,
	}, nil
}

// billableUsage 是当前 token_v1 公式消费的协议无关 token 数。
type billableUsage struct {
	UncachedInputTokens      int64
	CacheReadInputTokens     int64
	CacheWrite5mInputTokens  int64
	CacheWrite1hInputTokens  int64
	CacheWrite30mInputTokens int64
	OutputTokensTotal        int64
	ReasoningOutputTokens    int64
}

// normalizeUsageFacts 校验 usage facts 并把 not_applicable 安全转换成 0。
//
// unknown 不得静默按 0 计费；只要当前公式需要的任一维度 unknown，就拒绝 settlement。
func normalizeUsageFacts(facts usage.Facts) (billableUsage, error) {
	if !facts.Valid() {
		return billableUsage{}, failure.Wrap(
			failure.CodeBillingInvalidUsage,
			ErrInvalidUsage,
			failure.WithMessage(ErrInvalidUsage.Error()),
		)
	}

	uncachedInput, uncachedInputOK := facts.UncachedInputTokens.BillableValue()
	cacheReadInput, cacheReadInputOK := facts.CacheReadInputTokens.BillableValue()
	cacheWrite5mInput, cacheWrite5mInputOK := facts.CacheWrite5mInputTokens.BillableValue()
	cacheWrite1hInput, cacheWrite1hInputOK := facts.CacheWrite1hInputTokens.BillableValue()
	cacheWrite30mInput, cacheWrite30mInputOK := facts.CacheWrite30mInputTokens.BillableValue()
	outputTotal, outputTotalOK := facts.OutputTokensTotal.BillableValue()
	reasoningOutput, reasoningOutputOK := facts.ReasoningOutputTokens.BillableValue()
	if !uncachedInputOK || !cacheReadInputOK || !cacheWrite5mInputOK ||
		!cacheWrite1hInputOK || !cacheWrite30mInputOK || !outputTotalOK || !reasoningOutputOK {
		return billableUsage{}, failure.Wrap(
			failure.CodeBillingInvalidUsage,
			ErrInvalidUsage,
			failure.WithMessage(ErrInvalidUsage.Error()),
		)
	}

	return billableUsage{
		UncachedInputTokens:      uncachedInput,
		CacheReadInputTokens:     cacheReadInput,
		CacheWrite5mInputTokens:  cacheWrite5mInput,
		CacheWrite1hInputTokens:  cacheWrite1hInput,
		CacheWrite30mInputTokens: cacheWrite30mInput,
		OutputTokensTotal:        outputTotal,
		ReasoningOutputTokens:    reasoningOutput,
	}, nil
}

// tokenAmountBreakdown 表示协议无关 token 维度分别计算出的金额。
type tokenAmountBreakdown struct {
	UncachedInputAmount      *big.Rat
	CacheReadInputAmount     *big.Rat
	CacheWrite5mInputAmount  *big.Rat
	CacheWrite1hInputAmount  *big.Rat
	CacheWrite30mInputAmount *big.Rat
	OutputAmount             *big.Rat
	ReasoningOutputAmount    *big.Rat
	TotalAmount              *big.Rat
}

// calculateTokenAmountBreakdown 按 token_v1 公式计算各 token 维度的金额分项。
func calculateTokenAmountBreakdown(usage billableUsage, rates tokenRates) tokenAmountBreakdown {
	normalOutput := usage.OutputTokensTotal - usage.ReasoningOutputTokens

	uncachedInputAmount := tokenAmount(rates.UncachedInputRate, usage.UncachedInputTokens)
	cacheReadInputAmount := tokenAmount(rates.CacheReadInputRate, usage.CacheReadInputTokens)
	cacheWrite5mInputAmount := tokenAmount(rates.CacheWrite5mInputRate, usage.CacheWrite5mInputTokens)
	cacheWrite1hInputAmount := tokenAmount(rates.CacheWrite1hInputRate, usage.CacheWrite1hInputTokens)
	cacheWrite30mInputAmount := tokenAmount(rates.CacheWrite30mInputRate, usage.CacheWrite30mInputTokens)
	outputAmount := tokenAmount(rates.OutputRate, normalOutput)
	reasoningOutputAmount := tokenAmount(rates.ReasoningOutputRate, usage.ReasoningOutputTokens)

	// 调用方决定只使用总额，还是连同分项一起写入成本快照。
	totalAmount := new(big.Rat)
	totalAmount.Add(totalAmount, uncachedInputAmount)
	totalAmount.Add(totalAmount, cacheReadInputAmount)
	totalAmount.Add(totalAmount, cacheWrite5mInputAmount)
	totalAmount.Add(totalAmount, cacheWrite1hInputAmount)
	totalAmount.Add(totalAmount, cacheWrite30mInputAmount)
	totalAmount.Add(totalAmount, outputAmount)
	totalAmount.Add(totalAmount, reasoningOutputAmount)

	return tokenAmountBreakdown{
		UncachedInputAmount:      uncachedInputAmount,
		CacheReadInputAmount:     cacheReadInputAmount,
		CacheWrite5mInputAmount:  cacheWrite5mInputAmount,
		CacheWrite1hInputAmount:  cacheWrite1hInputAmount,
		CacheWrite30mInputAmount: cacheWrite30mInputAmount,
		OutputAmount:             outputAmount,
		ReasoningOutputAmount:    reasoningOutputAmount,
		TotalAmount:              totalAmount,
	}
}

// tokenAmount 计算某类 token 按 per_1m_tokens 计价后的金额。
func tokenAmount(unitPrice *big.Rat, tokens int64) *big.Rat {
	amount := tokenCost(unitPrice, tokens)
	return amount.Quo(amount, big.NewRat(1_000_000, 1))
}
