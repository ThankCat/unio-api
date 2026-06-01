package gatewayapi

import (
	"fmt"
	"strings"
)

// validateChatMessageContent 按 OpenAI parity 规则校验单条 message 内容边界。
func validateChatMessageContent(msg ChatMessage, index int) *chatValidationError {
	param := fmt.Sprintf("messages.%d.content", index)

	switch msg.Role {
	case "tool":
		if msg.ToolCallID == nil || strings.TrimSpace(*msg.ToolCallID) == "" {
			return &chatValidationError{
				param:   fmt.Sprintf("messages.%d.tool_call_id", index),
				message: "tool message requires tool_call_id",
			}
		}
		if len(msg.Content) == 0 {
			return &chatValidationError{param: param, message: "tool message content is required"}
		}
	case "assistant":
		if len(msg.ToolCalls) > 0 {
			return nil
		}
		if msg.ReasoningContent != nil && strings.TrimSpace(*msg.ReasoningContent) != "" {
			return nil
		}
		if strings.TrimSpace(msg.ContentString()) != "" {
			return nil
		}
		return &chatValidationError{
			param:   param,
			message: "assistant message requires content, reasoning_content, or tool_calls",
		}
	default:
		if strings.TrimSpace(msg.ContentString()) == "" {
			return &chatValidationError{param: param, message: "message content is required"}
		}
	}

	return nil
}

// isSupportedChatRole 判断 OpenAI parity 支持的 chat role（C1~C4）。
func isSupportedChatRole(role string) bool {
	switch role {
	case "system", "user", "assistant", "developer", "tool":
		return true
	default:
		return false
	}
}
