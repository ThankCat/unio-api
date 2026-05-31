package normalizer_test

import (
	"testing"

	"github.com/ThankCat/unio-api/internal/core/adapter"
	"github.com/ThankCat/unio-api/internal/core/adapter/openai/normalizer"
)

func TestDefaultNormalizeStreamEventOpenAIUsageTail(t *testing.T) {
	usage := adapter.ChatUsage{
		PromptTokens:     5,
		CompletionTokens: 6,
		TotalTokens:      11,
	}

	got, err := normalizer.Default{}.NormalizeStreamEvent(normalizer.StreamInput{
		ID:    "chatcmpl-fixture",
		Model: "gpt-4.1",
		Usage: &usage,
	})
	if err != nil {
		t.Fatalf("NormalizeStreamEvent returned error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1", len(got))
	}
	if got[0].Usage == nil || got[0].Usage.TotalTokens != 11 {
		t.Fatalf("got usage %+v, want total_tokens=11", got[0].Usage)
	}
}

func TestDefaultNormalizeStreamEventDeepSeekUsageWithChoices(t *testing.T) {
	stop := "length"
	usage := adapter.ChatUsage{
		PromptTokens:     6,
		CompletionTokens: 20,
		TotalTokens:      26,
		ReasoningTokens:  20,
	}

	got, err := normalizer.Default{}.NormalizeStreamEvent(normalizer.StreamInput{
		ID:    "chatcmpl-deepseek",
		Model: "deepseek-v4-pro",
		Choices: []normalizer.StreamChoice{
			{
				FinishReason: &stop,
			},
		},
		Usage: &usage,
	})
	if err != nil {
		t.Fatalf("NormalizeStreamEvent returned error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2", len(got))
	}
	if got[0].FinishReason == nil || *got[0].FinishReason != "length" {
		t.Fatalf("got finish event %+v, want finish_reason=length", got[0])
	}
	if got[1].Usage == nil || got[1].Usage.TotalTokens != 26 {
		t.Fatalf("got usage event %+v, want total_tokens=26", got[1])
	}
}

func TestDefaultNormalizeStreamEventSkipsEmptyHeartbeat(t *testing.T) {
	got, err := normalizer.Default{}.NormalizeStreamEvent(normalizer.StreamInput{
		ID:    "chatcmpl-fixture",
		Model: "gpt-4.1",
		Choices: []normalizer.StreamChoice{
			{},
		},
	})
	if err != nil {
		t.Fatalf("NormalizeStreamEvent returned error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d events, want 0", len(got))
	}
}

type testVendorNormalizer struct{}

func (testVendorNormalizer) Key() normalizer.Key { return "test-vendor" }

func (testVendorNormalizer) NormalizeStreamEvent(in normalizer.StreamInput) ([]normalizer.StreamEvent, error) {
	return nil, nil
}

func TestRegistryResolveReturnsVendorThenDefault(t *testing.T) {
	vendor := testVendorNormalizer{}
	registry := normalizer.NewRegistry(normalizer.Default{}, vendor)

	if registry.Resolve("test-vendor").Key() != vendor.Key() {
		t.Fatal("expected vendor normalizer for matching slug")
	}
	if registry.Resolve("unknown").Key() != normalizer.DefaultKey {
		t.Fatal("expected default normalizer for unknown slug")
	}
}
