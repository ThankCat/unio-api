package lifecycle

import (
	"testing"

	coreusage "github.com/ThankCat/unio-gateway/internal/core/usage"
)

func TestBillableTPMTokensExcludesCacheRead(t *testing.T) {
	facts := coreusage.Facts{
		UncachedInputTokens:     coreusage.KnownTokens(1_000),
		CacheReadInputTokens:    coreusage.KnownTokens(80_000),
		CacheWrite5mInputTokens: coreusage.KnownTokens(200),
		CacheWrite1hInputTokens: coreusage.KnownTokens(300),
		OutputTokensTotal:       coreusage.KnownTokens(500),
		ReasoningOutputTokens:   coreusage.KnownTokens(100),
	}

	if got, want := billableTPMTokens(facts), int64(2_000); got != want {
		t.Fatalf("billableTPMTokens = %d, want %d", got, want)
	}
}

func TestBillableTPMTokensAllCacheReadCountsOnlyOutput(t *testing.T) {
	facts := coreusage.Facts{
		UncachedInputTokens:     coreusage.KnownTokens(0),
		CacheReadInputTokens:    coreusage.KnownTokens(90_000),
		CacheWrite5mInputTokens: coreusage.NotApplicableTokens(),
		CacheWrite1hInputTokens: coreusage.NotApplicableTokens(),
		OutputTokensTotal:       coreusage.KnownTokens(42),
	}
	if got := billableTPMTokens(facts); got != 42 {
		t.Fatalf("billableTPMTokens = %d, want 42", got)
	}
}
