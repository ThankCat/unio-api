package billing

import (
	"errors"
	"math/big"
	"testing"

	coreusage "github.com/ThankCat/unio-gateway/internal/core/usage"
	"github.com/jackc/pgx/v5/pgtype"
)

// numeric 创建测试用 pgtype.Numeric，value 表示 Int，exp 表示 10 的指数。
func numeric(value int64, exp int32) pgtype.Numeric {
	return pgtype.Numeric{Int: big.NewInt(value), Exp: exp, Valid: true}
}

// nullNumeric 创建测试用 SQL NULL NUMERIC。
func nullNumeric() pgtype.Numeric {
	return pgtype.Numeric{Valid: false}
}

type testUsage struct {
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	CachedTokens     int64
	ReasoningTokens  int64
}

// testUsageFacts 把旧 OpenAI 形状的测试输入映射成协议无关 facts，便于验证金额兼容性。
func testUsageFacts(u testUsage) coreusage.Facts {
	return coreusage.Facts{
		UncachedInputTokens:      coreusage.KnownTokens(u.PromptTokens - u.CachedTokens),
		CacheReadInputTokens:     coreusage.KnownTokens(u.CachedTokens),
		CacheWrite5mInputTokens:  coreusage.NotApplicableTokens(),
		CacheWrite1hInputTokens:  coreusage.NotApplicableTokens(),
		CacheWrite30mInputTokens: coreusage.NotApplicableTokens(),
		OutputTokensTotal:        coreusage.KnownTokens(u.CompletionTokens),
		ReasoningOutputTokens:    coreusage.KnownTokens(u.ReasoningTokens),
	}
}

// defaultCustomerPriceSnapshot 返回一份支持 token_v1 的基础客户售价快照。
func defaultCustomerPriceSnapshot() CustomerPriceSnapshot {
	return CustomerPriceSnapshot{
		Currency:             "USD",
		PricingUnit:          PricingUnitPer1MTokens,
		UncachedInputPrice:   numeric(2_0000000000, -10),
		OutputPrice:          numeric(8_0000000000, -10),
		CacheReadInputPrice:  numeric(5000000000, -10),
		ReasoningOutputPrice: numeric(12_0000000000, -10),
		FormulaVersion:       FormulaVersionV1,
	}
}

func TestCustomerPriceSnapshotIsEffectivelyFree(t *testing.T) {
	if defaultCustomerPriceSnapshot().IsEffectivelyFree() {
		t.Fatal("expected priced snapshot to not be effectively free")
	}

	allZero := CustomerPriceSnapshot{
		Currency:           "USD",
		PricingUnit:        PricingUnitPer1MTokens,
		UncachedInputPrice: numeric(0, 0),
		OutputPrice:        numeric(0, 0),
		FormulaVersion:     FormulaVersionV1,
	}
	if !allZero.IsEffectivelyFree() {
		t.Fatal("expected all-zero snapshot to be effectively free")
	}

	allInvalid := CustomerPriceSnapshot{
		Currency:           "USD",
		PricingUnit:        PricingUnitPer1MTokens,
		UncachedInputPrice: nullNumeric(),
		OutputPrice:        nullNumeric(),
		FormulaVersion:     FormulaVersionV1,
	}
	if !allInvalid.IsEffectivelyFree() {
		t.Fatal("expected missing-rate snapshot to be treated as effectively free")
	}

	onlyOutputPriced := CustomerPriceSnapshot{
		Currency:           "USD",
		PricingUnit:        PricingUnitPer1MTokens,
		UncachedInputPrice: numeric(0, 0),
		OutputPrice:        numeric(8_0000000000, -10),
		FormulaVersion:     FormulaVersionV1,
	}
	if onlyOutputPriced.IsEffectivelyFree() {
		t.Fatal("expected a positive output price to make snapshot not free")
	}
}

