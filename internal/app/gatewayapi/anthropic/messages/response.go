package messages

import "encoding/json"

// MessageResponse 是 Anthropic Messages Create 的非流式响应体。
//
// 客户收到原生 Anthropic Message，不收到 OpenAI choices。content block 由 adapter 在解析
// 上游响应时构造为原始 JSON（后续小步可按 block 类型 typed 化）；usage 强类型，供账务事实消费。
type MessageResponse struct {
	ID           string            `json:"id"`
	Type         string            `json:"type"`
	Role         string            `json:"role"`
	Model        string            `json:"model"`
	Content      []json.RawMessage `json:"content"`
	StopReason   *string           `json:"stop_reason"`
	StopSequence *string           `json:"stop_sequence"`
	Usage        MessageUsage      `json:"usage"`
	Container    json.RawMessage   `json:"container,omitempty"`
}

// MessageUsage 是 Anthropic Messages 的 usage 结构。
//
// 各 cache / output / server tool 维度用指针表达"上游未提供"与"已知为 0"的区别；
// 统一账务事实映射见 RESPONSE_FACTS.md。
type MessageUsage struct {
	InputTokens              int                  `json:"input_tokens"`
	CacheCreationInputTokens *int                 `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     *int                 `json:"cache_read_input_tokens,omitempty"`
	CacheCreation            *CacheCreation       `json:"cache_creation,omitempty"`
	OutputTokens             int                  `json:"output_tokens"`
	OutputTokensDetails      *OutputTokensDetails `json:"output_tokens_details,omitempty"`
	ServerToolUse            *ServerToolUse       `json:"server_tool_use,omitempty"`
	ServiceTier              *string              `json:"service_tier,omitempty"`
	InferenceGeo             *string              `json:"inference_geo,omitempty"`
}

// CacheCreation 是 cache 写入的 TTL 分解。
type CacheCreation struct {
	Ephemeral5mInputTokens *int `json:"ephemeral_5m_input_tokens,omitempty"`
	Ephemeral1hInputTokens *int `json:"ephemeral_1h_input_tokens,omitempty"`
}

// OutputTokensDetails 是 output token 的分解项。
type OutputTokensDetails struct {
	ThinkingTokens *int `json:"thinking_tokens,omitempty"`
}

// ServerToolUse 是服务端工具调用次数计量。
type ServerToolUse struct {
	WebSearchRequests *int `json:"web_search_requests,omitempty"`
	WebFetchRequests  *int `json:"web_fetch_requests,omitempty"`
}
