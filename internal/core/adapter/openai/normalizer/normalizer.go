// Package normalizer 是 Phase 9 之前的过渡实现：OpenAI-compatible 上游流式响应翻译。
//
// TODO(阶段9/production): [GAP-9-003] 本包将吸收进 TASK-9.07 stream response translate；
// 不再单独维护 Normalizer 架构定义；DeepSeek reasoning 需改回 OpenAI 双字段输出。
package normalizer

import "github.com/ThankCat/unio-api/internal/core/adapter"

// Key 与 providers.slug 对应；Default 表示无厂商 override 时的基线行为。
type Key string

// StreamEvent 是单个 SSE JSON event 解析后的中间表示。
// 仍在 openai adapter 内部使用，不暴露给 gateway。
type StreamEvent struct {
	ID           string
	Model        string
	Role         string
	Content      string
	FinishReason *string
	Usage        *adapter.ChatUsage
}

// Normalizer 消化 OpenAI-compatible 协议内、不同厂商的细微差异。
type Normalizer interface {
	// Key 返回 normalizer 绑定的 provider slug；Default 返回 返回 DefaultKey。
	Key() Key
	// NormalizeStreamEvent 把一个上游 SSE event 转成 0..N 个可 emit 的内部事件。
	// skip 时返回 nil slice 即可。
	NormalizeStreamEvent(in StreamInput) ([]StreamEvent, error)
}

// StreamInput 是 Normalizer 看到的单个 SSE event 输入。
type StreamInput struct {
	ID      string
	Model   string
	Choices []StreamChoice
	Usage   *adapter.ChatUsage // 已由 adapter 层从 openai usage DTO 转换；nil 表示无 usage
}
type StreamChoice struct {
	Role             string
	Content          string
	ReasoningContent string // 扩展字段，厂商 normalizer 可使用
	FinishReason     *string
}
