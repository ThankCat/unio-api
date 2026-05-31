package normalizer_test

import (
	"testing"

	"github.com/ThankCat/unio-api/internal/core/adapter"
	"github.com/ThankCat/unio-api/internal/core/adapter/openai/normalizer"
)

func TestDeepSeekKeyMatchesProviderSlug(t *testing.T) {
	deepSeek := normalizer.DeepSeek{}
	if deepSeek.Key() != normalizer.DeepSeekKey {
		t.Fatalf("got key %q, want %q", deepSeek.Key(), normalizer.DeepSeekKey)
	}
}

func TestDeepSeekNormalizeStreamEventMapsReasoningContentToContent(t *testing.T) {
	got, err := normalizer.DeepSeek{}.NormalizeStreamEvent(normalizer.StreamInput{
		ID:    "chatcmpl-deepseek",
		Model: "deepseek-v4-pro",
		Choices: []normalizer.StreamChoice{
			{
				ReasoningContent: "thinking",
			},
		},
	})
	if err != nil {
		t.Fatalf("NormalizeStreamEvent returned error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1", len(got))
	}
	if got[0].Content != "thinking" {
		t.Fatalf("got content %q, want thinking", got[0].Content)
	}
}

func TestDeepSeekNormalizeStreamEventPrefersContentOverReasoning(t *testing.T) {
	got, err := normalizer.DeepSeek{}.NormalizeStreamEvent(normalizer.StreamInput{
		ID:    "chatcmpl-deepseek",
		Model: "deepseek-v4-pro",
		Choices: []normalizer.StreamChoice{
			{
				Content:          "answer",
				ReasoningContent: "thinking",
			},
		},
	})
	if err != nil {
		t.Fatalf("NormalizeStreamEvent returned error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1", len(got))
	}
	if got[0].Content != "answer" {
		t.Fatalf("got content %q, want answer", got[0].Content)
	}
}

func TestDeepSeekNormalizeStreamEventDelegatesUsageTailToDefault(t *testing.T) {
	stop := "length"
	usage := adapter.ChatUsage{
		PromptTokens:     6,
		CompletionTokens: 20,
		TotalTokens:      26,
	}

	got, err := normalizer.DeepSeek{}.NormalizeStreamEvent(normalizer.StreamInput{
		ID:    "chatcmpl-deepseek",
		Model: "deepseek-v4-pro",
		Choices: []normalizer.StreamChoice{
			{
				ReasoningContent: "final-thought",
				FinishReason:     &stop,
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
	if got[0].Content != "final-thought" {
		t.Fatalf("got first content %q, want final-thought", got[0].Content)
	}
	if got[0].FinishReason == nil || *got[0].FinishReason != "length" {
		t.Fatalf("got finish event %+v, want finish_reason=length", got[0])
	}
	if got[1].Usage == nil || got[1].Usage.TotalTokens != 26 {
		t.Fatalf("got usage event %+v, want total_tokens=26", got[1])
	}
}

func TestRegistryResolveReturnsDeepSeekNormalizer(t *testing.T) {
	registry := normalizer.NewRegistry(normalizer.Default{}, normalizer.DeepSeek{})

	if registry.Resolve("deepseek").Key() != normalizer.DeepSeekKey {
		t.Fatal("expected deepseek normalizer for deepseek slug")
	}
}
