package gatewayapi

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
}

// ChatCompletionStreamOptions 表示 OpenAI-compatible stream_options 请求参数。
type ChatCompletionStreamOptions struct {
	// IncludeUsage 为 true 时，成功结束的流式响应会在 [DONE] 前追加一条 usage chunk。
	IncludeUsage *bool `json:"include_usage,omitempty"`
}

// StreamIncludeUsage 判断客户端是否请求在流式响应中返回 usage。
func (req ChatCompletionRequest) StreamIncludeUsage() bool {
	return req.StreamOptions != nil &&
		req.StreamOptions.IncludeUsage != nil &&
		*req.StreamOptions.IncludeUsage
}

// ChatMessage 表示 chat completions 请求或响应中的一条消息。
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatCompletionResponse 表示 OpenAI-compatible chat completions 响应体。
type ChatCompletionResponse struct {
	ID      string                 `json:"id"`
	Object  string                 `json:"object"`
	Created int64                  `json:"created"`
	Model   string                 `json:"model"`
	Choices []ChatCompletionChoice `json:"choices"`
	Usage   ChatCompletionUsage    `json:"usage"`
}

// ChatCompletionChoice 表示 chat completions 响应中的一个候选结果。
type ChatCompletionChoice struct {
	Index        int         `json:"index"`
	Message      ChatMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

// ChatCompletionUsage 表示 chat completions 请求的 token 用量统计。
type ChatCompletionUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ChatCompletionStreamResponse 表示 OpenAI-compatible chat completions 流式响应中的一条 chunk。
type ChatCompletionStreamResponse struct {
	ID      string                       `json:"id"`
	Object  string                       `json:"object"`
	Created int64                        `json:"created"`
	Model   string                       `json:"model"`
	Choices []ChatCompletionStreamChoice `json:"choices"`
	Usage   *ChatCompletionUsage         `json:"usage,omitempty"`
}

// ChatCompletionStreamChoice 表示流式响应中的一个候选增量。
type ChatCompletionStreamChoice struct {
	Index        int                       `json:"index"`
	Delta        ChatCompletionStreamDelta `json:"delta"`
	FinishReason *string                   `json:"finish_reason"`
}

// ChatCompletionStreamDelta 表示流式响应里本次增量返回的消息内容。
type ChatCompletionStreamDelta struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}
