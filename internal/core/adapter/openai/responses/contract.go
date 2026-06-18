// Package responses 实现 OpenAI 协议族「Responses API 直传」adapter。
//
// 与 chat completions adapter（internal/core/adapter/openai）并列：当上游原生支持
// POST /responses（OpenAI 官方或 codex 中转）时，本 adapter 直连上游 /responses，
// 请求/响应/SSE 事件零结构转换透传，只在同一次解析里抽取协议无关的账务事实
// （adapter.ResponseFacts），交给共享 lifecycle 结算。chat-only 第三方（如 deepseek）
// 不注册本 adapter，gateway 自动落到既有 responses→chat 桥接（DEC-014）。
//
// 依赖纪律：本包只依赖 core/adapter、core/adapter/sse、core/channel、platform/failure，
// 不依赖任何 ingress（gatewayapi）或 service 包；ingress↔adapter 的请求/响应搬运由 service 编排层完成。
package responses

import (
	"context"
	"encoding/json"

	"github.com/ThankCat/unio-api/internal/core/adapter"
	"github.com/ThankCat/unio-api/internal/core/channel"
)

// ResponsesAdapter 定义 OpenAI 协议族 Responses 直传 adapter 的非流式上游调用能力。
type ResponsesAdapter interface {
	// CreateResponse 调用上游 POST /responses（非流式），返回原文响应体与同次解析的账务事实。
	CreateResponse(ctx context.Context, ch channel.Runtime, req Request) (*Response, error)
}

// StreamResponsesAdapter 定义 OpenAI 协议族 Responses 直传 adapter 的流式上游调用能力。
type StreamResponsesAdapter interface {
	// StreamResponse 调用上游 POST /responses（流式），逐 SSE 事件经 emit 透传给 gateway，
	// 并返回流式结束后的不可变账务事实（StreamOutcome.Facts，可能为 nil）。
	StreamResponse(ctx context.Context, ch channel.Runtime, req Request, emit func(StreamChunk) error) (adapter.StreamOutcome, error)
}

// ResponsesCompactAdapter 定义 OpenAI 协议族 Responses 原生压缩（POST /responses/compact）上游调用能力。
//
// 仅当上游原生支持 /responses/compact（OpenAI 官方 / Codex 中转）时注册；gateway 据是否注册分流
// NativeCompact（原文透传）vs SyntheticCompact（chat 摘要降级，DEC-014）。
type ResponsesCompactAdapter interface {
	// CompactResponse 调用上游 POST /responses/compact（非流式），返回压缩结果原文与同次解析的账务事实。
	// 上游不支持（404/405）或响应缺少可计费 usage 时返回 ErrCompactUnsupported，调用方据此回落 Synthetic。
	CompactResponse(ctx context.Context, ch channel.Runtime, req Request) (*Response, error)
}

// ResponsesInputTokenizer 定义某个 Responses 直传 provider adapter 对输入 token 的计数能力。
//
// 用于 authorization 阶段的保守预估；与协议响应解析无关。
type ResponsesInputTokenizer interface {
	// CountResponsesInputTokens 估算一次 Responses 请求的输入 token 数。
	CountResponsesInputTokens(req Request) (int64, error)
}

// Request 是 gateway 传给 Responses 直传 adapter 的内部请求 DTO。
//
// Body 是要 POST 给上游 /responses 的完整请求 JSON：model 已由 service 映射为 candidate
// 的 upstream model，stream 已按调用方式（CreateResponse=false / StreamResponse=true）置位。
// adapter 直传 Body，不做二次结构化改写（上游 responses 直传零转换）。
type Request struct {
	Body json.RawMessage
}

// Response 是 Responses 直传 adapter 返回给 gateway 的非流式响应 DTO。
//
// Raw 是上游响应体原文：service 透传给客户前只重写顶层 model 回显（零转换）。Facts 与 Raw
// 在同一次解析中产生，settlement / recovery / 审计只消费 Facts，不反向解析 Raw。
type Response struct {
	// Raw 是上游 /responses 非流式响应体原文。
	Raw json.RawMessage

	// ResponseID 是上游 response.id，作为 request_records 的响应 ID 与审计标识。
	ResponseID string

	// Model 是上游回显的 model（仅审计；客户可见 model 由 service 改写为客户请求名）。
	Model string

	// Usage 是本次响应的 token 用量（已从上游 usage 归一）。
	Usage adapter.ChatUsage

	// Upstream 是本次上游调用的可审计元信息（HTTP 状态码、上游 request id）。
	Upstream adapter.UpstreamMetadata

	// Facts 是本次响应的协议无关账务审计事实。
	Facts adapter.ResponseFacts
}

// StreamChunk 表示 Responses 直传 adapter 透传给 gateway 的单个上游 SSE 事件。
//
// EventType + Data 是上游事件原文（SSE `event:` 名 + `data:` JSON），service 透传给客户前只重写
// 嵌套 response.model。ResponseID/Usage/FinishReason 仅在 adapter 能从该事件（response.completed /
// response.incomplete 终态）解析出时填充，供共享流式循环维护 stream id / final usage / 终态。
type StreamChunk struct {
	// EventType 是 SSE 事件名（如 response.output_text.delta / response.completed）。
	EventType string

	// Data 是该事件 data 字段的原始 JSON 原文。
	Data json.RawMessage

	// ResponseID 是终态事件中解析到的 response.id；非终态事件为空。
	ResponseID string

	// Usage 仅在终态事件（completed/incomplete 且上游带 usage）解析到 usage 时设置；其余为 nil。
	Usage *adapter.ChatUsage

	// FinishReason 是终态事件解析到的稳定 finish 语义原始串（如 "stop"/"length"/"content_filter"）；
	// 非终态事件为空。
	FinishReason string
}
