// Package messages 定义 Anthropic Messages 协议族的 adapter 契约。
//
// 它与 openai 协议族（internal/core/adapter/openai）平行：维护各自的内部请求/响应 DTO、
// usage 形状、facts 映射与 tokenizer 契约，不共享公开 DTO，也不把两套 wire framing 揉成一套。
// 具体 provider（如 DeepSeek 的 Anthropic endpoint）在 internal/core/adapter/anthropic/<provider>
// 下实现差异。协议无关的 UpstreamMetadata、UpstreamError、ResponseFacts、usage.Facts 仍复用
// adapter 根包与 usage 包。
package messages

import (
	"context"
	"encoding/json"

	"github.com/ThankCat/unio-api/internal/core/adapter"
	"github.com/ThankCat/unio-api/internal/core/channel"
)

// MessagesAdapter 定义 Anthropic 协议族非流式 Messages adapter 的协议转换与上游调用能力。
type MessagesAdapter interface {
	Messages(ctx context.Context, ch channel.Runtime, req MessageRequest) (*MessageResponse, error)
}

// StreamMessagesAdapter 定义 Anthropic 协议族流式 Messages adapter 能力。
//
// adapter 解析上游 SSE 后，按 Anthropic 原生事件重新编码并通过 emit 逐个回调。
// 终态 usage 事件（message_delta）必须在对应 MessageStreamEvent 上携带 Usage 与 Upstream，
// 供 lifecycle 结算与审计；其余事件这两个字段必须为 nil。上游 message_stop 由 adapter
// 截留，lifecycle durable closeout 完成后再由 gatewayapi 写出客户可见成功终态。
type StreamMessagesAdapter interface {
	StreamMessages(ctx context.Context, ch channel.Runtime, req MessageRequest, emit func(MessageStreamEvent) error) (adapter.StreamOutcome, error)
}

// MessageRequest 是 gateway 传给 Anthropic 协议族 adapter 的内部请求 DTO。
//
// 复杂 union（system、content block、tools、tool_choice、thinking）以 json.RawMessage 承载，
// 由各 provider adapter 在 request map 阶段按自身 wire 规则转换或 Reject；禁止 silent drop：
// gateway 未显式建模但客户传入的字段进入 Extensions，由 adapter 决定 forward 或 Reject。
type MessageRequest struct {
	// Model 是 routing 选中的 upstream model（不是客户 catalog model）。
	Model string

	// System 是顶层 system prompt：string 或 text block 数组的原始 JSON。
	System json.RawMessage

	// Messages 是多轮对话消息。
	Messages []Message

	// MaxTokens 是最大输出 token；Anthropic 必填，nil 表示调用方未传（ingress 已校验）。
	MaxTokens *int

	// StopSequences 是停止序列。
	StopSequences []string

	// Temperature / TopP / TopK 是采样参数；nil 表示未传。
	Temperature *float64
	TopP        *float64
	TopK        *int

	// Thinking / ToolChoice / Tools 是复杂 union 的原始 JSON。
	Thinking   json.RawMessage
	ToolChoice json.RawMessage
	Tools      json.RawMessage

	// Metadata 是请求元信息（含 user_id）原始 JSON。
	Metadata json.RawMessage

	// Stream 表示是否流式。
	Stream bool

	// Extensions 保留 gateway 未显式建模、但客户传入的顶层字段，禁止 silent drop。
	Extensions map[string]json.RawMessage

	// AnthropicBeta 是 ingress 解析的 anthropic-beta 头 token（DEC-013 宽进）。
	// DeepSeek adapter 出站 Drop；官方 1P adapter 按白名单 Pass 到 upstream anthropic-beta。
	AnthropicBeta []string
}

// Message 是 Anthropic adapter 层的单条消息；Content 为 string 或 content block 数组的原始 JSON。
type Message struct {
	Role    string
	Content json.RawMessage
}

// MessageResponse 是 Anthropic 协议族非流式 adapter 返回给 gateway 的内部响应 DTO。
//
// Content 为 adapter 在解析上游响应时忠实构造的 Anthropic content block 原始 JSON（text、
// thinking、tool_use 等）；Model 为 upstream model，gateway 在写客户响应时恢复 catalog model。
type MessageResponse struct {
	ID           string
	Model        string
	Role         string
	Content      []json.RawMessage
	StopReason   *string
	StopSequence *string
	Usage        MessageUsage

	// Upstream 是本次上游成功调用的可审计元信息。
	Upstream adapter.UpstreamMetadata

	// Facts 是与响应在同一次解析中产生的协议无关账务审计事实。
	Facts adapter.ResponseFacts
}

// MessageStreamEvent 是 Anthropic 协议族流式 adapter 回调给 gateway 的一个具名事件。
//
// Type 是 Anthropic SSE 事件名（message_start、content_block_start、content_block_delta、
// content_block_stop、message_delta、ping、error）；Data 是该事件已按 Anthropic 语义构造的
// JSON payload。gatewayapi/anthropic 负责把它编码为 SSE 帧写给客户。message_stop 不通过
// 本回调透出，由 lifecycle durable closeout 后单独写出。
type MessageStreamEvent struct {
	Type string
	Data json.RawMessage

	// Usage 仅在终态 usage 事件（message_delta）上设置，是流式结算的最终用量事实来源；
	// 其余事件必须为 nil。
	Usage *MessageUsage

	// Upstream 仅在携带 Usage 的事件上设置，记录真实上游 status/request id。
	Upstream *adapter.UpstreamMetadata
}