// defaultProviderCostSnapshot 返回一份支持 token_v1 的基础 provider/channel 成本价快照。
func defaultProviderCostSnapshot() ProviderCostSnapshot {
	return ProviderCostSnapshot{
		Currency:            "USD",
		PricingUnit:         PricingUnitPer1MTokens,
		UncachedInputCost:   numeric(1_0000000000, -10),
		OutputCost:          numeric(4_0000000000, -10),
		CacheReadInputCost:  numeric(2_000000000, -10),
		ReasoningOutputCost: numeric(6_0000000000, -10),
		FormulaVersion:      FormulaVersionV1,
	}
}

// assertNumeric 校验 NUMERIC 的内部整数和小数指数，避免 float64 精度误差。
func assertNumeric(t *testing.T, got pgtype.Numeric, wantInt int64, wantExp int32) {
	t.Helper()

	if !got.Valid {
		t.Fatal("expected valid numeric")
	}
	if got.Exp != wantExp {
		t.Fatalf("expected numeric exponent %d, got %d", wantExp, got.Exp)
	}
	if got.Int == nil {
		t.Fatal("expected numeric int")
	}
	if got.Int.Cmp(big.NewInt(wantInt)) != 0 {
		t.Fatalf("expected numeric int %d, got %s", wantInt, got.Int.String())
	}
}

// TestCalculateCustomerChargeChargesTokenClassesSeparately 验证客户扣费按普通、缓存和 reasoning token 分别计费。
func TestCalculateCustomerChargeChargesTokenClassesSeparately(t *testing.T) {
	settlement, err := (Service{}).CalculateCustomerCharge(testUsageFacts(testUsage{
		PromptTokens:     1000,
		CompletionTokens: 500,
		TotalTokens:      1500,
		CachedTokens:     200,
		ReasoningTokens:  100,
	}), defaultCustomerPriceSnapshot())
	if err != nil {
		t.Fatalf("calculate customer charge: %v", err)
	}

	if settlement.Currency != "USD" {
		t.Fatalf("expected USD currency, got %q", settlement.Currency)
	}
	if settlement.FormulaVersion != FormulaVersionV1 {
		t.Fatalf("expected formula version %q, got %q", FormulaVersionV1, settlement.FormulaVersion)
	}

	// (800*2 + 200*0.5 + 400*8 + 100*12) / 1_000_000 = 0.0061000000。
	assertNumeric(t, settlement.Amount, 61_000000, -10)
}

// TestCalculateCustomerChargeChargesCacheWrite30m 验证 OpenAI GPT-5.6 的 30m 缓存写维度按独立单价计费，
// 与 5m/1h（此处 not_applicable）互不影响。
func TestCalculateCustomerChargeChargesCacheWrite30m(t *testing.T) {
	price := defaultCustomerPriceSnapshot()
	// 30m 缓存写单价 = 未缓存输入价 1.25x = 2.5 / 1M。
	price.CacheWrite30mInputPrice = numeric(2_5000000000, -10)

	facts := coreusage.Facts{
		UncachedInputTokens:      coreusage.KnownTokens(500),
		CacheReadInputTokens:     coreusage.KnownTokens(200),
		CacheWrite5mInputTokens:  coreusage.NotApplicableTokens(),
		CacheWrite1hInputTokens:  coreusage.NotApplicableTokens(),
		CacheWrite30mInputTokens: coreusage.KnownTokens(300),
		OutputTokensTotal:        coreusage.KnownTokens(400),
		ReasoningOutputTokens:    coreusage.KnownTokens(0),
	}

	settlement, err := (Service{}).CalculateCustomerCharge(facts, price)
	if err != nil {
		t.Fatalf("calculate customer charge: %v", err)
	}

	// (500*2 + 200*0.5 + 300*2.5 + 400*8) / 1_000_000 = 5050 / 1e6 = 0.0050500000。
	assertNumeric(t, settlement.Amount, 50_500000, -10)
}

