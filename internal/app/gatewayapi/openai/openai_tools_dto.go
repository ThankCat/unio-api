package openai

import "encoding/json"

// ChatCompletionTool 表示 OpenAI tools[] 中的单个 tool 定义。
type ChatCompletionTool struct {
	Type     string                     `json:"type"`
	Function ChatCompletionFunctionTool `json:"function"`
}

// ChatCompletionFunctionTool 是 function 类型 tool 的 function 字段。
type ChatCompletionFunctionTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}

// ChatCompletionToolCall 表示 assistant message 或 stream delta 中的 tool_calls 元素。
type ChatCompletionToolCall struct {
	ID       string                         `json:"id"`
	Type     string                         `json:"type"`
	Function ChatCompletionToolCallFunction `json:"function"`
}

// ChatCompletionToolCallFunction 是 tool call 的 function 载荷。
type ChatCompletionToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ChatCompletionResponseFormat 表示 response_format 请求字段。
type ChatCompletionResponseFormat struct {
	Type       string          `json:"type"`
	JSONSchema json.RawMessage `json:"json_schema,omitempty"`
}
