package messages

import (
	"testing"
)

func TestEstimateMessagesInputTokensCountsWireBody(t *testing.T) {
	got, err := EstimateMessagesInputTokens(MessagesInputTokenizeRequest{
		Model:  "claude-sonnet-4-20250514",
		System: []byte(`"You are concise."`),
		Messages: []Message{
			{Role: "user", Content: []byte(`"Hello"`)},
		},
	})
	if err != nil {
		t.Fatalf("EstimateMessagesInputTokens: %v", err)
	}
	if got <= 0 {
		t.Fatalf("expected positive token count, got %d", got)
	}
}

func TestEstimateMessagesInputTokensIncludesTools(t *testing.T) {
	base := MessagesInputTokenizeRequest{
		Model: "claude-sonnet-4-20250514",
		Messages: []Message{
			{Role: "user", Content: []byte(`"Hello"`)},
		},
	}

	withoutTools, err := EstimateMessagesInputTokens(base)
	if err != nil {
		t.Fatalf("without tools: %v", err)
	}

	base.Tools = []byte(`[{"name":"search","description":"Search docs","input_schema":{"type":"object"}}]`)
	withTools, err := EstimateMessagesInputTokens(base)
	if err != nil {
		t.Fatalf("with tools: %v", err)
	}
	if withTools <= withoutTools {
		t.Fatalf("expected tools to increase estimate, got without=%d with=%d", withoutTools, withTools)
	}
}

func TestEstimateMessagesInputTokensAcceptsEmptyModel(t *testing.T) {
	// 空模型名不再硬失败：估算是保守上界，无法解析 tokenizer 时回退默认编码（对齐 new-api）。
	got, err := EstimateMessagesInputTokens(MessagesInputTokenizeRequest{
		Model: " ",
		Messages: []Message{
			{Role: "user", Content: []byte(`"Hello"`)},
		},
	})
	if err != nil {
		t.Fatalf("EstimateMessagesInputTokens: %v", err)
	}
	if got <= 0 {
		t.Fatalf("expected positive token count, got %d", got)
	}
}

func TestAdapterCountMessagesInputTokensDelegatesToEstimate(t *testing.T) {
	adapter := NewAdapter(nil)
	got, err := adapter.CountMessagesInputTokens(MessagesInputTokenizeRequest{
		Model: "claude-sonnet-4-20250514",
		Messages: []Message{
			{Role: "user", Content: []byte(`"Hello"`)},
		},
	})
	if err != nil {
		t.Fatalf("CountMessagesInputTokens: %v", err)
	}
	if got <= 0 {
		t.Fatalf("expected positive token count, got %d", got)
	}
}
