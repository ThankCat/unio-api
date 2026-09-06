package dto

import "encoding/json"

// Responses tool 类型常量。抓包 Codex v0.130 时 function / namespace 为主路径；
// v0.147 起 custom 承载 apply_patch（文件编辑的唯一手段），已升为主路径。
// local_shell / 内置工具（web_search 等）仍为兜底或当前不消费。
const (
	ToolTypeFunction  = "function"
	ToolTypeNamespace = "namespace"
	ToolTypeCustom    = "custom"
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

	// custom（grammar/text format）：format 原始 JSON。Codex v0.147 的 apply_patch 形如
	// {type:"custom", name:"apply_patch", format:{type:"grammar", syntax:"lark", definition:"…"}}，
	// 其调用参数是不经 JSON 包装的裸文本。
	Format json.RawMessage `json:"format,omitempty"`
}

// IsFunction 判断是否为 function 工具。
func (t ResponsesTool) IsFunction() bool { return t.Type == ToolTypeFunction }

// IsNamespace 判断是否为 Codex MCP namespace 分组工具。
func (t ResponsesTool) IsNamespace() bool { return t.Type == ToolTypeNamespace }

// IsCustom 判断是否为 custom（freeform 文本参数）工具。
func (t ResponsesTool) IsCustom() bool { return t.Type == ToolTypeCustom }