// TestCalculateCustomerChargeFallsBackToBasePricesForNullSpecialPrices 验证客户扣费在特殊价格未配置时回退到普通输入/输出价格。
func TestCalculateCustomerChargeFallsBackToBasePricesForNullSpecialPrices(t *testing.T) {
	price := defaultCustomerPriceSnapshot()
	price.CacheReadInputPrice = nullNumeric()
	price.ReasoningOutputPrice = nullNumeric()

	settlement, err := (Service{}).CalculateCustomerCharge(testUsageFacts(testUsage{
		PromptTokens:     1000,
		CompletionTokens: 500,
		TotalTokens:      1500,
		CachedTokens:     200,
		ReasoningTokens:  100,
	}), price)
	if err != nil {
		t.Fatalf("calculate customer charge: %v", err)
	}

	// (800*2 + 200*2 + 400*8 + 100*8) / 1_000_000 = 0.0060000000。
	assertNumeric(t, settlement.Amount, 60_000000, -10)
}

// TestCalculateCustomerChargeDefaultsEmptyFormulaVersion 验证旧数据未显式写公式版本时默认按 token_v1 计算。
func TestCalculateCustomerChargeDefaultsEmptyFormulaVersion(t *testing.T) {
	price := defaultCustomerPriceSnapshot()
	price.FormulaVersion = ""

	settlement, err := (Service{}).CalculateCustomerCharge(testUsageFacts(testUsage{
		PromptTokens:     100,
		CompletionTokens: 50,
		TotalTokens:      150,
	}), price)
	if err != nil {
		t.Fatalf("calculate customer charge: %v", err)
	}

	if settlement.FormulaVersion != FormulaVersionV1 {
		t.Fatalf("expected default formula version %q, got %q", FormulaVersionV1, settlement.FormulaVersion)
	}
}

// TestCalculateCustomerChargeRoundsToAmountScale 验证客户扣费金额会四舍五入到 NUMERIC(20,10) 对应的小数位。
func TestCalculateCustomerChargeRoundsToAmountScale(t *testing.T) {
	price := defaultCustomerPriceSnapshot()
	price.UncachedInputPrice = numeric(5, -5)
	price.OutputPrice = numeric(0, 0)
	price.CacheReadInputPrice = nullNumeric()
	price.ReasoningOutputPrice = nullNumeric()

	settlement, err := (Service{}).CalculateCustomerCharge(testUsageFacts(testUsage{
		PromptTokens: 1,
		TotalTokens:  1,
	}), price)
	if err != nil {
		t.Fatalf("calculate customer charge: %v", err)
	}

	// 0.00005 / 1_000_000 = 0.00000000005，保留 10 位后四舍五入为 0.0000000001。
	assertNumeric(t, settlement.Amount, 1, -10)
}

// TestCalculateProviderCostReturnsCostBreakdown 验证平台成本会保存四类 token 的成本分项和总成本。
func TestCalculateProviderCostReturnsCostBreakdown(t *testing.T) {
	cost, err := (Service{}).CalculateProviderCost(testUsageFacts(testUsage{
		PromptTokens:     1000,
		CompletionTokens: 500,
		TotalTokens:      1500,
		CachedTokens:     200,
		ReasoningTokens:  100,
	}), defaultProviderCostSnapshot())
	if err != nil {
		t.Fatalf("calculate provider cost: %v", err)
	}

	if cost.Currency != "USD" {
		t.Fatalf("expected USD currency, got %q", cost.Currency)
	}
	if cost.FormulaVersion != FormulaVersionV1 {
		t.Fatalf("expected formula version %q, got %q", FormulaVersionV1, cost.FormulaVersion)
	}

	// input=(800*1)/1M, cached=(200*0.2)/1M, output=(400*4)/1M, reasoning=(100*6)/1M。
	assertNumeric(t, cost.UncachedInputCostAmount, 8_000000, -10)
	assertNumeric(t, cost.CacheReadInputCostAmount, 400000, -10)
	assertNumeric(t, cost.OutputCostAmount, 16_000000, -10)
	assertNumeric(t, cost.ReasoningOutputCostAmount, 6_000000, -10)
	assertNumeric(t, cost.TotalCostAmount, 30_400000, -10)
}

