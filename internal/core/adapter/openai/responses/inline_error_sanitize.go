package responses

import (
	"encoding/json"
	"regexp"
	"strings"
)

// P2-10：Responses 流式会把上游 response.failed / error 事件「内联」透传给客户端（为兼容 Codex SDK
// 据此映射 ApiError）。上游错误 message 可能含 base_url、内部 request id 等基础设施细节，直接透传是信息泄露。
// 这里在透传前把错误事件重建为「最小且脱敏」的同形状信封：保留 Codex 所需的 type / error.code / message，
// 但对 message 去 URL、压缩空白并截断，剥离上游细节。chat / anthropic 的客户端错误本就是固定脱敏文案，
// 本改动让 Responses 与之对齐。

// inlineErrorURLPattern 匹配 http(s) URL（可能含上游 base_url）。
var inlineErrorURLPattern = regexp.MustCompile(`https?://\S+`)

const maxInlineErrorMessageLen = 300

// sanitizeInlineErrorMessage 脱敏内联透传给客户端的上游错误消息。
func sanitizeInlineErrorMessage(message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return "upstream provider error"
	}
	message = inlineErrorURLPattern.ReplaceAllString(message, "[redacted]")
	// 压缩所有连续空白（含换行）为单空格，避免多行/控制字符。
	message = strings.Join(strings.Fields(message), " ")
	if message == "" {
		return "upstream provider error"
	}
	if len(message) > maxInlineErrorMessageLen {
		message = strings.TrimSpace(message[:maxInlineErrorMessageLen]) + "…"
	}
	return message
}

type sanitizedFailedError struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message"`
}

type sanitizedFailedResponse struct {
	ID    string               `json:"id,omitempty"`
	Error sanitizedFailedError `json:"error"`
}

type sanitizedFailedEnvelope struct {
	Type     string                  `json:"type"`
	Response sanitizedFailedResponse `json:"response"`
}

type sanitizedErrorEnvelope struct {
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message"`
}

// sanitizedResponsesFailedEvent 重建脱敏的 response.failed 事件 data（保留 Codex 需要的 error.code/message）。
func sanitizedResponsesFailedEvent(responseID, code, message string) []byte {
	env := sanitizedFailedEnvelope{
		Type: eventResponseFailed,
		Response: sanitizedFailedResponse{
			ID: responseID,
			Error: sanitizedFailedError{
				Code:    code,
				Message: sanitizeInlineErrorMessage(message),
			},
		},
	}
	b, err := json.Marshal(env)
	if err != nil {
		return []byte(`{"type":"response.failed","response":{"error":{"message":"upstream provider error"}}}`)
	}
	return b
}

// sanitizedResponsesErrorEvent 重建脱敏的 error 事件 data。
func sanitizedResponsesErrorEvent(code, message string) []byte {
	env := sanitizedErrorEnvelope{
		Type:    eventError,
		Code:    code,
		Message: sanitizeInlineErrorMessage(message),
	}
	b, err := json.Marshal(env)
	if err != nil {
		return []byte(`{"type":"error","message":"upstream provider error"}`)
	}
	return b
}
