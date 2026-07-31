package responses

import "encoding/json"

// 权威首字判定（OpenAI Responses）。判定是事件的纯函数，理由见 chatcompletions/first_token.go。

const (
	eventOutputTextDelta           = "response.output_text.delta"
	eventReasoningTextDelta        = "response.reasoning_text.delta"
	eventReasoningSummaryTextDelta = "response.reasoning_summary_text.delta"
	eventRefusalDelta              = "response.refusal.delta"
	eventFunctionCallArgsDelta     = "response.function_call_arguments.delta"
)

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
