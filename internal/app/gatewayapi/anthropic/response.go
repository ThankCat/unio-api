// Package anthropic 放 Anthropic 协议族在 gatewayapi 层共享的响应与错误渲染能力。
//
// 具体 operation（如 messages）在子包实现；本包只保留协议族级共享结构，例如 Anthropic
// 原生错误形状。Anthropic ingress 对外保持 Anthropic error shape，不复用 OpenAI error body。
package anthropic

// ErrorResponse 是 Anthropic 原生错误响应结构。
//
//	{
//	  "type": "error",
//	  "error": {"type": "invalid_request_error", "message": "..."},
//	  "request_id": "..."
//	}
type ErrorResponse struct {
	Type      string    `json:"type"`
	Error     ErrorBody `json:"error"`
	RequestID string    `json:"request_id,omitempty"`
}

// ErrorBody 是 Anthropic 错误响应中的 error 对象。
type ErrorBody struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// NewErrorResponse 用安全的错误类型与公开文案构造 Anthropic 错误响应。
// message 必须是安全公开文案，不能包含上游原始 body 或内部诊断细节。
func NewErrorResponse(errorType, message, requestID string) ErrorResponse {
	return ErrorResponse{
		Type: "error",
		Error: ErrorBody{
			Type:    errorType,
			Message: message,
		},
		RequestID: requestID,
	}
}
