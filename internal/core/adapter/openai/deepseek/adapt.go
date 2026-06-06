package deepseek

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/ThankCat/unio-api/internal/core/adapter/openai"
)

// legacyFunctionDef 是 deprecated functions[] 的元素形状（OpenAI legacy function calling）。
type legacyFunctionDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// deepseekReasoningEfforts 把 OpenAI/Codex 的 reasoning_effort 枚举归一为 DeepSeek 文档支持的 high/max。
//
// DeepSeek 官方枚举仅 high/max（thinking 模式下生效），并自带 low/medium→high、xhigh→max 的兼容映射；
// Codex（gpt-5 家族）还会发 minimal，不在 DeepSeek 文档枚举内。为让出站 wire 始终是 DeepSeek 文档合法值、
// 不依赖上游隐式兼容行为，也避免 minimal 触发上游 422，这里在 adapter 出站显式归一（DEEPSEEK_OPENAI_MAPPING §2）。
var deepseekReasoningEfforts = map[string]string{
	"minimal": "high",
	"low":     "high",
	"medium":  "high",
	"high":    "high",
	"xhigh":   "max",
	"max":     "max",
}

// normalizeReasoningEffort 归一 reasoning_effort 为 DeepSeek 支持值（大小写/空白不敏感）。
//
// 未知枚举返回 ok=false，由调用方 Drop（让 DeepSeek 回退默认 high），不把非法值发上游。
func normalizeReasoningEffort(effort string) (string, bool) {
	normalized, ok := deepseekReasoningEfforts[strings.ToLower(strings.TrimSpace(effort))]
	return normalized, ok
}

// adaptLegacyFunctions 把 deprecated functions / function_call Adapt 成现代 tools / tool_choice。
//
// 规则（DEEPSEEK_OPENAI_MAPPING.md §2；无法无损转换则 Drop，避免上游 400）：
//   - functions → tools（function 类型）。若请求已带 tools，无法无损合并 → Drop functions。
//   - function_call → tool_choice。若请求已带 tool_choice，无法无损合并 → Drop function_call。
//   - function_call 取值：字符串 none/auto 透传为同名 tool_choice；对象 {"name":X} →
//     {"type":"function","function":{"name":X}}；无法识别 → Drop function_call。
//
// 返回 Adapt/Drop 后的 req 与被 Drop 的字段名（Adapt 成功不计入 dropped）。
// req 为值传递，清空 legacy 字段对调用方无副作用；新 tools slice 为新分配。
func adaptLegacyFunctions(req openai.ChatRequest) (openai.ChatRequest, []string) {
	var dropped []string

	if len(req.Functions) > 0 {
		switch {
		case len(req.Tools) > 0:
			// 同时显式传了现代 tools，legacy functions 无法无损合并，Drop。
			dropped = append(dropped, "functions")
		default:
			if tools, ok := convertLegacyFunctions(req.Functions); ok {
				req.Tools = tools
			} else {
				dropped = append(dropped, "functions")
			}
		}
		req.Functions = nil
	}

	if len(req.FunctionCall) > 0 {
		switch {
		case len(req.ToolChoice) > 0:
			dropped = append(dropped, "function_call")
		default:
			if choice, ok := convertLegacyFunctionCall(req.FunctionCall); ok {
				req.ToolChoice = choice
			} else {
				dropped = append(dropped, "function_call")
			}
		}
		req.FunctionCall = nil
	}

	return req, dropped
}

// convertLegacyFunctions 把 legacy functions[] 转换为现代 function tools。
// 任一条目缺 name 视为无法无损转换，返回 ok=false 让调用方 Drop。
func convertLegacyFunctions(raw json.RawMessage) ([]openai.ChatTool, bool) {
	var defs []legacyFunctionDef
	if err := json.Unmarshal(raw, &defs); err != nil || len(defs) == 0 {
		return nil, false
	}

	tools := make([]openai.ChatTool, 0, len(defs))
	for _, def := range defs {
		if strings.TrimSpace(def.Name) == "" {
			return nil, false
		}
		tools = append(tools, openai.ChatTool{
			Type: "function",
			Function: openai.ChatFunctionTool{
				Name:        def.Name,
				Description: def.Description,
				Parameters:  def.Parameters,
			},
		})
	}

	return tools, true
}

// convertLegacyFunctionCall 把 legacy function_call 转换为现代 tool_choice 的原始 JSON。
//
// legacy 取值仅 "none" / "auto" / {"name": "..."}（OpenAI 规范）；其余无法识别 → ok=false。
func convertLegacyFunctionCall(raw json.RawMessage) (json.RawMessage, bool) {
	trimmed := bytes.TrimSpace(raw)

	var asString string
	if err := json.Unmarshal(trimmed, &asString); err == nil {
		switch asString {
		case "none", "auto":
			// tool_choice 接受同名字符串，直接透传。
			return append(json.RawMessage(nil), trimmed...), true
		default:
			return nil, false
		}
	}

	var named struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(trimmed, &named); err == nil && strings.TrimSpace(named.Name) != "" {
		choice := map[string]any{
			"type":     "function",
			"function": map[string]string{"name": named.Name},
		}
		if encoded, err := json.Marshal(choice); err == nil {
			return encoded, true
		}
	}

	return nil, false
}
