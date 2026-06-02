package messages

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// knownContentBlockTypes 是 Anthropic Messages 请求侧已登记的 content block 类型集合。
//
// 这里只做协议族结构识别：已登记类型放行（结构由后续按需细化），未登记类型在 ingress
// 直接 400，禁止 silent drop。provider 能力级 Reject（例如 DeepSeek 不支持多模态）由 adapter
// 在调上游前处理，不在 ingress 拍板。集合对齐 ANTHROPIC_MESSAGES_MATRIX.md 的 content block 表。
var knownContentBlockTypes = map[string]bool{
	"text":                                   true,
	"image":                                  true,
	"document":                               true,
	"search_result":                          true,
	"thinking":                               true,
	"redacted_thinking":                      true,
	"tool_use":                               true,
	"tool_result":                            true,
	"server_tool_use":                        true,
	"web_search_tool_result":                 true,
	"web_fetch_tool_result":                  true,
	"code_execution_tool_result":             true,
	"bash_code_execution_tool_result":        true,
	"text_editor_code_execution_tool_result": true,
	"tool_search_tool_result":                true,
	"container_upload":                       true,
	"mid_conversation_system":                true,
}

// validateMessageContent 校验单条 message 的 content union 结构。
//
// content 支持两种形态：string shorthand（等价单个 text block）或 content block 数组。
// 数组中每个 block 必须是带已登记 type 的对象；空字符串与空数组都按缺失内容拒绝。
func validateMessageContent(msgIndex int, raw json.RawMessage) *messageValidationError {
	param := fmt.Sprintf("messages.%d.content", msgIndex)

	data := bytes.TrimSpace(raw)
	if len(data) == 0 {
		return &messageValidationError{param: param, message: "message content is required"}
	}

	switch data[0] {
	case '"':
		var text string
		if err := json.Unmarshal(data, &text); err != nil {
			return &messageValidationError{param: param, message: "message content string is malformed"}
		}
		if strings.TrimSpace(text) == "" {
			return &messageValidationError{param: param, message: "message content must not be empty"}
		}
		return nil

	case '[':
		var blocks []json.RawMessage
		if err := json.Unmarshal(data, &blocks); err != nil {
			return &messageValidationError{param: param, message: "message content array is malformed"}
		}
		if len(blocks) == 0 {
			return &messageValidationError{param: param, message: "message content must not be empty"}
		}
		for i, block := range blocks {
			if verr := validateContentBlock(msgIndex, i, block); verr != nil {
				return verr
			}
		}
		return nil

	default:
		return &messageValidationError{
			param:   param,
			message: "message content must be a string or an array of content blocks",
		}
	}
}

// validateContentBlock 校验单个 content block 的类型与核心必填字段。
func validateContentBlock(msgIndex, blockIndex int, raw json.RawMessage) *messageValidationError {
	param := fmt.Sprintf("messages.%d.content.%d", msgIndex, blockIndex)

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return &messageValidationError{param: param, message: "content block must be an object"}
	}

	var blockType string
	if rawType, ok := fields["type"]; ok {
		if err := json.Unmarshal(rawType, &blockType); err != nil {
			return &messageValidationError{param: param + ".type", message: "content block type must be a string"}
		}
	}
	if blockType == "" {
		return &messageValidationError{param: param + ".type", message: "content block type is required"}
	}
	if !knownContentBlockTypes[blockType] {
		return &messageValidationError{
			param:   param + ".type",
			message: fmt.Sprintf("unsupported content block type %q", blockType),
		}
	}

	// 对核心 block 做最小必填字段检查；其余字段结构在后续按需细化。
	switch blockType {
	case "text":
		return requireBlockFields(param, fields, "text")
	case "tool_use":
		return requireBlockFields(param, fields, "id", "name")
	case "tool_result":
		return requireBlockFields(param, fields, "tool_use_id")
	default:
		return nil
	}
}

// requireBlockFields 校验 content block 中给定键存在，缺失返回可定位的 400。
func requireBlockFields(param string, fields map[string]json.RawMessage, names ...string) *messageValidationError {
	for _, name := range names {
		if _, ok := fields[name]; !ok {
			return &messageValidationError{
				param:   param + "." + name,
				message: fmt.Sprintf("content block %q is missing required field %q", param, name),
			}
		}
	}
	return nil
}
