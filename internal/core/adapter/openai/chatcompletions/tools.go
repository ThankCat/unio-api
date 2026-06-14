package chatcompletions

import "encoding/json"

// ChatTool 表示 OpenAI tools[] 中的单个 tool 定义。
type ChatTool struct {
	Type     string           `json:"type"`
	Function ChatFunctionTool `json:"function"`
}

// ChatFunctionTool 是 function 类型 tool 的 function 字段。
type ChatFunctionTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}

// ChatToolCall 表示 assistant message 上的 tool_calls 元素。
type ChatToolCall struct {
	ID       string               `json:"id"`
	Type     string               `json:"type"`
	Function ChatToolCallFunction `json:"function"`
}

// ChatToolCallFunction 是 tool call 的 function 载荷。
type ChatToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ChatResponseFormat 表示 response_format 请求字段。
type ChatResponseFormat struct {
	Type       string          `json:"type"`
	JSONSchema json.RawMessage `json:"json_schema,omitempty"`
}
