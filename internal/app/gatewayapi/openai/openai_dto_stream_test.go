package openai

import (
	"encoding/json"
	"testing"
)

func TestChatCompletionStreamResponseMarshalUsageNull(t *testing.T) {
	resp := ChatCompletionStreamResponse{
		ID:              "chatcmpl_test",
		Object:          "chat.completion.chunk",
		Model:           "openai/gpt-4.1",
		EmitUsageAsNull: true,
		Choices: []ChatCompletionStreamChoice{
			{
				Index: 0,
				Delta: ChatCompletionStreamDelta{Content: "hi"},
			},
		},
	}

	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if string(obj["usage"]) != "null" {
		t.Fatalf("expected usage null, got %s", string(obj["usage"]))
	}
}