// TestCalculateProviderCostFallsBackToBaseCostsForNullSpecialCosts 验证特殊成本未配置时回退到普通输入/输出成本。
func TestCalculateProviderCostFallsBackToBaseCostsForNullSpecialCosts(t *testing.T) {
	costSnapshot := defaultProviderCostSnapshot()
	costSnapshot.CacheReadInputCost = nullNumeric()
	costSnapshot.ReasoningOutputCost = nullNumeric()

	cost, err := (Service{}).CalculateProviderCost(testUsageFacts(testUsage{
		PromptTokens:     1000,
		CompletionTokens: 500,
		TotalTokens:      1500,
		CachedTokens:     200,
		ReasoningTokens:  100,
	}), costSnapshot)
	if err != nil {
		t.Fatalf("calculate provider cost: %v", err)
	}

	// cached 回退 input=1，reasoning 回退 output=4，总成本为 0.0030000000。
	assertNumeric(t, cost.UncachedInputCostAmount, 8_000000, -10)
	assertNumeric(t, cost.CacheReadInputCostAmount, 2_000000, -10)
	assertNumeric(t, cost.OutputCostAmount, 16_000000, -10)
	assertNumeric(t, cost.ReasoningOutputCostAmount, 4_000000, -10)
	assertNumeric(t, cost.TotalCostAmount, 30_000000, -10)
}

// TestEstimateAuthorizationAmountUsesHigherCompletionPrice 验证冻结金额按 output/reasoning 中更贵的 completion 价格计算。
func TestEstimateAuthorizationAmountUsesHigherCompletionPrice(t *testing.T) {
	settlement, err := (Service{}).EstimateAuthorizationAmount(AuthorizationEstimate{
		InputTokens:         1000,
		MaxCompletionTokens: 500,
	}, defaultCustomerPriceSnapshot())
	if err != nil {
		t.Fatalf("estimate reservation amount: %v", err)
	}

	if settlement.Currency != "USD" {
		t.Fatalf("expected USD currency, got %q", settlement.Currency)
	}
	if settlement.FormulaVersion != FormulaVersionV1 {
		t.Fatalf("expected formula version %q, got %q", FormulaVersionV1, settlement.FormulaVersion)
	}

	// (1000*2 + 500*12) / 1_000_000 = 0.0080000000。
	assertNumeric(t, settlement.Amount, 80_000000, -10)
}

// TestEstimateAuthorizationAmountFallsBackToOutputPrice 验证 reasoning 价格未配置时按普通 output 价格估算。
func TestEstimateAuthorizationAmountFallsBackToOutputPrice(t *testing.T) {
	price := defaultCustomerPriceSnapshot()
	price.ReasoningOutputPrice = nullNumeric()

	settlement, err := (Service{}).EstimateAuthorizationAmount(AuthorizationEstimate{
		InputTokens:         1000,
		MaxCompletionTokens: 500,
	}, price)
	if err != nil {
		t.Fatalf("estimate reservation amount: %v", err)
	}

	// (1000*2 + 500*8) / 1_000_000 = 0.0060000000。
	assertNumeric(t, settlement.Amount, 60_000000, -10)
}

// TestEstimateAuthorizationAmountRejectsInvalidEstimate 验证冻结金额估算不接受负数 token。
func TestEstimateAuthorizationAmountRejectsInvalidEstimate(t *testing.T) {
	tests := []struct {
		name     string
		estimate AuthorizationEstimate
	}{
		{
			name:     "negative prompt tokens",
			estimate: AuthorizationEstimate{InputTokens: -1},
		},
		{
			name:     "negative max completion tokens",
			estimate: AuthorizationEstimate{MaxCompletionTokens: -1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := (Service{}).EstimateAuthorizationAmount(tt.estimate, defaultCustomerPriceSnapshot())
			if !errors.Is(err, ErrInvalidUsage) {
				t.Fatalf("expected ErrInvalidUsage, got %v", err)
			}
		})
	}
}

