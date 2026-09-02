package chatbridge

import (
	"encoding/json"

	chatcompletions "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/chatcompletions"
	responses "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/responses"
)

// streamTranslator 把 Responses SSE 事件流译成 chat chunk 流。
//
// 事件 → chunk 映射（权威首字见 bridge.go 注释）：
//   - response.output_text.delta → 携带 Content 的 chunk（首个即权威首字）；
//   - response.output_item.added(function_call) → tool_calls 起始 chunk（id+name+空 arguments）；
//   - response.function_call_arguments.delta → tool_calls 增量 chunk（arguments 增量）；
//   - response.completed / incomplete → 终态 chunk（finish_reason + usage 由外层 outcome 承载）；
//   - created / in_progress / output_item.done 等生命周期事件 → 不产出 chunk。
type streamTranslator struct {
	model string
	emit  func(chatcompletions.ChatStreamChunk) error

	responseID    string
	roleSent      bool
	sawToolCall   bool
	toolCallIndex int
	// callIndexByItem 把 responses 的 item_id 映射到 chat tool_calls 的稳定 index。
	callIndexByItem map[string]int
	finishSent      bool
	terminalReason  string
}

func newStreamTranslator(model string, emit func(chatcompletions.ChatStreamChunk) error) *streamTranslator {
	return &streamTranslator{model: model, emit: emit, callIndexByItem: map[string]int{}}
}

// streamEventEnvelope 是桥接关心的事件字段合集（其余字段忽略）。
type streamEventEnvelope struct {
	Type   string `json:"type"`
	ItemID string `json:"item_id"`
	Delta  string `json:"delta"`
	Item   *struct {
		ID     string `json:"id"`
		Type   string `json:"type"`
		CallID string `json:"call_id"`
		Name   string `json:"name"`
	} `json:"item"`
	Response *struct {
		ID                string `json:"id"`
		Status            string `json:"status"`
		IncompleteDetails *struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
	} `json:"response"`
}

func (t *streamTranslator) consume(chunk responses.StreamChunk) error {
	var event streamEventEnvelope
	if err := json.Unmarshal(chunk.Data, &event); err != nil {
		// 未知形状的事件不阻断流：桥接只译自己认识的事件，其余静默跳过。
		return nil
	}
	if event.Response != nil && event.Response.ID != "" {
		t.responseID = event.Response.ID
	}
	if chunk.ResponseID != "" {
		t.responseID = chunk.ResponseID
	}

	switch event.Type {
	case "response.output_text.delta":
		if event.Delta == "" {
			return nil
		}
		return t.emitChunk(chatcompletions.ChatStreamChunk{Content: event.Delta})

	case "response.output_item.added":
		if event.Item == nil || (event.Item.Type != "function_call" && event.Item.Type != "custom_tool_call") {
			return nil
		}
		index := t.toolCallIndex
		t.toolCallIndex++
		t.callIndexByItem[event.Item.ID] = index
		t.sawToolCall = true
		payload, err := json.Marshal([]map[string]any{{
			"index": index,
			"id":    event.Item.CallID,
			"type":  "function",
			"function": map[string]any{
				"name":      event.Item.Name,
				"arguments": "",
			},
		}})
		if err != nil {
			return nil
		}
		return t.emitChunk(chatcompletions.ChatStreamChunk{ToolCalls: payload})

	case "response.function_call_arguments.delta", "response.custom_tool_call_input.delta":
		index, ok := t.callIndexByItem[event.ItemID]
		if !ok || event.Delta == "" {
			return nil
		}
		payload, err := json.Marshal([]map[string]any{{
			"index":    index,
			"function": map[string]any{"arguments": event.Delta},
		}})
		if err != nil {
			return nil
		}
		return t.emitChunk(chatcompletions.ChatStreamChunk{ToolCalls: payload})

	case "response.completed", "response.incomplete":
		reason := "stop"
		if t.sawToolCall {
			reason = "tool_calls"
		} else if event.Response != nil && event.Response.Status == "incomplete" &&
			event.Response.IncompleteDetails != nil {
			switch event.Response.IncompleteDetails.Reason {
			case "max_output_tokens", "max_tokens":
				reason = "length"
			case "content_filter":
				reason = "content_filter"
			}
		}
		t.terminalReason = reason
		t.finishSent = true
		if err := t.emitChunk(chatcompletions.ChatStreamChunk{FinishReason: &reason}); err != nil {
			return err
		}
		// 终态 usage 走独立 chunk（与 chat 上游的 usage 尾帧同形态）：
		// 共享流式循环据 chunk.Usage 抑制 emit 并把它记为 finalUsage，
		// 客户可见的 usage 帧由 Finish + include_usage 决定，不在这里直写。
		if chunk.Usage != nil {
			usage := *chunk.Usage
			return t.emitChunk(chatcompletions.ChatStreamChunk{Usage: &usage})
		}
		return nil

	default:
		return nil
	}
}

// finish 保证终态 chunk 至少发一次（上游缺正式终态时由外层判 incomplete，不在这里补）。
func (t *streamTranslator) finish() error {
	return nil
}

func (t *streamTranslator) emitChunk(chunk chatcompletions.ChatStreamChunk) error {
	chunk.ID = t.responseID
	chunk.Model = t.model
	if !t.roleSent {
		chunk.Role = "assistant"
		t.roleSent = true
	}
	return t.emit(chunk)
}
