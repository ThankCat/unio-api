package chatcompletions

import (
	"context"
	"encoding/json"

	"github.com/ThankCat/unio-api/internal/core/adapter"
	"github.com/ThankCat/unio-api/internal/core/channel"
)

// ChatAdapter 定义 OpenAI 协议族聊天补全 adapter 需要提供的协议转换和上游调用能力。
type ChatAdapter interface {
	ChatCompletions(ctx context.Context, ch channel.Runtime, req ChatRequest) (*ChatResponse, error)
}

// StreamChatAdapter 定义 OpenAI 协议族聊天补全流式 adapter 能力。
type StreamChatAdapter interface {
	StreamChatCompletions(ctx context.Context, ch channel.Runtime, req ChatRequest, emit func(ChatStreamChunk) error) (adapter.StreamOutcome, error)
}

// ChatRequest 是 gateway 传给 OpenAI 协议族聊天补全 adapter 的内部请求 DTO。
type ChatRequest struct {
	Model    string
	Messages []ChatMessage

	// Temperature 控制输出随机性；nil 表示调用方没有传该参数。
	Temperature *float64

	// TopP 控制 nucleus sampling；nil 表示调用方没有传该参数。
	TopP *float64

	// MaxTokens 控制最大输出 token 数；nil 表示调用方没有传该参数。
	MaxTokens *int

	MaxCompletionTokens *int

	// PresencePenalty 降低重复主题倾向；nil 表示调用方没有传该参数。
	PresencePenalty *float64

	// FrequencyPenalty 降低重复词语倾向；nil 表示调用方没有传该参数。
	FrequencyPenalty *float64

	// Stop 是停止序列；nil 表示调用方没有传该参数。
	Stop []string

	// User 是终端用户标识，用于上游审计或风控；nil 表示调用方没有传该参数。
	User *string

	// ReasoningEffort 是 reasoning 模型推理强度。
	ReasoningEffort *string

	// ReasoningDisabled 是「客户显式未请求 reasoning」的内部意图标志，由 Responses 桥接设置
	// （Responses reasoning 缺省/null）。它不是 OpenAI wire 字段、不会被序列化进 upstream body；
	// provider adapter 据此关闭该 provider 的私有思考模式（如 DeepSeek 出站注入 thinking:disabled）。
	// chat completions ingress 不设置本字段（保持 provider 默认行为，避免回归 DeepSeek 默认开启思考）。
	ReasoningDisabled bool

	// Tools / ToolChoice / ResponseFormat 为 OpenAI 请求字段，由 adapter wire 透传 upstream。
	Tools             []ChatTool
	ToolChoice        json.RawMessage
	ParallelToolCalls *bool
	ResponseFormat    *ChatResponseFormat

	// 以下为 OpenAI Chat Completions 协议族的其余顶层字段。它们都是 OpenAI 规范字段（非
	// vendor 扩展），统一进入协议族契约；具体 provider 不支持时由该 provider adapter 出站
	// Drop（见 DEEPSEEK_OPENAI_MAPPING.md），未来支持的 provider 可直接 Pass 进 wire。

	// N 是返回候选数量。
	N *int

	// Seed 是 best-effort 确定性采样种子。
	Seed *int

	// Logprobs 控制是否返回 token 对数概率；TopLogprobs 是每位置候选数。
	Logprobs    *bool
	TopLogprobs *int

	// LogitBias 是 token ID → bias 映射的原始 JSON。
	LogitBias json.RawMessage

	// Modalities 是输出模态列表。
	Modalities []string

	// Audio / Prediction / Metadata / WebSearchOptions 为复杂对象，保留原始 JSON。
	Audio            json.RawMessage
	Prediction       json.RawMessage
	Metadata         json.RawMessage
	WebSearchOptions json.RawMessage

	// Store 是否存储输出。
	Store *bool

	// ServiceTier / Verbosity / PromptCacheKey / PromptCacheRetention / SafetyIdentifier 为标量控制字段。
	ServiceTier          *string
	Verbosity            *string
	PromptCacheKey       *string
	PromptCacheRetention *string
	SafetyIdentifier     *string

	// FunctionCall / Functions 为 deprecated legacy function 字段，保留原始 JSON，
	// 由 provider adapter 决定 Adapt 为 tool_choice/tools 还是 Drop。
	FunctionCall json.RawMessage
	Functions    json.RawMessage

	// Extensions 是 gateway 层保留的非 OpenAI 规范 vendor 扩展（如 thinking）。
	Extensions map[string]json.RawMessage
}

