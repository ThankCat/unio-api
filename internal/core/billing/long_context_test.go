package billing

import (
	"math/big"
	"testing"

	coreusage "github.com/ThankCat/unio-api/internal/core/usage"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestShouldApplyLongContext(t *testing.T) {
	policy := LongContextPolicy{
		Enabled:          true,
		Threshold:        272_000,
		InputMultiplier:  numeric(2, 0),
		OutputMultiplier: numeric(15, -1), // 1.5
	}

	if ShouldApplyLongContext(policy, 272_000) {
		t.Fatal("threshold is exclusive: equal should not apply")
	}
	if !ShouldApplyLongContext(policy, 272_001) {
		t.Fatal("expected apply when input sum exceeds threshold")
	}
	if ShouldApplyLongContext(LongContextPolicy{}, 1_000_000) {
		t.Fatal("disabled policy must not apply")
	}
}

func TestLongContextInputTokenSum(t *testing.T) {
	sum := LongContextInputTokenSum(coreusage.Facts{
		UncachedInputTokens:      coreusage.KnownTokens(200_000),
		CacheReadInputTokens:     coreusage.KnownTokens(70_000),
		CacheWrite5mInputTokens:  coreusage.KnownTokens(2_000),
		CacheWrite1hInputTokens:  coreusage.NotApplicableTokens(),
		CacheWrite30mInputTokens: coreusage.KnownTokens(1_000),
	})
	if sum != 273_000 {
		t.Fatalf("sum = %d, want 273000", sum)
	}
}

func TestApplyLongContextToCustomerPriceScalesWhenTriggered(t *testing.T) {
	base := defaultCustomerPriceSnapshot()
	policy := LongContextPolicy{
		Enabled:          true,
		Threshold:        100,
		InputMultiplier:  numeric(2, 0),
		OutputMultiplier: numeric(15, -1),
	}

	scaled, applied, err := ApplyLongContextToCustomerPrice(base, policy, 101)
	if err != nil {
		t.Fatalf("ApplyLongContextToCustomerPrice: %v", err)
	}
	if !applied {
		t.Fatal("expected applied=true")
	}
	assertNumericRat(t, scaled.UncachedInputPrice, "4")    // 2 × 2
	assertNumericRat(t, scaled.CacheReadInputPrice, "1")   // 0.5 × 2
	assertNumericRat(t, scaled.OutputPrice, "12")          // 8 × 1.5
	assertNumericRat(t, scaled.ReasoningOutputPrice, "18") // 12 × 1.5
}

func TestApplyLongContextToCustomerPriceNoopBelowThreshold(t *testing.T) {
	base := defaultCustomerPriceSnapshot()
	policy := LongContextPolicy{
		Enabled:          true,
		Threshold:        100,
		InputMultiplier:  numeric(2, 0),
		OutputMultiplier: numeric(15, -1),
	}

	scaled, applied, err := ApplyLongContextToCustomerPrice(base, policy, 100)
	if err != nil {
		t.Fatalf("ApplyLongContextToCustomerPrice: %v", err)
	}
	if applied {
		t.Fatal("expected applied=false below/at threshold")
	}
	assertNumericRat(t, scaled.UncachedInputPrice, "2")
	assertNumericRat(t, scaled.OutputPrice, "8")
}

func TestApplyLongContextToProviderCostScalesWhenTriggered(t *testing.T) {
	base := defaultProviderCostSnapshot()
	policy := LongContextPolicy{
		Enabled:          true,
		Threshold:        50,
		InputMultiplier:  numeric(2, 0),
		OutputMultiplier: numeric(3, 0),
	}

	scaled, applied, err := ApplyLongContextToProviderCost(base, policy, 51)
	if err != nil {
		t.Fatalf("ApplyLongContextToProviderCost: %v", err)
	}
	if !applied {
		t.Fatal("expected applied=true")
	}
	assertNumericRat(t, scaled.UncachedInputCost, "2") // 1 × 2
	assertNumericRat(t, scaled.OutputCost, "12")       // 4 × 3
}

func assertNumericRat(t *testing.T, got pgtype.Numeric, want string) {
	t.Helper()
	rat, err := numericToRat(got)
	if err != nil {
		t.Fatalf("numericToRat: %v", err)
	}
	wantRat, ok := new(big.Rat).SetString(want)
	if !ok {
		t.Fatalf("invalid want %q", want)
	}
	if rat.Cmp(wantRat) != 0 {
		t.Fatalf("got %s, want %s", rat.FloatString(10), want)
	}
}
