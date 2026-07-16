package messages

import (
	"bytes"
	"encoding/json"

	"github.com/ThankCat/unio-gateway/internal/app/gatewayapi/anthropic"
)

// StreamEvent 是 Anthropic Messages 流式响应中的一个具名 SSE 事件。
//
// Anthropic 流式 wire framing 为：
//
//	event: <EventName>\n
//	data: <json，其 type 字段与 EventName 相同>\n\n
//
// 因此每个事件 payload 同时携带 type 字段（用于 data JSON）并实现 EventName（用于 event: 行），
// 二者必须一致。各事件类型见 ANTHROPIC_MESSAGES_MATRIX.md 的 SSE 章节。
type StreamEvent interface {
	EventName() string
}

// StreamMessageStart 是流首个事件，携带初始 message（content 通常为空，usage 含输入计量）。
type StreamMessageStart struct {
	Type    string          `json:"type"`
	Message MessageResponse `json:"message"`
}

func (StreamMessageStart) EventName() string { return "message_start" }

// StreamContentBlockStart 标记某个 content block 开始；content_block 为该 block 的初始结构。
type StreamContentBlockStart struct {
	Type         string          `json:"type"`
	Index        int             `json:"index"`
	ContentBlock json.RawMessage `json:"content_block"`
}

func (StreamContentBlockStart) EventName() string { return "content_block_start" }

// StreamContentBlockDelta 是某个 content block 的增量。
type StreamContentBlockDelta struct {
	Type  string            `json:"type"`
	Index int               `json:"index"`
	Delta ContentBlockDelta `json:"delta"`
}

func (StreamContentBlockDelta) EventName() string { return "content_block_delta" }

// ContentBlockDelta 是 content_block_delta 的 delta union。
//
// delta.type 决定生效字段：
//   - text_delta      -> Text
//   - input_json_delta -> PartialJSON（tool_use 入参增量）
//   - thinking_delta  -> Thinking
//   - signature_delta -> Signature
type ContentBlockDelta struct {
	Type        string `json:"type"`
	Text        string `json:"text,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
	Thinking    string `json:"thinking,omitempty"`
	Signature   string `json:"signature,omitempty"`
}

// StreamContentBlockStop 标记某个 content block 结束。
type StreamContentBlockStop struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
}

func (StreamContentBlockStop) EventName() string { return "content_block_stop" }

// StreamMessageDelta 携带顶层 message 的增量（stop_reason/stop_sequence）与累计 usage。
type StreamMessageDelta struct {
	Type  string           `json:"type"`
	Delta MessageDeltaBody `json:"delta"`
	Usage *MessageUsage    `json:"usage,omitempty"`
}

func (StreamMessageDelta) EventName() string { return "message_delta" }

// MessageDeltaBody 是 message_delta 的 delta 对象。
// stop_reason / stop_sequence 用指针表达"本事件未携带"与"显式为 null"的区别。
type MessageDeltaBody struct {
	StopReason   *string `json:"stop_reason,omitempty"`
	StopSequence *string `json:"stop_sequence,omitempty"`
}

// StreamMessageStop 标记整个 message 流结束。
type StreamMessageStop struct {
	Type string `json:"type"`
}

func (StreamMessageStop) EventName() string { return "message_stop" }

// StreamPing 是保活事件。
type StreamPing struct {
	Type string `json:"type"`
}

func (StreamPing) EventName() string { return "ping" }

// StreamError 是流中途的错误事件，沿用 Anthropic 原生 error 形状。
type StreamError struct {
	Type  string              `json:"type"`
	Error anthropic.ErrorBody `json:"error"`
}

func (StreamError) EventName() string { return "error" }

// StreamFrame 是 service 层交给 HTTP 层写出的一个 Anthropic 原生 SSE 事件帧。
type StreamFrame struct {
	EventType string
	Data      json.RawMessage
}

// EncodeStreamEvent 把一个具名事件编码为 Anthropic SSE 帧（含结尾空行）。
func EncodeStreamEvent(ev StreamEvent) ([]byte, error) {
	data, err := json.Marshal(ev)
	if err != nil {
		return nil, err
	}

	var b bytes.Buffer
	b.WriteString("event: ")
	b.WriteString(ev.EventName())
	b.WriteByte('\n')
	b.WriteString("data: ")
	b.Write(data)
	b.WriteString("\n\n")
	return b.Bytes(), nil
}
