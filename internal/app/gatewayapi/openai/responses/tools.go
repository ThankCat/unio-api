package responses

import "encoding/json"

// Responses tool 类型常量。真实抓包（Codex v0.130）确认 function / namespace 为主路径，
// custom / local_shell / 内置工具为兜底或本阶段不消费（见 RESPONSES_CHAT_BRIDGE.md §3.1）。
const (
	toolTypeFunction  = "function"
	toolTypeNamespace = "namespace"
)

// ResponsesTool 表示 Responses tools[] 中的单个工具定义（按 type 区分的 union）。
//
// 与 Chat Completions 的嵌套形态不同，Responses function 工具是扁平形态：
// {type:"function", name, description, parameters, strict}。MCP 工具用
// {type:"namespace", name:"mcp__xxx__", tools:[function...]} 分组（OpenAI 规范无此类型，
// 为 Codex 客户端特有）。
type ResponsesTool struct {
	Type string `json:"type"`

	// function（扁平形态）/ namespace 名称。
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`

	// namespace（Codex MCP 分组）：内层是 function 工具，translation 拍平到 Chat 顶层 tools。
	Tools []ResponsesTool `json:"tools,omitempty"`

	// custom（grammar/text format）：format 原始 JSON；v0.130 未出现，保留兜底。
	Format json.RawMessage `json:"format,omitempty"`
}

// IsFunction 判断是否为 function 工具。
func (t ResponsesTool) IsFunction() bool { return t.Type == toolTypeFunction }

// IsNamespace 判断是否为 Codex MCP namespace 分组工具。
func (t ResponsesTool) IsNamespace() bool { return t.Type == toolTypeNamespace }
