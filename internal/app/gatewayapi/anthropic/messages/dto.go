// Package messages 实现 Anthropic Messages Create（POST /v1/messages）的公开协议族 DTO、
// decode、校验与（后续）handler/stream。
//
// 它与 OpenAI Chat Completions 是两个独立协议族，不共享公开 DTO：Anthropic 返回原生
// Message 结构与 named SSE event，不转换成 OpenAI 形状。复杂 union（system、content block、
// tools、tool_choice、thinking、output_config）当前先以 json.RawMessage 承载并在后续小步
// 结构化校验，保证未建模字段不被 silent drop（保留原始 JSON）。
package messages

import "encoding/json"

// MessageRequest 表示 Anthropic Messages Create 请求体。
type MessageRequest struct {
	// Model 是客户请求的 Unio catalog model；adapter 使用 routing 后的 upstream model。
	Model string `json:"model"`

	// Messages 是多轮对话消息列表。
	Messages []Message `json:"messages"`

	// MaxTokens 是最大输出 token；Anthropic 必填，协议允许 0（cache warm 场景）。
	MaxTokens *int `json:"max_tokens,omitempty"`

	// System 是顶层 system prompt：string 或 text block 数组（原始 JSON 透传，后续结构化校验）。
	System json.RawMessage `json:"system,omitempty"`

	// Metadata 是请求元信息（包含 user_id）。
	Metadata json.RawMessage `json:"metadata,omitempty"`

	// StopSequences 是停止序列。
	StopSequences []string `json:"stop_sequences,omitempty"`

	// Stream 表示是否启用流式响应。
	Stream *bool `json:"stream,omitempty"`

	// Temperature / TopK / TopP 是采样参数。
	Temperature *float64 `json:"temperature,omitempty"`
	TopK        *int     `json:"top_k,omitempty"`
	TopP        *float64 `json:"top_p,omitempty"`

	// Thinking 是 thinking 配置 union（enabled/disabled/adaptive）。
	Thinking json.RawMessage `json:"thinking,omitempty"`

	// ToolChoice / Tools 是工具相关 union。
	ToolChoice json.RawMessage `json:"tool_choice,omitempty"`
	Tools      json.RawMessage `json:"tools,omitempty"`

	// OutputConfig 是 effort 与 structured output format 配置。
	OutputConfig json.RawMessage `json:"output_config,omitempty"`

	// ServiceTier 是服务等级（auto / standard_only）。
	ServiceTier *string `json:"service_tier,omitempty"`

	// Container 是 code execution container ID。
	Container *string `json:"container,omitempty"`

	// InferenceGeo 是推理区域偏好。
	InferenceGeo *string `json:"inference_geo,omitempty"`

	// Extensions 保留未显式建模的顶层 JSON 字段；由 UnmarshalJSON 填充，禁止 silent drop。
	Extensions map[string]json.RawMessage `json:"-"`
}

// Message 表示 Anthropic 单条消息；Content 为 string 或 content block 数组（原始 JSON）。
type Message struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// IsStream 判断是否请求流式响应。
func (req *MessageRequest) IsStream() bool {
	return req.Stream != nil && *req.Stream
}
