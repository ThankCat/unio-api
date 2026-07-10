package chatcompletions

import "encoding/json"

type chatCompletionRequest struct {
	Model         string             `json:"model"`
	Messages      []chatMessage      `json:"messages"`
	Stream        bool               `json:"stream,omitempty"`
	StreamOptions *chatStreamOptions `json:"stream_options,omitempty"`

	Temperature         *float64            `json:"temperature,omitempty"`
	TopP                *float64            `json:"top_p,omitempty"`
	MaxTokens           *int                `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int                `json:"max_completion_tokens,omitempty"`
	PresencePenalty     *float64            `json:"presence_penalty,omitempty"`
	FrequencyPenalty    *float64            `json:"frequency_penalty,omitempty"`
	Stop                []string            `json:"stop,omitempty"`
	User                *string             `json:"user,omitempty"`
	ReasoningEffort     *string             `json:"reasoning_effort,omitempty"`
	Tools               json.RawMessage     `json:"tools,omitempty"`
	ToolChoice          json.RawMessage     `json:"tool_choice,omitempty"`
	ParallelToolCalls   *bool               `json:"parallel_tool_calls,omitempty"`
	ResponseFormat      *chatResponseFormat `json:"response_format,omitempty"`

	N                    *int            `json:"n,omitempty"`
	Seed                 *int            `json:"seed,omitempty"`
	Logprobs             *bool           `json:"logprobs,omitempty"`
	TopLogprobs          *int            `json:"top_logprobs,omitempty"`
	LogitBias            json.RawMessage `json:"logit_bias,omitempty"`
	Modalities           []string        `json:"modalities,omitempty"`
	Audio                json.RawMessage `json:"audio,omitempty"`
	Prediction           json.RawMessage `json:"prediction,omitempty"`
	Metadata             json.RawMessage `json:"metadata,omitempty"`
	WebSearchOptions     json.RawMessage `json:"web_search_options,omitempty"`
	Store                *bool           `json:"store,omitempty"`
	ServiceTier          *string         `json:"service_tier,omitempty"`
	Verbosity            *string         `json:"verbosity,omitempty"`
	PromptCacheKey       *string         `json:"prompt_cache_key,omitempty"`
	PromptCacheRetention *string         `json:"prompt_cache_retention,omitempty"`
	SafetyIdentifier     *string         `json:"safety_identifier,omitempty"`
	FunctionCall         json.RawMessage `json:"function_call,omitempty"`
	Functions            json.RawMessage `json:"functions,omitempty"`
}

type chatResponseFormat struct {
	Type       string          `json:"type"`
	JSONSchema json.RawMessage `json:"json_schema,omitempty"`
}

// chatStreamOptions 表示 OpenAI stream_options 请求参数。
type chatStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type chatMessage struct {
	Role             string          `json:"role"`
	Content          json.RawMessage `json:"content,omitempty"`
	ReasoningContent *string         `json:"reasoning_content,omitempty"`
	ToolCallID       *string         `json:"tool_call_id,omitempty"`
	ToolCalls        json.RawMessage `json:"tool_calls,omitempty"`

	// 以下为响应侧 assistant message 字段（请求编码不设置，omitempty 不发送）。
	// Refusal 为模型拒答文本；Annotations / Audio 结构复杂，保留上游原始 JSON 透传。
	Refusal     *string         `json:"refusal,omitempty"`
	Annotations json.RawMessage `json:"annotations,omitempty"`
	Audio       json.RawMessage `json:"audio,omitempty"`
}

type chatCompletionResponse struct {
	ID                string               `json:"id"`
	Object            string               `json:"object"`
	Created           int64                `json:"created"`
	Model             string               `json:"model"`
	Choices           []chatChoice         `json:"choices"`
	Usage             *chatCompletionUsage `json:"usage"`
	ServiceTier       *string              `json:"service_tier,omitempty"`
	SystemFingerprint *string              `json:"system_fingerprint,omitempty"`
}

type chatChoice struct {
	Index        int             `json:"index"`
	Message      chatMessage     `json:"message"`
	FinishReason string          `json:"finish_reason,omitempty"`
	Logprobs     json.RawMessage `json:"logprobs,omitempty"`
}

type chatCompletionStreamResponse struct {
	ID                string               `json:"id"`
	Object            string               `json:"object"`
	Created           int64                `json:"created"`
	Model             string               `json:"model"`
	Choices           []chatStreamChoice   `json:"choices"`
	Usage             *chatCompletionUsage `json:"usage"`
	ServiceTier       *string              `json:"service_tier,omitempty"`
	SystemFingerprint *string              `json:"system_fingerprint,omitempty"`
}

type chatStreamChoice struct {
	Index        int             `json:"index"`
	Delta        chatStreamDelta `json:"delta"`
	FinishReason *string         `json:"finish_reason"`
	Logprobs     json.RawMessage `json:"logprobs,omitempty"`
}

type chatStreamDelta struct {
	Role             string          `json:"role"`
	Content          string          `json:"content"`
	ReasoningContent *string         `json:"reasoning_content"`
	ToolCalls        json.RawMessage `json:"tool_calls,omitempty"`
	Refusal          *string         `json:"refusal,omitempty"`
	FunctionCall     json.RawMessage `json:"function_call,omitempty"`
}

type chatCompletionUsage struct {
	PromptTokens            *int                        `json:"prompt_tokens"`
	CompletionTokens        *int                        `json:"completion_tokens"`
	TotalTokens             *int                        `json:"total_tokens"`
	PromptTokensDetails     chatPromptTokensDetails     `json:"prompt_tokens_details"`
	CompletionTokensDetails chatCompletionTokensDetails `json:"completion_tokens_details"`
}

type chatPromptTokensDetails struct {
	CachedTokens int `json:"cached_tokens"`
	// CacheWriteTokens 是 GPT-5.6+ 起 prompt_tokens_details 中「写入缓存」的 token 数（按 1.25x 计费）。
	CacheWriteTokens int `json:"cache_write_tokens"`
	// CacheCreationTokens 是 sub2api 等兼容上游的别名字段（等价 cache_write_tokens）。
	CacheCreationTokens int `json:"cache_creation_tokens"`
}

// CacheWrite 返回缓存写入 token，按别名优先级兜底（cache_write_tokens > cache_creation_tokens）。
func (d chatPromptTokensDetails) CacheWrite() int {
	if d.CacheWriteTokens > 0 {
		return d.CacheWriteTokens
	}
	if d.CacheCreationTokens > 0 {
		return d.CacheCreationTokens
	}
	return 0
}

type chatCompletionTokensDetails struct {
	ReasoningTokens          int `json:"reasoning_tokens"`
	AcceptedPredictionTokens int `json:"accepted_prediction_tokens"`
	RejectedPredictionTokens int `json:"rejected_prediction_tokens"`
}
