package lifecycle

import (
	"testing"

	coreusage "github.com/ThankCat/unio-gateway/internal/core/usage"
)

func TestActualTotalTokensIncludesEveryInputCategoryOnce(t *testing.T) {
	facts := coreusage.Facts{
		UncachedInputTokens:      coreusage.KnownTokens(1_000),
		CacheReadInputTokens:     coreusage.KnownTokens(80_000),
		CacheWrite5mInputTokens:  coreusage.KnownTokens(200),
		CacheWrite30mInputTokens: coreusage.KnownTokens(400),
		CacheWrite1hInputTokens:  coreusage.KnownTokens(300),
		OutputTokensTotal:        coreusage.KnownTokens(500),
		ReasoningOutputTokens:    coreusage.KnownTokens(100),
	}

	got, reliable := actualTotalTokens(facts)
	if want := int64(82_400); !reliable || got != want {
		t.Fatalf("actualTotalTokens = (%d, %v), want (%d, true)", got, reliable, want)
	}
}

func TestActualTotalTokensRejectsUnknownInputCategory(t *testing.T) {
	facts := coreusage.Facts{
		UncachedInputTokens:      coreusage.KnownTokens(0),
		CacheReadInputTokens:     coreusage.KnownTokens(90_000),
		CacheWrite5mInputTokens:  coreusage.NotApplicableTokens(),
		CacheWrite30mInputTokens: coreusage.UnknownTokens(),
		CacheWrite1hInputTokens:  coreusage.NotApplicableTokens(),
		OutputTokensTotal:        coreusage.KnownTokens(42),
		ReasoningOutputTokens:    coreusage.NotApplicableTokens(),
	}
	if got, reliable := actualTotalTokens(facts); reliable || got != 0 {
		t.Fatalf("actualTotalTokens = (%d, %v), want (0, false)", got, reliable)
	}
}
