package messages

import (
	"fmt"
	"strings"
)

// messageValidationError 表示 Anthropic Messages 请求校验失败后的用户可见错误。
type messageValidationError struct {
	param   string
	message string
}

// validateMessageRequest 校验 Anthropic Messages 请求的顶层 HTTP DTO 边界。
//
// 这里只做协议级结构校验（必填、枚举、范围）；content block union、tools、thinking 等复杂结构的
// 结构化校验在后续小步补充，provider 能力级 Reject 由 adapter 在调上游前处理。
func validateMessageRequest(req MessageRequest) *messageValidationError {
	if strings.TrimSpace(req.Model) == "" {
		return &messageValidationError{param: "model", message: "model is required"}
	}

	if req.MaxTokens == nil {
		return &messageValidationError{param: "max_tokens", message: "max_tokens is required"}
	}
	if *req.MaxTokens < 0 {
		return &messageValidationError{param: "max_tokens", message: "max_tokens must be greater than or equal to 0"}
	}

	if len(req.Messages) == 0 {
		return &messageValidationError{param: "messages", message: "messages is required"}
	}

	for i, msg := range req.Messages {
		roleParam := fmt.Sprintf("messages.%d.role", i)
		if strings.TrimSpace(msg.Role) == "" {
			return &messageValidationError{param: roleParam, message: "message role is required"}
		}
		if !isSupportedMessageRole(msg.Role) {
			return &messageValidationError{
				param:   roleParam,
				message: "message role must be one of user, assistant, system",
			}
		}
		if len(msg.Content) == 0 {
			return &messageValidationError{
				param:   fmt.Sprintf("messages.%d.content", i),
				message: "message content is required",
			}
		}
		if verr := validateMessageContent(i, msg.Content); verr != nil {
			return verr
		}
	}

	// Anthropic 文档定义 temperature/top_p 范围为 [0,1]。
	if req.Temperature != nil && (*req.Temperature < 0 || *req.Temperature > 1) {
		return &messageValidationError{param: "temperature", message: "temperature must be between 0 and 1"}
	}
	if req.TopP != nil && (*req.TopP < 0 || *req.TopP > 1) {
		return &messageValidationError{param: "top_p", message: "top_p must be between 0 and 1"}
	}
	if req.TopK != nil && *req.TopK < 0 {
		return &messageValidationError{param: "top_k", message: "top_k must be greater than or equal to 0"}
	}

	if verr := validateSystem(req.System); verr != nil {
		return verr
	}
	if verr := validateThinking(req.Thinking); verr != nil {
		return verr
	}
	if verr := validateToolChoice(req.ToolChoice); verr != nil {
		return verr
	}
	if verr := validateTools(req.Tools); verr != nil {
		return verr
	}

	return nil
}

// isSupportedMessageRole 判断 Anthropic Messages 支持的消息 role。
func isSupportedMessageRole(role string) bool {
	switch role {
	case "user", "assistant", "system":
		return true
	default:
		return false
	}
}