// ChatMessage 表示 OpenAI 协议族 adapter 层的单条聊天消息。
type ChatMessage struct {
	Role             string
	Content          json.RawMessage
	ReasoningContent *string
	ToolCallID       *string
	ToolCalls        []ChatToolCall
}

// ContentString 从 Content 提取纯文本；仅当 content 为 JSON string 时返回文本。
func (m ChatMessage) ContentString() string {
	if len(m.Content) == 0 {
		return ""
	}

	var text string
	if err := json.Unmarshal(m.Content, &text); err == nil {
		return text
	}

	return ""
}

// ChatResponse 是 OpenAI 协议族聊天补全 adapter 返回给 gateway 的内部响应 DTO。
//
// 当前按单候选（choice[0]）扁平化建模：DeepSeek 等 provider 只返回 1 个候选，n>1 已在出站 Drop。
// Refusal/Annotations/Audio/Logprobs 对应 choice[0] 的 message/choice 字段；复杂嵌套结构保留
// 上游原始 JSON 透传，保证 OpenAI 客户端按协议拿到字段，不做有损重组（DEC-012 协议为先）。
type ChatResponse struct {
	ID               string
	Model            string
	Content          string
	ReasoningContent *string
	ToolCalls        []ChatToolCall
	FinishReason     string
	Usage            adapter.ChatUsage

	// Created 是上游响应创建的 Unix 秒时间戳；OpenAI 客户端会读取该字段。
	Created int64

	// ServiceTier / SystemFingerprint 为顶层响应元信息；provider 未返回时为 nil。
	ServiceTier       *string
	SystemFingerprint *string

	// Refusal 是 assistant 拒答文本（choice[0].message.refusal）；nil 表示上游未返回。
	Refusal *string

	// Annotations / Audio 为 choice[0].message 的复杂对象，保留上游原始 JSON 透传。
	Annotations json.RawMessage
	Audio       json.RawMessage

	// Logprobs 为 choice[0].logprobs 的原始 JSON（content/refusal 对数概率），透传。
	Logprobs json.RawMessage

	// Upstream 是本次上游成功调用的可审计元信息（HTTP 状态码、上游 request id）。
	// gateway 在结算时写入 request attempt，用于渠道审计和 observability。
	Upstream adapter.UpstreamMetadata

	// Facts 是本次响应的协议无关账务审计事实，与 Content/ToolCalls/Usage 在同一次解析中产生。
	// settlement、recovery 和审计后续只消费 Facts，不反向解析公开响应。
	Facts adapter.ResponseFacts
}

// ChatStreamChunk 表示 OpenAI 协议族 adapter 返回给 gateway 的一段聊天补全流式内容。
//
// 按单候选扁平化（与非流式 ChatResponse 一致）：Index/Logprobs 对应 choice[0]，
// Refusal/FunctionCall 对应 choice[0].delta；复杂嵌套（logprobs/function_call）透传上游原始 JSON。
type ChatStreamChunk struct {
	ID               string
	Model            string
	Role             string
	Content          string
	ReasoningContent *string
	ToolCalls        json.RawMessage
	FinishReason     *string

	// Created 是上游 chunk 的创建时间戳（Unix 秒）；0 表示上游未在该 chunk 给出。
	Created int64

	// ServiceTier / SystemFingerprint 为 chunk 顶层元信息；provider 未返回时为 nil。
	ServiceTier       *string
	SystemFingerprint *string

	// Index 是 choice 序号；Logprobs 是该 choice 的对数概率原始 JSON。
	Index    int
	Logprobs json.RawMessage

	// Refusal 是 delta 拒答增量；FunctionCall 是 deprecated legacy function 调用增量原始 JSON。
	Refusal      *string
	FunctionCall json.RawMessage

	// Usage 只在 provider 返回 final usage stream chunk 时设置。
	// 普通内容 chunk 必须为 nil。
	Usage *adapter.ChatUsage

	// Upstream 是本次上游流式调用的可审计元信息。
	// 流式 adapter 在发出 final usage chunk 时一并附带；普通内容 chunk 必须为 nil。
	// gateway 用它在流式结算时写入真实 upstream status/request id。
	Upstream *adapter.UpstreamMetadata
}