// TestCalculateCustomerChargeRejectsInvalidUsage 验证 usage token 约束和数据库 usage_records 约束保持一致。
func TestCalculateCustomerChargeRejectsInvalidUsage(t *testing.T) {
	tests := []struct {
		name  string
		usage coreusage.Facts
	}{
		{
			name:  "negative prompt tokens",
			usage: testUsageFacts(testUsage{PromptTokens: -1}),
		},
		{
			name: "unknown output tokens",
			usage: func() coreusage.Facts {
				facts := testUsageFacts(testUsage{PromptTokens: 10, CompletionTokens: 5})
				facts.OutputTokensTotal = coreusage.UnknownTokens()
				return facts
			}(),
		},
		{
			name:  "cached tokens exceed prompt tokens",
			usage: testUsageFacts(testUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15, CachedTokens: 11}),
		},
		{
			name:  "reasoning tokens exceed completion tokens",
			usage: testUsageFacts(testUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15, ReasoningTokens: 6}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := (Service{}).CalculateCustomerCharge(tt.usage, defaultCustomerPriceSnapshot())
			if !errors.Is(err, ErrInvalidUsage) {
				t.Fatalf("expected ErrInvalidUsage, got %v", err)
			}
		})
	}
}

// TestCalculateCustomerChargeRejectsInvalidRate 验证客户售价快照缺少必填单价或含负数时会失败。
func TestCalculateCustomerChargeRejectsInvalidRate(t *testing.T) {
	validUsage := testUsageFacts(testUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15})

	tests := []struct {
		name  string
		price CustomerPriceSnapshot
	}{
		{
			name: "empty currency",
			price: func() CustomerPriceSnapshot {
				price := defaultCustomerPriceSnapshot()
				price.Currency = ""
				return price
			}(),
		},
		{
			name: "missing input price",
			price: func() CustomerPriceSnapshot {
				price := defaultCustomerPriceSnapshot()
				price.UncachedInputPrice = nullNumeric()
				return price
			}(),
		},
		{
			name: "negative output price",
			price: func() CustomerPriceSnapshot {
				price := defaultCustomerPriceSnapshot()
				price.OutputPrice = numeric(-1, 0)
				return price
			}(),
		},
		{
			name: "negative cached input price",
			price: func() CustomerPriceSnapshot {
				price := defaultCustomerPriceSnapshot()
				price.CacheReadInputPrice = numeric(-1, 0)
				return price
			}(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := (Service{}).CalculateCustomerCharge(validUsage, tt.price)
			if !errors.Is(err, ErrInvalidRate) {
				t.Fatalf("expected ErrInvalidRate, got %v", err)
			}
		})
	}
}

// TestCalculateCustomerChargeRejectsUnsupportedPricingUnit 验证不支持的计价单位会返回专门错误。
func TestCalculateCustomerChargeRejectsUnsupportedPricingUnit(t *testing.T) {
	price := defaultCustomerPriceSnapshot()
	price.PricingUnit = "per_token"

	_, err := (Service{}).CalculateCustomerCharge(testUsageFacts(testUsage{PromptTokens: 10, TotalTokens: 10}), price)
	if !errors.Is(err, ErrUnsupportedPricingUnit) {
		t.Fatalf("expected ErrUnsupportedPricingUnit, got %v", err)
	}
}

// TestCalculateCustomerChargeRejectsUnsupportedFormula 验证不支持的公式版本不会被错误地按 token_v1 计算。
func TestCalculateCustomerChargeRejectsUnsupportedFormula(t *testing.T) {
	price := defaultCustomerPriceSnapshot()
	price.FormulaVersion = "token_v2"

	_, err := (Service{}).CalculateCustomerCharge(testUsageFacts(testUsage{PromptTokens: 10, TotalTokens: 10}), price)
	if !errors.Is(err, ErrUnsupportedFormula) {
		t.Fatalf("expected ErrUnsupportedFormula, got %v", err)
	}
}
