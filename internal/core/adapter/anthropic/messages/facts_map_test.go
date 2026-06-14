package messages

import (
	"testing"

	"github.com/ThankCat/unio-api/internal/core/adapter"
	"github.com/ThankCat/unio-api/internal/core/usage"
)

func TestAnthropicFinishClass(t *testing.T) {
	cases := map[string]adapter.FinishClass{
		"":              adapter.FinishStop,
		"end_turn":      adapter.FinishStop,
		"stop_sequence": adapter.FinishStop,
		"max_tokens":    adapter.FinishLength,
		"tool_use":      adapter.FinishToolUse,
		"pause_turn":    adapter.FinishPause,
		"refusal":       adapter.FinishRefusal,
		"weird_future":  adapter.FinishOther,
	}
	for raw, want := range cases {
		if got := anthropicFinishClass(raw); got != want {
			t.Errorf("anthropicFinishClass(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestResponseFactsNonStream(t *testing.T) {
	u := MessageUsage{InputTokens: 11, OutputTokens: 3, CacheReadInputTokens: intptr(0), CacheCreationInputTokens: intptr(0)}
	meta := adapter.UpstreamMetadata{StatusCode: 200, RequestID: "req-1"}

	facts := ResponseFactsNonStream("msg_1", "deepseek-v4-flash", "end_turn", u, meta)

	if facts.UpstreamProtocol != "anthropic" {
		t.Fatalf("protocol = %q", facts.UpstreamProtocol)
	}
	if facts.UpstreamResponseID != "msg_1" || facts.UpstreamModel != "deepseek-v4-flash" {
		t.Fatalf("id/model = %q/%q", facts.UpstreamResponseID, facts.UpstreamModel)
	}
	if facts.Finish.Class != adapter.FinishStop || facts.Finish.RawReason != "end_turn" {
		t.Fatalf("finish = %#v", facts.Finish)
	}
	if facts.UsageSource != usage.SourceUpstreamResponse {
		t.Fatalf("source = %q", facts.UsageSource)
	}
	if facts.UsageMappingVersion != usageMappingVersionAnthropic {
		t.Fatalf("mapping version = %q", facts.UsageMappingVersion)
	}
	if facts.Metadata != meta {
		t.Fatalf("metadata = %#v", facts.Metadata)
	}
}

func TestResponseFactsStreamSource(t *testing.T) {
	facts := ResponseFactsStream("msg_2", "deepseek-v4-flash", "tool_use", MessageUsage{InputTokens: 1, OutputTokens: 1}, adapter.UpstreamMetadata{})
	if facts.UsageSource != usage.SourceUpstreamStream {
		t.Fatalf("source = %q, want upstream_stream", facts.UsageSource)
	}
	if facts.Finish.Class != adapter.FinishToolUse {
		t.Fatalf("finish class = %q", facts.Finish.Class)
	}
}
