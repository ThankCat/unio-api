package adapter

import (
	"testing"

	"github.com/ThankCat/unio-api/internal/core/usage"
)

func TestChatUsageToUsageFactsSplitsCacheAndMarksCacheWriteNotApplicable(t *testing.T) {
	facts := ChatUsage{
		PromptTokens:     100,
		CompletionTokens: 50,
		TotalTokens:      150,
		CachedTokens:     20,
		ReasoningTokens:  10,
	}.ToUsageFacts()

	assertKnownToken(t, "uncached_input", facts.UncachedInputTokens, 80)
	assertKnownToken(t, "cache_read_input", facts.CacheReadInputTokens, 20)
	assertKnownToken(t, "output_total", facts.OutputTokensTotal, 50)
	assertKnownToken(t, "reasoning_output", facts.ReasoningOutputTokens, 10)

	if facts.CacheWrite5mInputTokens.State != usage.CountNotApplicable {
		t.Fatalf("cache_write_5m: expected not_applicable, got %q", facts.CacheWrite5mInputTokens.State)
	}
	if facts.CacheWrite1hInputTokens.State != usage.CountNotApplicable {
		t.Fatalf("cache_write_1h: expected not_applicable, got %q", facts.CacheWrite1hInputTokens.State)
	}
	if len(facts.ServerToolUsage) != 0 {
		t.Fatalf("expected no server tool usage, got %d", len(facts.ServerToolUsage))
	}
}

func TestChatUsageToUsageFactsHandlesNoCacheNoReasoning(t *testing.T) {
	facts := ChatUsage{
		PromptTokens:     10,
		CompletionTokens: 5,
		TotalTokens:      15,
		CachedTokens:     0,
		ReasoningTokens:  0,
	}.ToUsageFacts()

	assertKnownToken(t, "uncached_input", facts.UncachedInputTokens, 10)
	assertKnownToken(t, "cache_read_input", facts.CacheReadInputTokens, 0)
	assertKnownToken(t, "output_total", facts.OutputTokensTotal, 5)
	assertKnownToken(t, "reasoning_output", facts.ReasoningOutputTokens, 0)
}

func assertKnownToken(t *testing.T, name string, got usage.TokenCount, want int64) {
	t.Helper()

	if got.State != usage.CountKnown {
		t.Fatalf("%s: expected state known, got %q", name, got.State)
	}
	if got.Value != want {
		t.Fatalf("%s: expected value %d, got %d", name, want, got.Value)
	}
}
