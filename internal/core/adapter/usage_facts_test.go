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

func TestChatUsageToUsageFactsSplitsCacheWriteIntoThirtyMinuteBucket(t *testing.T) {
	// GPT-5.6+：prompt=100 中 cached=20（读）、cache_write=30（写，1.25x），余 50 为纯未缓存。
	facts := ChatUsage{
		PromptTokens:     100,
		CompletionTokens: 40,
		TotalTokens:      140,
		CachedTokens:     20,
		CacheWriteTokens: 30,
		ReasoningTokens:  5,
	}.ToUsageFacts()

	// uncached 必须扣掉 cache_write，避免写入 token 被同时按 1x 与 1.25x 双重计费。
	assertKnownToken(t, "uncached_input", facts.UncachedInputTokens, 50)
	assertKnownToken(t, "cache_read_input", facts.CacheReadInputTokens, 20)
	assertKnownToken(t, "cache_write_30m", facts.CacheWrite30mInputTokens, 30)
	assertKnownToken(t, "output_total", facts.OutputTokensTotal, 40)

	// OpenAI 无 Anthropic 式 5m/1h 分档。
	if facts.CacheWrite5mInputTokens.State != usage.CountNotApplicable {
		t.Fatalf("cache_write_5m: expected not_applicable, got %q", facts.CacheWrite5mInputTokens.State)
	}
	if facts.CacheWrite1hInputTokens.State != usage.CountNotApplicable {
		t.Fatalf("cache_write_1h: expected not_applicable, got %q", facts.CacheWrite1hInputTokens.State)
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
