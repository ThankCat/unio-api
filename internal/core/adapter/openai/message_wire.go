package openai

import (
	"encoding/json"

	"github.com/ThankCat/unio-api/internal/core/adapter"
)

func adapterChatMessageToWire(msg adapter.ChatMessage) chatMessage {
	return chatMessage{
		Role:             mapWireMessageRole(msg.Role),
		Content:          cloneRawMessage(msg.Content),
		ReasoningContent: msg.ReasoningContent,
		ToolCallID:       msg.ToolCallID,
		ToolCalls:        marshalJSONValue(msg.ToolCalls),
	}
}

func cloneRawMessage(src json.RawMessage) json.RawMessage {
	if len(src) == 0 {
		return nil
	}

	return append(json.RawMessage(nil), src...)
}

func marshalJSONValue(v any) json.RawMessage {
	if v == nil {
		return nil
	}

	switch typed := v.(type) {
	case json.RawMessage:
		return cloneRawMessage(typed)
	case []adapter.ChatToolCall:
		if len(typed) == 0 {
			return nil
		}
	case []adapter.ChatTool:
		if len(typed) == 0 {
			return nil
		}
	}

	raw, err := json.Marshal(v)
	if err != nil || string(raw) == "null" {
		return nil
	}

	return raw
}

func upstreamFinishReason(choice chatChoice) string {
	return choice.FinishReason
}

func wireMessageContentString(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}

	var text string
	if err := json.Unmarshal(content, &text); err == nil {
		return text
	}

	return ""
}

func wireToolCallsToAdapter(raw json.RawMessage) ([]adapter.ChatToolCall, error) {
	if len(raw) == 0 {
		return nil, nil
	}

	var calls []adapter.ChatToolCall
	if err := json.Unmarshal(raw, &calls); err != nil {
		return nil, err
	}

	return calls, nil
}

func adapterResponseFormatToWire(format *adapter.ChatResponseFormat) *chatResponseFormat {
	if format == nil {
		return nil
	}

	return &chatResponseFormat{
		Type:       format.Type,
		JSONSchema: cloneRawMessage(format.JSONSchema),
	}
}
