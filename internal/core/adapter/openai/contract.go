package openai

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

	// Tools / ToolChoice / ResponseFormat 为 OpenAI 请求字段，由 adapter wire 透传 upstream。
	Tools             []ChatTool
	ToolChoice        json.RawMessage
	ParallelToolCalls *bool
	ResponseFormat    *ChatResponseFormat

	// Extensions 是 gateway 层保留的 vendor 扩展（如 thinking）。
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
type ChatResponse struct {
	ID               string
	Model            string
	Content          string
	ReasoningContent *string
	ToolCalls        []ChatToolCall
	FinishReason     string
	Usage            adapter.ChatUsage

	// Upstream 是本次上游成功调用的可审计元信息（HTTP 状态码、上游 request id）。
	// gateway 在结算时写入 request attempt，用于渠道审计和 observability。
	Upstream adapter.UpstreamMetadata

	// Facts 是本次响应的协议无关账务审计事实，与 Content/ToolCalls/Usage 在同一次解析中产生。
	// settlement、recovery 和审计后续只消费 Facts，不反向解析公开响应。
	Facts adapter.ResponseFacts
}

// ChatStreamChunk 表示 OpenAI 协议族 adapter 返回给 gateway 的一段聊天补全流式内容。
type ChatStreamChunk struct {
	ID               string
	Model            string
	Role             string
	Content          string
	ReasoningContent *string
	ToolCalls        json.RawMessage
	FinishReason     *string

	// Usage 只在 provider 返回 final usage stream chunk 时设置。
	// 普通内容 chunk 必须为 nil。
	Usage *adapter.ChatUsage

	// Upstream 是本次上游流式调用的可审计元信息。
	// 流式 adapter 在发出 final usage chunk 时一并附带；普通内容 chunk 必须为 nil。
	// gateway 用它在流式结算时写入真实 upstream status/request id。
	Upstream *adapter.UpstreamMetadata
}
