package streamtranslate_test

import (
	"testing"

	"github.com/ThankCat/unio-api/internal/core/adapter"
	"github.com/ThankCat/unio-api/internal/core/adapter/openai/streamtranslate"
)

func TestDeepSeekKeyMatchesProviderSlug(t *testing.T) {
	deepSeek := streamtranslate.DeepSeek{}
	if deepSeek.Key() != streamtranslate.DeepSeekKey {
		t.Fatalf("got key %q, want %q", deepSeek.Key(), streamtranslate.DeepSeekKey)
	}
}

func TestDeepSeekTranslateStreamEventKeepsReasoningContentSeparate(t *testing.T) {
	got, err := streamtranslate.DeepSeek{}.TranslateStreamEvent(streamtranslate.StreamInput{
		ID:    "chatcmpl-deepseek",
		Model: "deepseek-v4-pro",
		Choices: []streamtranslate.StreamChoice{
			{ReasoningContent: "thinking"},
		},
	})
	if err != nil {
		t.Fatalf("TranslateStreamEvent returned error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1", len(got))
	}
	if got[0].Content != "" {
		t.Fatalf("got content %q, want empty", got[0].Content)
	}
	if got[0].ReasoningContent != "thinking" {
		t.Fatalf("got reasoning %q, want thinking", got[0].ReasoningContent)
	}
}

func TestDeepSeekTranslateStreamEventEmitsBothContentAndReasoning(t *testing.T) {
	got, err := streamtranslate.DeepSeek{}.TranslateStreamEvent(streamtranslate.StreamInput{
		ID:    "chatcmpl-deepseek",
		Model: "deepseek-v4-pro",
		Choices: []streamtranslate.StreamChoice{
			{Content: "answer", ReasoningContent: "thinking"},
		},
	})
	if err != nil {
		t.Fatalf("TranslateStreamEvent returned error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1", len(got))
	}
	if got[0].Content != "answer" {
		t.Fatalf("got content %q, want answer", got[0].Content)
	}
	if got[0].ReasoningContent != "thinking" {
		t.Fatalf("got reasoning %q, want thinking", got[0].ReasoningContent)
	}
}

func TestDeepSeekTranslateStreamEventDelegatesUsageTailToDefault(t *testing.T) {
	stop := "length"
	usage := adapter.ChatUsage{
		PromptTokens:     6,
		CompletionTokens: 20,
		TotalTokens:      26,
	}

	got, err := streamtranslate.DeepSeek{}.TranslateStreamEvent(streamtranslate.StreamInput{
		ID:    "chatcmpl-deepseek",
		Model: "deepseek-v4-pro",
		Choices: []streamtranslate.StreamChoice{
			{ReasoningContent: "final-thought", FinishReason: &stop},
		},
		Usage: &usage,
	})
	if err != nil {
		t.Fatalf("TranslateStreamEvent returned error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2", len(got))
	}
	if got[0].ReasoningContent != "final-thought" {
		t.Fatalf("got first reasoning %q, want final-thought", got[0].ReasoningContent)
	}
	if got[0].FinishReason == nil || *got[0].FinishReason != "length" {
		t.Fatalf("got finish event %+v, want finish_reason=length", got[0])
	}
	if got[1].Usage == nil || got[1].Usage.TotalTokens != 26 {
		t.Fatalf("got usage event %+v, want total_tokens=26", got[1].Usage)
	}
}

func TestRegistryResolveReturnsDeepSeekTranslator(t *testing.T) {
	registry := streamtranslate.NewRegistry(streamtranslate.Default{}, streamtranslate.DeepSeek{})

	if registry.Resolve("deepseek").Key() != streamtranslate.DeepSeekKey {
		t.Fatal("expected deepseek translator for deepseek slug")
	}
}
