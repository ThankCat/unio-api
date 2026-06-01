package anthropic

import (
	"testing"

	"github.com/ThankCat/unio-api/internal/core/usage"
)

func intptr(v int) *int { return &v }

func TestMessageUsageToUsageFactsDeepSeekShape(t *testing.T) {
	// 模拟 DeepSeek Anthropic endpoint 的固定 usage 形状：五字段，无 TTL 拆分、无 thinking 分解。
	u := MessageUsage{
		InputTokens:              24,
		CacheCreationInputTokens: intptr(0),
		CacheReadInputTokens:     intptr(256),
		OutputTokens:             44,
		ServiceTier:              strptrLocal("standard"),
	}

	facts := u.ToUsageFacts()

	if got, ok := facts.UncachedInputTokens.BillableValue(); !ok || got != 24 {
		t.Fatalf("uncached = %d ok=%v", got, ok)
	}
	if got, ok := facts.CacheReadInputTokens.BillableValue(); !ok || got != 256 {
		t.Fatalf("cache_read = %d ok=%v", got, ok)
	}
	// flat cache_creation 总量归入默认 5m 档。
	if got, ok := facts.CacheWrite5mInputTokens.BillableValue(); !ok || got != 0 {
		t.Fatalf("cache_write_5m = %d ok=%v", got, ok)
	}
	if facts.CacheWrite1hInputTokens.State != usage.CountNotApplicable {
		t.Fatalf("cache_write_1h state = %s, want not_applicable", facts.CacheWrite1hInputTokens.State)
	}
	if got, ok := facts.OutputTokensTotal.BillableValue(); !ok || got != 44 {
		t.Fatalf("output = %d ok=%v", got, ok)
	}
	// DeepSeek 不返回 thinking 分解 → not_applicable。
	if facts.ReasoningOutputTokens.State != usage.CountNotApplicable {
		t.Fatalf("reasoning state = %s, want not_applicable", facts.ReasoningOutputTokens.State)
	}
	if len(facts.ServerToolUsage) != 0 {
		t.Fatalf("server tool usage = %#v, want empty", facts.ServerToolUsage)
	}
}

func TestMessageUsageToUsageFactsTTLBreakdownAndServerTools(t *testing.T) {
	u := MessageUsage{
		InputTokens: 10,
		CacheCreation: &CacheCreationUsage{
			Ephemeral5mInputTokens: intptr(5),
			Ephemeral1hInputTokens: intptr(3),
		},
		OutputTokens:         7,
		ThinkingOutputTokens: intptr(2),
		ServerToolUse: &ServerToolUsage{
			WebSearchRequests: intptr(1),
		},
	}

	facts := u.ToUsageFacts()

	if got, ok := facts.CacheWrite5mInputTokens.BillableValue(); !ok || got != 5 {
		t.Fatalf("cache_write_5m = %d ok=%v", got, ok)
	}
	if got, ok := facts.CacheWrite1hInputTokens.BillableValue(); !ok || got != 3 {
		t.Fatalf("cache_write_1h = %d ok=%v", got, ok)
	}
	if got, ok := facts.ReasoningOutputTokens.BillableValue(); !ok || got != 2 {
		t.Fatalf("reasoning = %d ok=%v", got, ok)
	}
	if len(facts.ServerToolUsage) != 1 || facts.ServerToolUsage[0].Kind != usage.MeteredServerWebSearchRequest {
		t.Fatalf("server tool usage = %#v", facts.ServerToolUsage)
	}
}

func TestMessageUsageMissingCacheReadIsNotApplicable(t *testing.T) {
	u := MessageUsage{InputTokens: 3, OutputTokens: 2}
	facts := u.ToUsageFacts()
	if facts.CacheReadInputTokens.State != usage.CountNotApplicable {
		t.Fatalf("cache_read state = %s, want not_applicable", facts.CacheReadInputTokens.State)
	}
}

func strptrLocal(s string) *string { return &s }
