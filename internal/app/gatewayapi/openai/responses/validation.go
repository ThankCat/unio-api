package responses

import (
	"fmt"
	"strings"
)

const (
	// maxUserLength 是 user / safety_identifier 字段的保守长度上限。
	maxUserLength = 512
)

// input item 判别类型（RESPONSES_CHAT_BRIDGE.md §2）。
const (
	itemTypeMessage            = "message"
	itemTypeFunctionCall       = "function_call"
	itemTypeFunctionCallOutput = "function_call_output"
	itemTypeReasoning          = "reasoning"
	itemTypeItemReference      = "item_reference"
	itemTypeCompaction         = "compaction"
)

// validateResponsesRequest 校验 Responses 请求的 HTTP DTO 协议结构边界。
//
// 只做协议合法性校验（DEC-012「协议为先」）：非法结构返回 Responses 原生 400；
// 合法但 provider 无法转换的字段不在此 Reject，留给 adapter 出站 Drop。
func validateResponsesRequest(req ResponsesRequest) *responsesValidationError {
	if strings.TrimSpace(req.Model) == "" {
		return &responsesValidationError{param: "model", message: "model is required"}
	}

	// background:true 是异步任务模式；Unio 无状态承诺下不支持，明确 400 拒绝（不静默转同步）。
	if req.Background != nil && *req.Background {
		return &responsesValidationError{
			code:    errorCodeUnsupportedBackground,
			param:   "background",
			message: "background mode is not supported; responses are synchronous only",
		}
	}

	if validationErr := validateResponsesInput(req.Input); validationErr != nil {
		return validationErr
	}

	if req.MaxOutputTokens != nil && (!req.MaxOutputTokens.Integral() || req.MaxOutputTokens.Int() <= 0) {
		return &responsesValidationError{param: "max_output_tokens", message: "max_output_tokens must be greater than 0"}
	}

	if req.Temperature != nil && (*req.Temperature < 0 || *req.Temperature > 2) {
		return &responsesValidationError{param: "temperature", message: "temperature must be between 0 and 2"}
	}

	if req.TopP != nil && (*req.TopP < 0 || *req.TopP > 1) {
		return &responsesValidationError{param: "top_p", message: "top_p must be between 0 and 1"}
	}

	if req.User != nil && len(*req.User) > maxUserLength {
		return &responsesValidationError{param: "user", message: "user must be at most 512 characters"}
	}

	if req.SafetyIdentifier != nil && len(*req.SafetyIdentifier) > maxUserLength {
		return &responsesValidationError{param: "safety_identifier", message: "safety_identifier must be at most 512 characters"}
	}

	for i, tool := range req.Tools {
		if validationErr := validateResponsesTool(tool, fmt.Sprintf("tools.%d", i)); validationErr != nil {
			return validationErr
		}
	}

	return nil
}

// validateResponsesInput 校验 `input` union：字符串非空或 item 数组合法。
func validateResponsesInput(input ResponsesInput) *responsesValidationError {
	if input.Text == nil && len(input.Items) == 0 {
		return &responsesValidationError{
			param:   "input",
			message: "input is required and must be a string or an array of input items",
		}
	}

	if input.Text != nil {
		if strings.TrimSpace(*input.Text) == "" {
			return &responsesValidationError{param: "input", message: "input must not be empty"}
		}
		return nil
	}

	for i, item := range input.Items {
		if validationErr := validateInputItem(item, i); validationErr != nil {
			return validationErr
		}
	}

	return nil
}

// validateInputItem 校验单个 input item 的协议结构。
//
// 已知类型校验其必填字段；未知类型放行（translation 决定 Drop/Reject），避免 ingress 因
// 不认识的（可能是未来新增的）合法 item 类型而 Reject。
func validateInputItem(item ResponseInputItem, index int) *responsesValidationError {
	param := fmt.Sprintf("input.%d", index)

	// type 缺省按 OpenAI 语义视为 message。
	itemType := item.Type
	if itemType == "" {
		itemType = itemTypeMessage
	}

	switch itemType {
	case itemTypeMessage:
		return validateMessageItem(item, param)
	case itemTypeFunctionCall:
		if item.CallID == nil || strings.TrimSpace(*item.CallID) == "" {
			return &responsesValidationError{param: param + ".call_id", message: "function_call requires call_id"}
		}
		if item.Name == nil || strings.TrimSpace(*item.Name) == "" {
			return &responsesValidationError{param: param + ".name", message: "function_call requires name"}
		}
		return nil
	case itemTypeFunctionCallOutput:
		if item.CallID == nil || strings.TrimSpace(*item.CallID) == "" {
			return &responsesValidationError{param: param + ".call_id", message: "function_call_output requires call_id"}
		}
		if len(item.Output) == 0 {
			return &responsesValidationError{param: param + ".output", message: "function_call_output requires output"}
		}
		return nil
	case itemTypeItemReference:
		if item.ID == nil || strings.TrimSpace(*item.ID) == "" {
			return &responsesValidationError{param: param + ".id", message: "item_reference requires id"}
		}
		return nil
	case itemTypeReasoning, itemTypeCompaction:
		// 结构宽松：reasoning / compaction 是回传的历史 item，字段可选；translation 还原。
		return nil
	default:
		// 未知 item 类型放行（DEC-012）：translation 按桥接矩阵 Drop/Reject 并记审计。
		return nil
	}
}

// validateMessageItem 校验 message item 的 role 与 content。
func validateMessageItem(item ResponseInputItem, param string) *responsesValidationError {
	roleParam := param + ".role"
	if strings.TrimSpace(item.Role) == "" {
		return &responsesValidationError{param: roleParam, message: "message role is required"}
	}
	if !isSupportedInputRole(item.Role) {
		return &responsesValidationError{
			param:   roleParam,
			message: "message role must be one of system, developer, user, assistant",
		}
	}

	state, contentErr := validateInputContent(item.Content, param+".content")
	if contentErr != nil {
		return contentErr
	}
	if !state.hasContent {
		return &responsesValidationError{param: param + ".content", message: "message content is required"}
	}

	return nil
}

// isSupportedInputRole 判断 Responses input message 支持的 role。
func isSupportedInputRole(role string) bool {
	switch role {
	case "system", "developer", "user", "assistant":
		return true
	default:
		return false
	}
}

// validateResponsesTool 校验单个 tool 定义的协议结构。
func validateResponsesTool(tool ResponsesTool, param string) *responsesValidationError {
	if strings.TrimSpace(tool.Type) == "" {
		return &responsesValidationError{param: param + ".type", message: "tool requires type"}
	}

	switch tool.Type {
	case toolTypeFunction:
		if strings.TrimSpace(tool.Name) == "" {
			return &responsesValidationError{param: param + ".name", message: "function tool requires name"}
		}
		return nil
	case toolTypeNamespace:
		if strings.TrimSpace(tool.Name) == "" {
			return &responsesValidationError{param: param + ".name", message: "namespace tool requires name"}
		}
		for i, nested := range tool.Tools {
			if validationErr := validateResponsesTool(nested, fmt.Sprintf("%s.tools.%d", param, i)); validationErr != nil {
				return validationErr
			}
		}
		return nil
	default:
		// custom / local_shell / 内置工具：合法协议结构，ingress 放行，translation 按矩阵处理。
		return nil
	}
}
