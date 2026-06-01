package messages

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// knownThinkingTypes 是 thinking union 已登记的 type 值（见 matrix §5）。
var knownThinkingTypes = map[string]bool{
	"enabled":  true,
	"disabled": true,
	"adaptive": true,
}

// knownToolChoiceTypes 是 tool_choice union 已登记的 type 值（见 matrix §7）。
var knownToolChoiceTypes = map[string]bool{
	"auto": true,
	"any":  true,
	"tool": true,
	"none": true,
}

// knownServerToolTypes 是 tools union 中已登记的内置（server）tool type（见 matrix §8）。
// 客户 custom tool 不带这些 type，由 name + input_schema 识别。
var knownServerToolTypes = map[string]bool{
	"bash_20250124":                   true,
	"code_execution_20250522":         true,
	"code_execution_20250825":         true,
	"code_execution_20260120":         true,
	"memory_20250818":                 true,
	"text_editor_20250124":            true,
	"text_editor_20250429":            true,
	"text_editor_20250728":            true,
	"web_search_20250305":             true,
	"web_search_20260209":             true,
	"web_fetch_20250910":              true,
	"web_fetch_20260209":              true,
	"web_fetch_20260309":              true,
	"tool_search_tool_bm25_20251119":  true,
	"tool_search_tool_regex_20251119": true,
}

// validateSystem 校验顶层 system union：string 或 text block 数组。
//
// system 不允许塞 messages system role 代替；这里只做结构与 block 类型识别，
// provider 能力级处理由 adapter 负责。
func validateSystem(raw json.RawMessage) *messageValidationError {
	const param = "system"

	data := bytes.TrimSpace(raw)
	if len(data) == 0 {
		return nil
	}

	switch data[0] {
	case '"':
		var text string
		if err := json.Unmarshal(data, &text); err != nil {
			return &messageValidationError{param: param, message: "system string is malformed"}
		}
		return nil

	case '[':
		var blocks []json.RawMessage
		if err := json.Unmarshal(data, &blocks); err != nil {
			return &messageValidationError{param: param, message: "system array is malformed"}
		}
		for i, block := range blocks {
			blockParam := fmt.Sprintf("system.%d", i)
			var head struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(block, &head); err != nil {
				return &messageValidationError{param: blockParam, message: "system block must be an object"}
			}
			if head.Type != "text" {
				return &messageValidationError{param: blockParam + ".type", message: "system blocks must be text blocks"}
			}
		}
		return nil

	default:
		return &messageValidationError{param: param, message: "system must be a string or an array of text blocks"}
	}
}

// validateThinking 校验 thinking union 的 type 枚举。
func validateThinking(raw json.RawMessage) *messageValidationError {
	const param = "thinking"

	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}

	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return &messageValidationError{param: param, message: "thinking must be an object"}
	}
	if head.Type == "" {
		return &messageValidationError{param: param + ".type", message: "thinking type is required"}
	}
	if !knownThinkingTypes[head.Type] {
		return &messageValidationError{
			param:   param + ".type",
			message: fmt.Sprintf("unsupported thinking type %q", head.Type),
		}
	}
	return nil
}

// validateToolChoice 校验 tool_choice union 的 type 枚举与 tool 的 name。
func validateToolChoice(raw json.RawMessage) *messageValidationError {
	const param = "tool_choice"

	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return &messageValidationError{param: param, message: "tool_choice must be an object"}
	}

	var choiceType string
	if rawType, ok := fields["type"]; ok {
		if err := json.Unmarshal(rawType, &choiceType); err != nil {
			return &messageValidationError{param: param + ".type", message: "tool_choice type must be a string"}
		}
	}
	if choiceType == "" {
		return &messageValidationError{param: param + ".type", message: "tool_choice type is required"}
	}
	if !knownToolChoiceTypes[choiceType] {
		return &messageValidationError{
			param:   param + ".type",
			message: fmt.Sprintf("unsupported tool_choice type %q", choiceType),
		}
	}
	if choiceType == "tool" {
		if _, ok := fields["name"]; !ok {
			return &messageValidationError{param: param + ".name", message: "tool_choice type \"tool\" requires name"}
		}
	}
	return nil
}

// validateTools 校验 tools union：数组中每个元素是 custom tool（name + input_schema）
// 或已登记的内置 tool type。未登记 type 直接 Reject，避免静默吞掉。
func validateTools(raw json.RawMessage) *messageValidationError {
	const param = "tools"

	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}

	var tools []json.RawMessage
	if err := json.Unmarshal(raw, &tools); err != nil {
		return &messageValidationError{param: param, message: "tools must be an array"}
	}

	for i, tool := range tools {
		toolParam := fmt.Sprintf("tools.%d", i)

		var fields map[string]json.RawMessage
		if err := json.Unmarshal(tool, &fields); err != nil {
			return &messageValidationError{param: toolParam, message: "tool must be an object"}
		}

		var toolType string
		if rawType, ok := fields["type"]; ok {
			if err := json.Unmarshal(rawType, &toolType); err != nil {
				return &messageValidationError{param: toolParam + ".type", message: "tool type must be a string"}
			}
		}

		switch {
		case toolType == "" || toolType == "custom":
			// 客户 custom tool：必须有 name 与 input_schema。
			if verr := requireBlockFields(toolParam, fields, "name", "input_schema"); verr != nil {
				return verr
			}
		case knownServerToolTypes[toolType]:
			// 已登记内置 tool：结构放行，provider 能力级 Reject 由 adapter 处理。
		default:
			return &messageValidationError{
				param:   toolParam + ".type",
				message: fmt.Sprintf("unsupported tool type %q", toolType),
			}
		}
	}
	return nil
}
