package openai

import "encoding/json"

type chatCompletionRequest struct {
	Model         string             `json:"model"`
	Messages      []chatMessage      `json:"messages"`
	Stream        bool               `json:"stream,omitempty"`
	StreamOptions *chatStreamOptions `json:"stream_options,omitempty"`

	Temperature      *float64 `json:"temperature,omitempty"`
	TopP             *float64 `json:"top_p,omitempty"`
	MaxTokens        *int     `json:"max_tokens,omitempty"`
	PresencePenalty  *float64 `json:"presence_penalty,omitempty"`
	FrequencyPenalty *float64 `json:"frequency_penalty,omitempty"`
	Stop             []string `json:"stop,omitempty"`
	User             *string  `json:"user,omitempty"`
	ReasoningEffort   *string              `json:"reasoning_effort,omitempty"`
	Tools             json.RawMessage      `json:"tools,omitempty"`
	ToolChoice        json.RawMessage      `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool                `json:"parallel_tool_calls,omitempty"`
	ResponseFormat    *chatResponseFormat  `json:"response_format,omitempty"`
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
}

type chatCompletionResponse struct {
	ID      string               `json:"id"`
	Model   string               `json:"model"`
	Choices []chatChoice         `json:"choices"`
	Usage   *chatCompletionUsage `json:"usage"`
}

type chatChoice struct {
	Message      chatMessage `json:"message"`
	FinishReason string      `json:"finish_reason,omitempty"`
}

type chatCompletionStreamResponse struct {
	ID      string               `json:"id"`
	Model   string               `json:"model"`
	Choices []chatStreamChoice   `json:"choices"`
	Usage   *chatCompletionUsage `json:"usage"`
}

type chatStreamChoice struct {
	Delta        chatStreamDelta `json:"delta"`
	FinishReason *string         `json:"finish_reason"`
}

type chatStreamDelta struct {
	Role             string          `json:"role"`
	Content          string          `json:"content"`
	ReasoningContent *string         `json:"reasoning_content"`
	ToolCalls        json.RawMessage `json:"tool_calls,omitempty"`
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
}

type chatCompletionTokensDetails struct {
	ReasoningTokens          int `json:"reasoning_tokens"`
	AcceptedPredictionTokens int `json:"accepted_prediction_tokens"`
	RejectedPredictionTokens int `json:"rejected_prediction_tokens"`
}
