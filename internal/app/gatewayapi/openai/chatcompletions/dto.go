package chatcompletions

import "encoding/json"

// ChatCompletionRequest 表示 OpenAI-compatible chat completions 请求体。
type ChatCompletionRequest struct {
	Model string `json:"model"`

	// 聊天消息列表
	Messages []ChatMessage `json:"messages"`

	// 是否启用流式响应。流式响应指服务端逐段返回内容，而不是一次性返回完整结果。
	Stream *bool `json:"stream,omitempty"`

	// StreamOptions 是流式响应选项；当前仅支持 include_usage。
	StreamOptions *ChatCompletionStreamOptions `json:"stream_options,omitempty"`

	// 采样温度，控制输出随机性。
	Temperature *float64 `json:"temperature,omitempty"`

	// 核采样参数，控制候选 token 的概率范围。可以理解为另一种控制随机性的参数。
	TopP *float64 `json:"top_p,omitempty"`

	// 最大输出 token 数。token 是模型处理文本的基本单位，可以粗略理解为词片段。
	MaxTokens *int `json:"max_tokens,omitempty"`

	// 存在惩罚，用来降低模型重复谈论已经出现过主题的倾向。
	PresencePenalty *float64 `json:"presence_penalty,omitempty"`

	// 频率惩罚，用来降低模型重复使用已经出现过词语的倾向。
	FrequencyPenalty *float64 `json:"frequency_penalty,omitempty"`

	// 停止序列。模型生成内容遇到这些字符串时停止。
	Stop []string `json:"stop,omitempty"`

	// 终端用户标识。一般用于审计、风控或上游服务追踪。
	User *string `json:"user,omitempty"`

	// MaxCompletionTokens 是 OpenAI 新版最大输出 token（含 reasoning）。
	MaxCompletionTokens *int `json:"max_completion_tokens,omitempty"`

	// ReasoningEffort 是 reasoning 模型推理强度（如 low/medium/high）。
	ReasoningEffort *string `json:"reasoning_effort,omitempty"`

	// Tools / ToolChoice / ResponseFormat 为 OpenAI 请求字段。
	Tools             []ChatCompletionTool          `json:"tools,omitempty"`
	ToolChoice        json.RawMessage               `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool                         `json:"parallel_tool_calls,omitempty"`
	ResponseFormat    *ChatCompletionResponseFormat `json:"response_format,omitempty"`

	// N 是返回的候选数量；OpenAI 默认 1。
	N *int `json:"n,omitempty"`

	// Seed 是 best-effort 确定性采样种子。
	Seed *int `json:"seed,omitempty"`

	// Logprobs 控制是否返回 token 对数概率。
	Logprobs *bool `json:"logprobs,omitempty"`

	// TopLogprobs 是每个位置返回的候选 token 数（0~20），依赖 logprobs=true。
	TopLogprobs *int `json:"top_logprobs,omitempty"`

	// LogitBias 是 token ID → bias（-100~100）映射；结构复杂，保留原始 JSON。
	LogitBias json.RawMessage `json:"logit_bias,omitempty"`

	// Modalities 是输出模态列表，如 ["text"]、["text","audio"]。
	Modalities []string `json:"modalities,omitempty"`

	// Audio 是输出音频配置（format/voice）；保留原始 JSON，nested 校验后续小步。
	Audio json.RawMessage `json:"audio,omitempty"`

	// Prediction 是 Predicted Outputs 配置；保留原始 JSON。
	Prediction json.RawMessage `json:"prediction,omitempty"`

	// Metadata 是公开协议 metadata（不等于内部 observability metadata）；保留原始 JSON。
	Metadata json.RawMessage `json:"metadata,omitempty"`

	// Store 是否存储输出供后续检索。
	Store *bool `json:"store,omitempty"`

	// ServiceTier 是服务等级（如 auto/default/flex）。
	ServiceTier *string `json:"service_tier,omitempty"`

	// Verbosity 是输出详细度（如 low/medium/high）。
	Verbosity *string `json:"verbosity,omitempty"`

	// PromptCacheKey 是 OpenAI prompt cache 路由键。
	PromptCacheKey *string `json:"prompt_cache_key,omitempty"`

	// PromptCacheRetention 是 OpenAI prompt cache 保留策略。
	PromptCacheRetention *string `json:"prompt_cache_retention,omitempty"`

	// SafetyIdentifier 是安全标识；不等于 user。
	SafetyIdentifier *string `json:"safety_identifier,omitempty"`

	// WebSearchOptions 是 Chat Completions web search 配置；保留原始 JSON。
	WebSearchOptions json.RawMessage `json:"web_search_options,omitempty"`

	// FunctionCall 是 deprecated legacy function 调用控制（none/auto/{name} union）；保留原始 JSON。
	FunctionCall json.RawMessage `json:"function_call,omitempty"`

	// Functions 是 deprecated legacy function 列表；保留原始 JSON。
	Functions json.RawMessage `json:"functions,omitempty"`

	// Extensions 保留未显式建模的顶层 JSON 字段；由 UnmarshalJSON 填充。
	Extensions map[string]json.RawMessage `json:"-"`
}

// ChatCompletionStreamOptions 表示 OpenAI-compatible stream_options 请求参数。
type ChatCompletionStreamOptions struct {
	// IncludeUsage 为 true 时，成功结束的流式响应会在 [DONE] 前追加一条 usage chunk。
	IncludeUsage *bool `json:"include_usage,omitempty"`

	// IncludeObfuscation 控制是否返回 obfuscation 字段；建模以避免 ingress silent drop，
	// provider 不支持时由 adapter 出站 Drop（见 DEEPSEEK_OPENAI_MAPPING.md §4）。
	IncludeObfuscation *bool `json:"include_obfuscation,omitempty"`
}

// StreamIncludeUsage 判断客户端是否请求在流式响应中返回 usage。
func (req *ChatCompletionRequest) StreamIncludeUsage() bool {
	return req.StreamOptions != nil &&
		req.StreamOptions.IncludeUsage != nil &&
		*req.StreamOptions.IncludeUsage
}

// ChatMessage 表示 chat completions 请求或响应中的一条消息。
type ChatMessage struct {
	Role string `json:"role"`
	// Content 是 OpenAI content 原样 JSON（string 或 array）。
	Content json.RawMessage `json:"content,omitempty"`
	// ReasoningContent 是 reasoning 模型思考过程；assistant 多轮历史需回传 upstream。
	ReasoningContent *string `json:"reasoning_content,omitempty"`
	// ToolCallID 是 tool role 消息关联的 call id。
	ToolCallID *string `json:"tool_call_id,omitempty"`
	// ToolCalls 是 assistant 消息上的 tool_calls 列表。
	ToolCalls []ChatCompletionToolCall `json:"tool_calls,omitempty"`

	// 以下为响应侧 assistant message 字段（请求不设置，omitempty 不输出）。
	// Refusal 为模型拒答文本；Annotations / Audio 结构复杂，透传上游原始 JSON。
	Refusal     *string         `json:"refusal,omitempty"`
	Annotations json.RawMessage `json:"annotations,omitempty"`
	Audio       json.RawMessage `json:"audio,omitempty"`
}

// ChatCompletionResponse 表示 OpenAI-compatible chat completions 响应体。
type ChatCompletionResponse struct {
	ID      string                 `json:"id"`
	Object  string                 `json:"object"`
	Created int64                  `json:"created"`
	Model   string                 `json:"model"`
	Choices []ChatCompletionChoice `json:"choices"`
	Usage   ChatCompletionUsage    `json:"usage"`

	// ServiceTier / SystemFingerprint 为顶层响应元信息；上游未返回时省略。
	ServiceTier       *string `json:"service_tier,omitempty"`
	SystemFingerprint *string `json:"system_fingerprint,omitempty"`
}

// ChatCompletionChoice 表示 chat completions 响应中的一个候选结果。
type ChatCompletionChoice struct {
	Index        int         `json:"index"`
	Message      ChatMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`

	// Logprobs 是该候选的 token 对数概率（content/refusal）；结构复杂，透传上游原始 JSON。
	Logprobs json.RawMessage `json:"logprobs,omitempty"`
}

