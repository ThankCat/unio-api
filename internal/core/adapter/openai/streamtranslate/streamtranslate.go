// Package streamtranslate 提供 OpenAI-compatible 上游流式响应翻译（stream response translation）。
package streamtranslate

import (
	"encoding/json"

	"github.com/ThankCat/unio-api/internal/core/adapter"
)

// Key 与 providers.slug 对应；Default 表示无厂商 override 时的基线行为。
type Key string

// StreamEvent 是单个 SSE JSON event 解析后的中间表示。
// 仍在 openai adapter 内部使用，不暴露给 gateway。
type StreamEvent struct {
	ID               string
	Model            string
	Role             string
	Content          string
	ReasoningContent string
	ToolCalls        json.RawMessage
	FinishReason     *string
	Usage            *adapter.ChatUsage
}

// StreamTranslator 消化 OpenAI-compatible 协议内、不同厂商的流式差异。
type StreamTranslator interface {
	// Key 返回 translator 绑定的 provider slug；Default 返回 DefaultKey。
	Key() Key
	// TranslateStreamEvent 把一个上游 SSE event 转成 0..N 个可 emit 的内部事件。
	TranslateStreamEvent(in StreamInput) ([]StreamEvent, error)
}

// StreamInput 是 stream translator 看到的单个 SSE event 输入。
type StreamInput struct {
	ID      string
	Model   string
	Choices []StreamChoice
	Usage   *adapter.ChatUsage
}

// StreamChoice 是 stream translator 看到的单个 choice delta。
type StreamChoice struct {
	Role             string
	Content          string
	ReasoningContent string
	ToolCalls        json.RawMessage
	FinishReason     *string
}
