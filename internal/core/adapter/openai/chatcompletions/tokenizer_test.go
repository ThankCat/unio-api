package chatcompletions

import (
	"testing"
)

func TestAdapterCountChatInputTokensCountsMessages(t *testing.T) {
	openAIAdapter := newTestAdapter(nil)

	got, err := openAIAdapter.CountChatInputTokens(ChatRequest{
		Model: "gpt-4.1",
		Messages: []ChatMessage{
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

func TestAdapterCountChatInputTokensAcceptsEmptyModel(t *testing.T) {
	openAIAdapter := newTestAdapter(nil)

	// 空模型名不再硬失败：估算是保守上界，无法解析 tokenizer 时回退默认编码（对齐 new-api）。
	got, err := openAIAdapter.CountChatInputTokens(ChatRequest{
		Model: " ",
		Messages: []ChatMessage{
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

func TestAdapterCountChatInputTokensIncludesTools(t *testing.T) {
	openAIAdapter := newTestAdapter(nil)
	req := ChatRequest{
		Model:    "gpt-4.1",
		Messages: []ChatMessage{{Role: "user", Content: jsonContent("Hello")}},
	}

	withoutTools, err := openAIAdapter.CountChatInputTokens(req)
	if err != nil {
		t.Fatalf("CountChatInputTokens without tools returned error: %v", err)
	}

	req.Tools = []ChatTool{{
		Type: "function",
		Function: ChatFunctionTool{
			Name:        "search_docs",
			Description: "Search product documentation for a detailed answer.",
			Parameters:  []byte(`{"type":"object","properties":{"query":{"type":"string"}}}`),
		},
	}}
	withTools, err := openAIAdapter.CountChatInputTokens(req)
	if err != nil {
		t.Fatalf("CountChatInputTokens with tools returned error: %v", err)
	}
	if withTools <= withoutTools {
		t.Fatalf("expected tools to increase estimate, got without=%d with=%d", withoutTools, withTools)
	}
}

func TestAdapterCountChatInputTokensAcceptsUnknownModelName(t *testing.T) {
	openAIAdapter := newTestAdapter(nil)

	got, err := openAIAdapter.CountChatInputTokens(ChatRequest{
		Model: "gpt-5.5",
		Messages: []ChatMessage{
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

func TestAdapterCountChatInputTokensCoversKnownOpenAIModelFamilies(t *testing.T) {
	openAIAdapter := newTestAdapter(nil)
	for _, model := range []string{"gpt-5", "gpt-4.1-mini", "gpt-4o", "o3-mini", "gpt-4-turbo", "gpt-3.5-turbo"} {
		t.Run(model, func(t *testing.T) {
			got, err := openAIAdapter.CountChatInputTokens(ChatRequest{
				Model:    model,
				Messages: []ChatMessage{{Role: "user", Content: jsonContent("Hello world")}},
			})
			if err != nil {
				t.Fatalf("CountChatInputTokens returned error: %v", err)
			}
			if got <= 0 {
				t.Fatalf("expected positive token count for %q, got %d", model, got)
			}
		})
	}
}