// ChatCompletionUsage 表示 chat completions 请求的 token 用量统计。
type ChatCompletionUsage struct {
	PromptTokens            int                              `json:"prompt_tokens"`
	CompletionTokens        int                              `json:"completion_tokens"`
	TotalTokens             int                              `json:"total_tokens"`
	PromptTokensDetails     *ChatCompletionPromptDetails     `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *ChatCompletionCompletionDetails `json:"completion_tokens_details,omitempty"`
}

// ChatCompletionPromptDetails 是 OpenAI prompt_tokens_details。
type ChatCompletionPromptDetails struct {
	CachedTokens int `json:"cached_tokens,omitempty"`
	AudioTokens  int `json:"audio_tokens,omitempty"`
}

// ChatCompletionCompletionDetails 是 OpenAI completion_tokens_details。
type ChatCompletionCompletionDetails struct {
	ReasoningTokens          int `json:"reasoning_tokens,omitempty"`
	AcceptedPredictionTokens int `json:"accepted_prediction_tokens,omitempty"`
	RejectedPredictionTokens int `json:"rejected_prediction_tokens,omitempty"`
	AudioTokens              int `json:"audio_tokens,omitempty"`
}

// ChatCompletionStreamResponse 表示 OpenAI-compatible chat completions 流式响应中的一条 chunk。
type ChatCompletionStreamResponse struct {
	ID      string                       `json:"id"`
	Object  string                       `json:"object"`
	Created int64                        `json:"created"`
	Model   string                       `json:"model"`
	Choices []ChatCompletionStreamChoice `json:"choices"`
	Usage   *ChatCompletionUsage         `json:"usage,omitempty"`

	// ServiceTier / SystemFingerprint 为 chunk 顶层元信息；上游未返回时省略。
	ServiceTier       *string `json:"service_tier,omitempty"`
	SystemFingerprint *string `json:"system_fingerprint,omitempty"`

	// EmitUsageAsNull 为 true 时写出 "usage": null（见 MarshalJSON）。
	EmitUsageAsNull bool `json:"-"`
}

// ChatCompletionStreamChoice 表示流式响应中的一个候选增量。
type ChatCompletionStreamChoice struct {
	Index        int                       `json:"index"`
	Delta        ChatCompletionStreamDelta `json:"delta"`
	FinishReason *string                   `json:"finish_reason"`

	// Logprobs 是该候选增量的 token 对数概率；结构复杂，透传上游原始 JSON。
	Logprobs json.RawMessage `json:"logprobs,omitempty"`
}

// ChatCompletionStreamDelta 表示流式响应里本次增量返回的消息内容。
type ChatCompletionStreamDelta struct {
	Role             string          `json:"role,omitempty"`
	Content          string          `json:"content,omitempty"`
	ReasoningContent *string         `json:"reasoning_content,omitempty"`
	ToolCalls        json.RawMessage `json:"tool_calls,omitempty"`

	// Refusal 为拒答增量；FunctionCall 为 deprecated legacy function 调用增量原始 JSON。
	Refusal      *string         `json:"refusal,omitempty"`
	FunctionCall json.RawMessage `json:"function_call,omitempty"`
}

// ContentString 从 Content 提取纯文本；string 时直接返回，array 时返回空（9.17 再完善估算）。
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
