package responses

import (
	"encoding/json"
	"strings"
)

// 权威首字判定（OpenAI Responses）。判定是事件的纯函数，理由见 chatcompletions/first_token.go。
//
// 本文件有两个正交判定（2026-09-05 对齐 Sub2API）：
//   - FirstTokenPayload：可见生成内容（文本 / 推理摘要 / 工具参数），决定 partial settlement 的计量文本，
//     以及 visible 口径下的 TTFT；
//   - StreamProgress：上游结构性进展（跳过协议前导后的首个事件），决定首字预算解除、前导缓冲写出与
//     fallback 锁定，以及 semantic 口径下的 TTFT。reasoning 模型思考阶段只有进展没有可见内容，
//     两者分开才能既不掐断思考、又不在客户没看到正文时收费。

const (
	eventOutputTextDelta           = "response.output_text.delta"
	eventReasoningTextDelta        = "response.reasoning_text.delta"
	eventReasoningSummaryTextDelta = "response.reasoning_summary_text.delta"
	eventRefusalDelta              = "response.refusal.delta"
	eventFunctionCallArgsDelta     = "response.function_call_arguments.delta"
	eventContentPartAdded          = "response.content_part.added"
	eventReasoningSummaryPartAdded = "response.reasoning_summary_part.added"
)

// StreamProgress 判定事件是否表示上游已在推进本次响应（Sub2API 的 client output started 口径）：
//
//   - 协议前导 response.created / response.queued / response.in_progress 不算——它们只说明请求被接收；
//   - response.failed 与 error 事件不算：它们是失败终态，是否可换号由 runner 的重试分类决定；
//   - response.output_item.added 的 reasoning item 要带 encrypted_content 或非空 summary 才算
//     （空壳只是"开始思考"的占位）；message item 要有非空内容；function_call 要有 arguments；
//     custom_tool_call 要有 input；compaction 要有 encrypted_content；其余 item 类型一律算；
//   - response.content_part.added / response.reasoning_summary_part.added 要带非空文本才算；
//   - 其余事件（含各类 .done、.delta、未知类型）一律算进展。
func StreamProgress(chunk StreamChunk) bool {
	switch chunk.EventType {
	case eventResponseCreated, eventResponseQueued, eventResponseInProgress, eventResponseFailed, eventError:
		return false
	case eventOutputItemAdded:
		return outputItemAddedHasProgress(chunk.Data)
	case eventContentPartAdded, eventReasoningSummaryPartAdded:
		return addedPartHasProgress(chunk.Data)
	}
	return len(chunk.Data) > 0
}

func outputItemAddedHasProgress(data []byte) bool {
	var envelope struct {
		Item struct {
			Type             string          `json:"type"`
			EncryptedContent string          `json:"encrypted_content"`
			Summary          json.RawMessage `json:"summary"`
			Content          json.RawMessage `json:"content"`
			Arguments        string          `json:"arguments"`
			Input            string          `json:"input"`
		} `json:"item"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		// 解析不了的 item 不猜形状：按进展处理，交给上游语义。
		return true
	}
	item := envelope.Item
	switch item.Type {
	case "reasoning":
		return item.EncryptedContent != "" || partsHaveText(item.Summary)
	case "message":
		return partsHaveText(item.Content)
	case "function_call":
		return item.Arguments != ""
	case "custom_tool_call":
		return item.Input != ""
	case "compaction":
		return item.EncryptedContent != ""
	default:
		return true
	}
}

func addedPartHasProgress(data []byte) bool {
	var envelope struct {
		Part struct {
			Type    string `json:"type"`
			Text    string `json:"text"`
			Refusal string `json:"refusal"`
		} `json:"part"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return true
	}
	switch envelope.Part.Type {
	case "output_text", "summary_text":
		return envelope.Part.Text != ""
	case "refusal":
		return envelope.Part.Refusal != ""
	default:
		return true
	}
}

// partsHaveText 判定 content/summary 数组里是否有任一带非空 text（或非 text 类型）的段。
func partsHaveText(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var parts []struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Refusal string `json:"refusal"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return strings.TrimSpace(string(raw)) != "[]" && strings.TrimSpace(string(raw)) != "null"
	}
	for _, part := range parts {
		switch part.Type {
		case "output_text", "summary_text", "reasoning_text":
			if part.Text != "" {
				return true
			}
		case "refusal":
			if part.Refusal != "" {
				return true
			}
		default:
			return true
		}
	}
	return false
}

// FirstTokenPayload 返回事件携带的生成负载；非空即「算首字」。
//
// 算首字：非空 output/reasoning/refusal delta、function arguments delta，
// 以及携带真实工具名称或参数的 output item。
// 不算首字：response.created/queued/in_progress、纯 ID/index/sequence、part/item 控制事件、
// usage、completed/incomplete/failed、error、[DONE]。
func FirstTokenPayload(chunk StreamChunk) string {
	switch chunk.EventType {
	case eventOutputTextDelta,
		eventReasoningTextDelta,
		eventReasoningSummaryTextDelta,
		eventRefusalDelta,
		eventFunctionCallArgsDelta:
		var delta struct {
			Delta string `json:"delta"`
		}
		if json.Unmarshal(chunk.Data, &delta) == nil {
			return delta.Delta
		}
	case eventOutputItemAdded:
		// 文本 output item 的 name/arguments 为空 → 不算首字；function_call item 携带工具名才算。
		var envelope struct {
			Item struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"item"`
		}
		if json.Unmarshal(chunk.Data, &envelope) == nil {
			return envelope.Item.Name + envelope.Item.Arguments
		}
	}
	return ""
}
