package messages

import (
	"testing"

	"github.com/ThankCat/unio-api/internal/core/usage"
)

func intptr(v int) *int { return &v }

// TestMergeUsageWireStreamCacheCreationFromDelta 锁定 sub2api/上游流式形状：
// message_start 只带全 0 的 cache_creation TTL 占位对象，真实 cache write 总量在 message_delta
// 的 flat cache_creation_input_tokens 上给出。合并后必须以 flat 权威值入账（8798），
// 而不能被 message_start 的 0 拆分吞没（否则缓存写入成本被严重少计）。
func TestMergeUsageWireStreamCacheCreationFromDelta(t *testing.T) {
	var state usageWire

	// message_start：input 计量 + 全 0 的 cache_creation 占位对象 + flat 0。
	mergeUsageWire(&state, usageWire{
		InputTokens:              intptr(7105),
		CacheCreationInputTokens: intptr(0),
		CacheReadInputTokens:     intptr(0),
		CacheCreation:            &cacheCreationWire{Ephemeral5mInputTokens: intptr(0), Ephemeral1hInputTokens: intptr(0)},
		OutputTokens:             intptr(1),
	})

	// message_delta：最终 output + 真实 flat cache write 总量，无 TTL 拆分对象。
	mergeUsageWire(&state, usageWire{
		InputTokens:              intptr(87),
		CacheCreationInputTokens: intptr(8798),
		CacheReadInputTokens:     intptr(0),
		OutputTokens:             intptr(9),
	})

	facts := messageUsageFromWire(state).ToUsageFacts()
	if got, ok := facts.CacheWrite5mInputTokens.BillableValue(); !ok || got != 8798 {
		t.Fatalf("cache_write_5m = %d ok=%v, want 8798 (flat total must win over stale zero TTL split)", got, ok)
	}
}

// TestMergeUsageWireKeepsConsistentTTLSplit 验证不误伤：当 flat 总量与 TTL 拆分汇总一致时
// （真实 Anthropic 直连 / DeepSeek 同事件形状），保留 TTL 拆分，不清空。
func TestMergeUsageWireKeepsConsistentTTLSplit(t *testing.T) {
	var state usageWire
	mergeUsageWire(&state, usageWire{
		InputTokens:              intptr(100),
		CacheCreationInputTokens: intptr(8),
		CacheCreation:            &cacheCreationWire{Ephemeral5mInputTokens: intptr(5), Ephemeral1hInputTokens: intptr(3)},
		OutputTokens:             intptr(9),
	})

	facts := messageUsageFromWire(state).ToUsageFacts()
	if got, ok := facts.CacheWrite5mInputTokens.BillableValue(); !ok || got != 5 {
		t.Fatalf("cache_write_5m = %d ok=%v, want 5 (TTL split preserved)", got, ok)
	}
	if got, ok := facts.CacheWrite1hInputTokens.BillableValue(); !ok || got != 3 {
		t.Fatalf("cache_write_1h = %d ok=%v, want 3 (TTL split preserved)", got, ok)
	}
}

// TestMergeUsageWireOfficialCumulativeNoDoubleCount 锁定官方 Anthropic 形状:message_start 与
// message_delta 都带**相同的累计** cache/token(delta 只有 flat、无嵌套)。合并必须是「覆盖」而非
// 「相加」——否则会像 langchain 那样把 cache 翻倍(issue #10249)。同时 delta 无嵌套但其 flat 与
// message_start 的嵌套汇总一致 → 保留 message_start 的 5m 分层,不被误清。
func TestMergeUsageWireOfficialCumulativeNoDoubleCount(t *testing.T) {
	var state usageWire

	// message_start:完整 usage,含嵌套 5m 分层。
	mergeUsageWire(&state, usageWire{
		InputTokens:              intptr(2000),
		CacheReadInputTokens:     intptr(1000),
		CacheCreationInputTokens: intptr(500),
		CacheCreation:            &cacheCreationWire{Ephemeral5mInputTokens: intptr(500), Ephemeral1hInputTokens: intptr(0)},
		OutputTokens:             intptr(1),
	})
	// message_delta:累计值(与 start 相同的 cache),仅 flat、无嵌套,output 增长。
	mergeUsageWire(&state, usageWire{
		InputTokens:              intptr(2000),
		CacheReadInputTokens:     intptr(1000),
		CacheCreationInputTokens: intptr(500),
		OutputTokens:             intptr(50),
	})

	facts := messageUsageFromWire(state).ToUsageFacts()
	if got, ok := facts.CacheWrite5mInputTokens.BillableValue(); !ok || got != 500 {
		t.Fatalf("cache_write_5m = %d ok=%v, want 500 (overwrite, not doubled to 1000)", got, ok)
	}
	if got, ok := facts.CacheReadInputTokens.BillableValue(); !ok || got != 1000 {
		t.Fatalf("cache_read = %d ok=%v, want 1000 (overwrite, not doubled)", got, ok)
	}
	if got, ok := facts.OutputTokensTotal.BillableValue(); !ok || got != 50 {
		t.Fatalf("output = %d ok=%v, want 50 (cumulative final, not summed)", got, ok)
	}
}

// TestMergeUsageWireOfficial1hTierPreserved 验证官方 1h 缓存分层被正确保留:message_start 给出
// 1h 分层,message_delta 只给一致的 flat(无嵌套) → 保留 1h(2× 定价),不被误清、不塌缩为 5m。
func TestMergeUsageWireOfficial1hTierPreserved(t *testing.T) {
	var state usageWire
	mergeUsageWire(&state, usageWire{
		InputTokens:              intptr(100),
		CacheCreationInputTokens: intptr(500),
		CacheCreation:            &cacheCreationWire{Ephemeral5mInputTokens: intptr(0), Ephemeral1hInputTokens: intptr(500)},
		OutputTokens:             intptr(1),
	})
	mergeUsageWire(&state, usageWire{
		CacheCreationInputTokens: intptr(500),
		OutputTokens:             intptr(20),
	})

	facts := messageUsageFromWire(state).ToUsageFacts()
	if got, ok := facts.CacheWrite1hInputTokens.BillableValue(); !ok || got != 500 {
		t.Fatalf("cache_write_1h = %d ok=%v, want 500 (1h tier preserved, not collapsed to 5m)", got, ok)
	}
	if got, ok := facts.CacheWrite5mInputTokens.BillableValue(); !ok || got != 0 {
		t.Fatalf("cache_write_5m = %d ok=%v, want 0", got, ok)
	}
}

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
