package responses

import (
	"encoding/json"
	"strings"

	chatcompletionsadapter "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/chatcompletions"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/openai/responses/dto"
)

// responses_custom_tool.go 承载 custom 工具（Codex v0.147 的 apply_patch）在 responses→chat
// 桥接上的双向转换。
//
// Chat Completions 协议没有 custom 工具概念，只有参数为 JSON 的 function 工具。桥接把 custom
// 降级为单参数 function：freeform 文本装进 {"input": "..."}，lark 语法从可采样的硬约束退化为
// description 里的软约束。实测 chat-only 上游在该软约束下可稳定产出合法 patch，但软约束终归
// 可能被违反，故还原侧必须显式失败而非静默产出空 patch（客户会当成"改了但没生效"）。
//
// 转换共四向：
//  1. 出站 tools：custom → function（本文件 mapCustomToolToChat）
//  2. 回传 input items：custom_tool_call / custom_tool_call_output → assistant.tool_calls / tool
//  3. 入站非流式：function_call → custom_tool_call output item
//  4. 入站流式：function_call_arguments.* → custom_tool_call_input.*（见 responses_stream.go）

// customToolInputKey 是 custom 工具降级为 function 后承载 freeform 文本的唯一参数名。
const customToolInputKey = "input"

// customToolParametersSchema 是降级后的 function 参数 schema：单个必填字符串。
var customToolParametersSchema = json.RawMessage(
	`{"type":"object","properties":{"input":{"type":"string","description":"The freeform tool input document."}},"required":["input"],"additionalProperties":false}`,
)

// customToolFormat 是 custom 工具的 format 描述（grammar / text）。
type customToolFormat struct {
	Type       string `json:"type"`
	Syntax     string `json:"syntax"`
	Definition string `json:"definition"`
}

// mapCustomToolToChat 把 custom 工具降级为等价的单参数 function 工具。
func mapCustomToolToChat(tool dto.ResponsesTool) (name, description string, parameters json.RawMessage) {
	return tool.Name, customToolDescription(tool), customToolParametersSchema
}

// customToolDescription 把 custom 工具的 freeform 契约转述进 description。
//
// grammar 无法随 Chat 协议下发，只能作为自然语言约束附在描述里；同时明确要求不要再包一层
// JSON 或 markdown，否则模型容易把 patch 包进代码块导致下游解析失败。
func customToolDescription(tool dto.ResponsesTool) string {
	var b strings.Builder
	if desc := strings.TrimSpace(tool.Description); desc != "" {
		b.WriteString(desc)
		b.WriteString("\n\n")
	}
	b.WriteString(`Provide the tool payload as the "input" argument. It must be the raw freeform document itself, not wrapped in JSON, markdown fences, or quotes beyond the JSON string encoding.`)

	if format := parseCustomToolFormat(tool.Format); format != nil && strings.TrimSpace(format.Definition) != "" {
		b.WriteString("\n\nThe payload must conform to the following ")
		if syntax := strings.TrimSpace(format.Syntax); syntax != "" {
			b.WriteString(syntax)
			b.WriteString(" ")
		}
		b.WriteString("grammar:\n")
		b.WriteString(strings.TrimSpace(format.Definition))
	}
	return b.String()
}

// parseCustomToolFormat 解析 custom 工具的 format 对象；结构不符返回 nil（描述退化但不影响调用）。
func parseCustomToolFormat(raw json.RawMessage) *customToolFormat {
	if len(raw) == 0 {
		return nil
	}
	var format customToolFormat
	if err := json.Unmarshal(raw, &format); err != nil {
		return nil
	}
	return &format
}

// customToolNames 收集本次请求中声明为 custom 的工具名，供响应方向判定还原目标形态。
//
// namespace 内层不支持 custom（Codex 不会这样发），不递归。
func customToolNames(tools []dto.ResponsesTool) map[string]struct{} {
	var names map[string]struct{}
	for _, tool := range tools {
		if !tool.IsCustom() || tool.Name == "" {
			continue
		}
		if names == nil {
			names = make(map[string]struct{}, 1)
		}
		names[tool.Name] = struct{}{}
	}
	return names
}

// encodeCustomToolArguments 把 freeform 文本包装成降级 function 的 arguments JSON。
func encodeCustomToolArguments(input string) string {
	encoded, err := json.Marshal(map[string]string{customToolInputKey: input})
	if err != nil {
		// map[string]string 恒可序列化；兜底保证形状合法而非发出空串。
		return `{"` + customToolInputKey + `":""}`
	}
	return string(encoded)
}

// decodeCustomToolArguments 从降级 function 的 arguments 还原 freeform 文本。
//
// ok=false 表示上游未遵守软约束（arguments 非 JSON 对象、缺 input、或 input 非字符串）。
// 调用方必须把它作为显式失败处理：静默返回空 patch 会让客户端以为编辑成功。
func decodeCustomToolArguments(arguments string) (string, bool) {
	trimmed := strings.TrimSpace(arguments)
	if trimmed == "" {
		return "", false
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &decoded); err != nil {
		return "", false
	}
	raw, ok := decoded[customToolInputKey]
	if !ok {
		return "", false
	}
	var input string
	if err := json.Unmarshal(raw, &input); err != nil {
		return "", false
	}
	return input, true
}

// customToolInputFromItem 读取回传的 custom_tool_call.input 裸文本。
//
// 抓包实测 input 是 JSON 字符串；容忍非字符串形态时退回原始 JSON 文本，避免丢内容。
func customToolInputFromItem(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	return string(raw)
}

// customToolCallOutputItem 把降级 function 的上游调用还原为 custom_tool_call output item。
//
// 上游违反降级 schema 时不静默产出空 input——那会让客户端以为工具执行过。此时原样透出上游
// 文本（常见情形是模型直接吐了裸 payload 而未包 JSON，内容本身仍可用）并置 status=incomplete，
// 把契约偏差显式暴露给客户端。
func customToolCallOutputItem(call chatcompletionsadapter.ChatToolCall) dto.ResponseOutputItem {
	item := dto.ResponseOutputItem{
		Type:   itemTypeCustomToolCall,
		ID:     newResponsesID("ctc"),
		CallID: call.ID,
		Name:   call.Function.Name,
		Status: "completed",
	}
	if input, ok := decodeCustomToolArguments(call.Function.Arguments); ok {
		item.Input = input
		return item
	}
	item.Input = call.Function.Arguments
	item.Status = "incomplete"
	return item
}

// customToolCallStreamItem 把流式累积的降级 function 调用收口为 custom_tool_call item。
// 兜底口径与非流式一致：还原失败时原样透出并置 incomplete，不静默产出空 input。
func customToolCallStreamItem(tool *streamToolState) dto.ResponseOutputItem {
	item := dto.ResponseOutputItem{
		Type:   itemTypeCustomToolCall,
		ID:     tool.id,
		CallID: tool.callID,
		Name:   tool.name,
		Status: "completed",
	}
	if input, ok := decodeCustomToolArguments(tool.arguments); ok {
		item.Input = input
		return item
	}
	item.Input = tool.arguments
	item.Status = "incomplete"
	return item
}
