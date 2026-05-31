package openai

import (
	"testing"

	"github.com/ThankCat/unio-api/internal/core/adapter"
	"github.com/ThankCat/unio-api/internal/platform/failure"
)

func TestAdapterCountChatInputTokensCountsMessages(t *testing.T) {
	openAIAdapter := newTestAdapter(nil)

	got, err := openAIAdapter.CountChatInputTokens(adapter.ChatInputTokenizeRequest{
		Model: "gpt-4.1",
		Messages: []adapter.ChatMessage{
			{Role: "system", Content: jsonContent("You are concise.")},
			{Role: "user", Content: jsonContent("Hello")},
		},
	})
	if err != nil {
		t.Fatalf("CountChatInputTokens returned error: %v", err)
	}
	if got <= 0 {
		t.Fatalf("expected positive token count, got %d", got)
	}
}

func TestAdapterCountChatInputTokensRejectsEmptyModel(t *testing.T) {
	openAIAdapter := newTestAdapter(nil)

	_, err := openAIAdapter.CountChatInputTokens(adapter.ChatInputTokenizeRequest{
		Model: " ",
		Messages: []adapter.ChatMessage{
			{Role: "user", Content: jsonContent("Hello")},
		},
	})
	if failure.CodeOf(err) != failure.CodeAdapterTokenizeFailed {
		t.Fatalf("expected failure code %q, got %q", failure.CodeAdapterTokenizeFailed, failure.CodeOf(err))
	}
}

func TestFallbackEncodingCoversKnownOpenAIModelFamilies(t *testing.T) {
	for _, model := range []string{"gpt-5", "gpt-4.1-mini", "gpt-4o", "o3-mini", "gpt-4-turbo", "gpt-3.5-turbo"} {
		t.Run(model, func(t *testing.T) {
			if _, ok := fallbackEncoding(model); !ok {
				t.Fatalf("expected fallback encoding for %q", model)
			}
		})
	}
}
