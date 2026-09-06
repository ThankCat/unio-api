package dto

import "encoding/json"

// StreamFrame 是 service 层交给 HTTP 层写出的一个 Anthropic 原生 SSE 事件帧。
type StreamFrame struct {
	EventType string
	Data      json.RawMessage
}

// StreamMessageStop 标记整个 message 流结束；service 在收口时据此合成 message_stop 帧。
type StreamMessageStop struct {
	Type string `json:"type"`
}

// EventName 返回 SSE event 行的事件名，与 data.type 一致。
func (StreamMessageStop) EventName() string { return "message_stop" }
