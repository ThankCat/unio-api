package billing

import (
	"math/big"

	"github.com/ThankCat/unio-gateway/internal/core/usage"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
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
	uncachedInputCostAmount := ratToNumeric(amounts.UncachedInputAmount, amountDecimalScale)
	cacheReadInputCostAmount := ratToNumeric(amounts.CacheReadInputAmount, amountDecimalScale)
	cacheCreation5mInputCostAmount := ratToNumeric(amounts.CacheCreation5mInputAmount, amountDecimalScale)
	cacheCreation1hInputCostAmount := ratToNumeric(amounts.CacheCreation1hInputAmount, amountDecimalScale)
	cacheCreation30mInputCostAmount := ratToNumeric(amounts.CacheCreation30mInputAmount, amountDecimalScale)
	outputCostAmount := ratToNumeric(amounts.OutputAmount, amountDecimalScale)
	reasoningOutputCostAmount := ratToNumeric(amounts.ReasoningOutputAmount, amountDecimalScale)

	return ProviderCost{
		UncachedInputCostAmount:         uncachedInputCostAmount,
		CacheReadInputCostAmount:        cacheReadInputCostAmount,
		CacheCreation5mInputCostAmount:  cacheCreation5mInputCostAmount,
		CacheCreation1hInputCostAmount:  cacheCreation1hInputCostAmount,
		CacheCreation30mInputCostAmount: cacheCreation30mInputCostAmount,
		OutputCostAmount:                outputCostAmount,
		ReasoningOutputCostAmount:       reasoningOutputCostAmount,
		TotalCostAmount: sumRoundedNumerics(
			amountDecimalScale,
			uncachedInputCostAmount,
			cacheReadInputCostAmount,
			cacheCreation5mInputCostAmount,
			cacheCreation1hInputCostAmount,
			cacheCreation30mInputCostAmount,
			outputCostAmount,
			reasoningOutputCostAmount,
		),
		Currency:       rates.Currency,
		FormulaVersion: rates.FormulaVersion,
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
			maxRat(rates.CacheCreation5mInputRate, rates.CacheCreation1hInputRate),
			rates.CacheCreation30mInputRate,
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
	UncachedInputTokens         int64
	CacheReadInputTokens        int64
	CacheCreation5mInputTokens  int64
	CacheCreation1hInputTokens  int64
	CacheCreation30mInputTokens int64
	OutputTokensTotal           int64
	ReasoningOutputTokens       int64
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
	cacheCreation5mInput, cacheCreation5mInputOK := facts.CacheCreation5mInputTokens.BillableValue()
	cacheCreation1hInput, cacheCreation1hInputOK := facts.CacheCreation1hInputTokens.BillableValue()
	cacheCreation30mInput, cacheCreation30mInputOK := facts.CacheCreation30mInputTokens.BillableValue()
	outputTotal, outputTotalOK := facts.OutputTokensTotal.BillableValue()
	reasoningOutput, reasoningOutputOK := facts.ReasoningOutputTokens.BillableValue()
	if !uncachedInputOK || !cacheReadInputOK || !cacheCreation5mInputOK ||
		!cacheCreation1hInputOK || !cacheCreation30mInputOK || !outputTotalOK || !reasoningOutputOK {
		return billableUsage{}, failure.Wrap(
			failure.CodeBillingInvalidUsage,
			ErrInvalidUsage,
			failure.WithMessage(ErrInvalidUsage.Error()),
		)
	}

	return billableUsage{
		UncachedInputTokens:         uncachedInput,
		CacheReadInputTokens:        cacheReadInput,
		CacheCreation5mInputTokens:  cacheCreation5mInput,
		CacheCreation1hInputTokens:  cacheCreation1hInput,
		CacheCreation30mInputTokens: cacheCreation30mInput,
		OutputTokensTotal:           outputTotal,
		ReasoningOutputTokens:       reasoningOutput,
	}, nil
}

// tokenAmountBreakdown 表示协议无关 token 维度分别计算出的金额。
type tokenAmountBreakdown struct {
	UncachedInputAmount         *big.Rat
	CacheReadInputAmount        *big.Rat
	CacheCreation5mInputAmount  *big.Rat
	CacheCreation1hInputAmount  *big.Rat
	CacheCreation30mInputAmount *big.Rat
	OutputAmount                *big.Rat
	ReasoningOutputAmount       *big.Rat
	TotalAmount                 *big.Rat
}

// calculateTokenAmountBreakdown 按 token_v1 公式计算各 token 维度的金额分项。
func calculateTokenAmountBreakdown(usage billableUsage, rates tokenRates) tokenAmountBreakdown {
	normalOutput := usage.OutputTokensTotal - usage.ReasoningOutputTokens

	uncachedInputAmount := tokenAmount(rates.UncachedInputRate, usage.UncachedInputTokens)
	cacheReadInputAmount := tokenAmount(rates.CacheReadInputRate, usage.CacheReadInputTokens)
	cacheCreation5mInputAmount := tokenAmount(rates.CacheCreation5mInputRate, usage.CacheCreation5mInputTokens)
	cacheCreation1hInputAmount := tokenAmount(rates.CacheCreation1hInputRate, usage.CacheCreation1hInputTokens)
	cacheCreation30mInputAmount := tokenAmount(rates.CacheCreation30mInputRate, usage.CacheCreation30mInputTokens)
	outputAmount := tokenAmount(rates.OutputRate, normalOutput)
	reasoningOutputAmount := tokenAmount(rates.ReasoningOutputRate, usage.ReasoningOutputTokens)

	// 调用方决定只使用总额，还是连同分项一起写入成本快照。
	totalAmount := new(big.Rat)
	totalAmount.Add(totalAmount, uncachedInputAmount)
	totalAmount.Add(totalAmount, cacheReadInputAmount)
	totalAmount.Add(totalAmount, cacheCreation5mInputAmount)
	totalAmount.Add(totalAmount, cacheCreation1hInputAmount)
	totalAmount.Add(totalAmount, cacheCreation30mInputAmount)
	totalAmount.Add(totalAmount, outputAmount)
	totalAmount.Add(totalAmount, reasoningOutputAmount)

	return tokenAmountBreakdown{
		UncachedInputAmount:         uncachedInputAmount,
		CacheReadInputAmount:        cacheReadInputAmount,
		CacheCreation5mInputAmount:  cacheCreation5mInputAmount,
		CacheCreation1hInputAmount:  cacheCreation1hInputAmount,
		CacheCreation30mInputAmount: cacheCreation30mInputAmount,
		OutputAmount:                outputAmount,
		ReasoningOutputAmount:       reasoningOutputAmount,
		TotalAmount:                 totalAmount,
	}
}

// tokenAmount 计算某类 token 按 per_1m_tokens 计价后的金额。
func tokenAmount(unitPrice *big.Rat, tokens int64) *big.Rat {
	amount := tokenCost(unitPrice, tokens)
	return amount.Quo(amount, big.NewRat(1_000_000, 1))
}
