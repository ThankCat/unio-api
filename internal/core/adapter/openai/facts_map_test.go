package openai

import (
	"testing"

	"github.com/ThankCat/unio-api/internal/core/adapter"
	"github.com/ThankCat/unio-api/internal/core/usage"
)

func TestOpenAIFinishClassMapsStableCategories(t *testing.T) {
	tests := map[string]adapter.FinishClass{
		"":               adapter.FinishStop,
		"stop":           adapter.FinishStop,
		"length":         adapter.FinishLength,
		"tool_calls":     adapter.FinishToolUse,
		"function_call":  adapter.FinishToolUse,
		"content_filter": adapter.FinishContentFilter,
		"mystery_reason": adapter.FinishOther,
	}

	for raw, want := range tests {
		if got := openAIFinishClass(raw); got != want {
			t.Fatalf("openAIFinishClass(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestResponseFactsNonStreamBuildsNeutralFacts(t *testing.T) {
	chatUsage := adapter.ChatUsage{
		PromptTokens:     100,
		CompletionTokens: 40,
		TotalTokens:      140,
		CachedTokens:     30,
		ReasoningTokens:  5,
	}
	meta := adapter.UpstreamMetadata{StatusCode: 200, RequestID: "req-abc"}

	facts := responseFactsNonStream("chatcmpl-1", "deepseek-v4-flash", "tool_calls", chatUsage, meta)

	if facts.UpstreamProtocol != "openai" {
		t.Fatalf("UpstreamProtocol = %q, want openai", facts.UpstreamProtocol)
	}
	if facts.UpstreamResponseID != "chatcmpl-1" {
		t.Fatalf("UpstreamResponseID = %q", facts.UpstreamResponseID)
	}
	if facts.UpstreamModel != "deepseek-v4-flash" {
		t.Fatalf("UpstreamModel = %q", facts.UpstreamModel)
	}
	if facts.Finish.Class != adapter.FinishToolUse || facts.Finish.RawReason != "tool_calls" {
		t.Fatalf("Finish = %+v", facts.Finish)
	}
	if facts.UsageSource != usage.SourceUpstreamResponse {
		t.Fatalf("UsageSource = %q, want %q", facts.UsageSource, usage.SourceUpstreamResponse)
	}
	if facts.UsageMappingVersion != usageMappingVersionOpenAI {
		t.Fatalf("UsageMappingVersion = %q", facts.UsageMappingVersion)
	}
	if facts.Metadata != meta {
		t.Fatalf("Metadata = %+v, want %+v", facts.Metadata, meta)
	}

	if got, ok := facts.Usage.UncachedInputTokens.BillableValue(); !ok || got != 70 {
		t.Fatalf("uncached input billable = (%d, %v), want (70, true)", got, ok)
	}
	if got, ok := facts.Usage.CacheReadInputTokens.BillableValue(); !ok || got != 30 {
		t.Fatalf("cache read billable = (%d, %v), want (30, true)", got, ok)
	}
	if facts.Usage.CacheWrite5mInputTokens.State != usage.CountNotApplicable {
		t.Fatalf("cache write 5m state = %q, want not_applicable", facts.Usage.CacheWrite5mInputTokens.State)
	}
	if got, ok := facts.Usage.OutputTokensTotal.BillableValue(); !ok || got != 40 {
		t.Fatalf("output total billable = (%d, %v), want (40, true)", got, ok)
	}
}
