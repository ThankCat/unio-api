package responses

// Responses 流式命名事件类型常量。
//
// 仅枚举桥接层会发出的事件；codex-rs `process_responses_event` 实际消费的子集见
// RESPONSES_CHAT_BRIDGE.md §6（output_item.done 为权威载体）。事件序列状态机在
// responses_stream（TASK-11.07）。
const (
	EventResponseCreated           = "response.created"
	EventResponseInProgress        = "response.in_progress"
	EventOutputItemAdded           = "response.output_item.added"
	EventOutputItemDone            = "response.output_item.done"
	EventContentPartAdded          = "response.content_part.added"
	EventContentPartDone           = "response.content_part.done"
	EventOutputTextDelta           = "response.output_text.delta"
	EventOutputTextDone            = "response.output_text.done"
	EventReasoningTextDelta        = "response.reasoning_text.delta"
	EventReasoningTextDone         = "response.reasoning_text.done"
	EventReasoningSummaryTextDelta = "response.reasoning_summary_text.delta"
	EventReasoningSummaryTextDone  = "response.reasoning_summary_text.done"
	EventFunctionCallArgsDelta     = "response.function_call_arguments.delta"
	EventFunctionCallArgsDone      = "response.function_call_arguments.done"
	EventResponseCompleted         = "response.completed"
	EventResponseIncomplete        = "response.incomplete"
	EventResponseFailed            = "response.failed"
)

// ResponsesStreamEvent 表示 Responses 流式命名事件的通用信封。
//
// Responses 流是「命名事件 + 单调 sequence_number」。本结构覆盖桥接层会发出的事件载荷字段；
// 不同事件只填用到的字段（omitempty 控制），由 responses_stream 状态机组装（TASK-11.07）。
type ResponsesStreamEvent struct {
	Type           string             `json:"type"`
	SequenceNumber int64              `json:"sequence_number"`
	Response       *ResponsesResponse `json:"response,omitempty"`

	// item / 索引：output_item.added|done 与 part 事件使用。
	Item         *ResponseOutputItem `json:"item,omitempty"`
	OutputIndex  *int                `json:"output_index,omitempty"`
	ContentIndex *int                `json:"content_index,omitempty"`
	SummaryIndex *int                `json:"summary_index,omitempty"`
	ItemID       string              `json:"item_id,omitempty"`

	// 内容增量 / 终值：output_text / reasoning_text / function_call_arguments 事件使用。
	Delta string `json:"delta,omitempty"`
	Text  string `json:"text,omitempty"`

	// Part：content_part.added|done 事件携带的 part 形状。
	Part *ResponseOutputContent `json:"part,omitempty"`
}

// ResponsesStreamErrorEvent 是流尾不可恢复错误的 SSE `error` 事件载荷。
//
// 仅用于 SSE 已开始后无法再改写 HTTP 状态的场景；只渲染安全的 code/message/param，
// 不透传上游原始 body。首帧前的错误走 JSON error，不使用本事件。
type ResponsesStreamErrorEvent struct {
	Type    string  `json:"type"`
	Code    string  `json:"code,omitempty"`
	Message string  `json:"message"`
	Param   *string `json:"param,omitempty"`
}
