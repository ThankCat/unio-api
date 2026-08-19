package httpx

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

const (
	// ContentTypeJSON 是 JSON 响应使用的 Content-Type。
	ContentTypeJSON = "application/json"

	// ContentTypeSSE 是 SSE 流式响应使用的 Content-Type。
	ContentTypeSSE = "text/event-stream"
)

var (
	// ErrStreamingUnsupported 表示当前 ResponseWriter 不支持流式 flush。
	ErrStreamingUnsupported = errors.New("streaming unsupported")
)

// ErrorResponse 是 API 错误响应的外层结构。
type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

// ErrorBody 描述 API 错误的 OpenAI-compatible 响应体。
type ErrorBody struct {
	Message string  `json:"message"`
	Type    string  `json:"type"`
	Param   *string `json:"param"`
	Code    string  `json:"code"`
}

// ConsoleErrorResponse 是 Console API 错误响应的外层结构。
//
// 该类型与 OpenAI-compatible 错误类型分开定义，避免 Console 协议意外依赖
// Gateway 的 OpenAI 兼容契约。
type ConsoleErrorResponse struct {
	Error ConsoleErrorBody `json:"error"`
}

// ConsoleErrorBody 描述 Console API 的稳定错误字段。
type ConsoleErrorBody struct {
	Code              string  `json:"code"`
	Message           string  `json:"message"`
	Type              string  `json:"type"`
	Param             *string `json:"param"`
	RemainingAttempts *int    `json:"remaining_attempts,omitempty"`
}

// WriteJSON 将 v 以 JSON 格式写入响应，并设置 HTTP 状态码。
func WriteJSON(w http.ResponseWriter, status int, v any) error {
	if err := RefreshResponseWriteDeadline(w, 0); err != nil {
		return failure.Wrap(
			failure.CodeHTTPResponseWriteFailed,
			err,
			failure.WithMessage("set json response write deadline"),
		)
	}
	w.Header().Set("Content-Type", ContentTypeJSON)
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		return failure.Wrap(
			failure.CodeHTTPResponseWriteFailed,
			err,
			failure.WithMessage("write json response"),
		)
	}

	return nil
}

// WriteError 写入统一格式的 JSON 错误响应。
func WriteError(w http.ResponseWriter, status int, code string, message string) error {
	return WriteOpenAIError(w, status, code, message, "api_error", nil)
}

// WriteOpenAIError 写入 OpenAI-compatible JSON 错误响应。
func WriteOpenAIError(w http.ResponseWriter, status int, code string, message string, errorType string, param *string) error {
	errBody := ErrorResponse{
		Error: ErrorBody{
			Code:    code,
			Message: message,
			Type:    errorType,
			Param:   param,
		},
	}

	return WriteJSON(w, status, errBody)
}

// WriteConsoleError 写入 Console API 专用的 JSON 错误响应。
//
// message 是面向 API 调用方的英文安全摘要；详细内部错误只能写入服务端日志。
func WriteConsoleError(w http.ResponseWriter, status int, code string, message string, param *string) error {
	return WriteConsoleErrorDetails(w, status, code, message, param, nil)
}

// WriteConsoleErrorDetails 写入可带结构化详情的 Console 错误响应。
func WriteConsoleErrorDetails(
	w http.ResponseWriter,
	status int,
	code string,
	message string,
	param *string,
	remainingAttempts *int,
) error {
	errorType := "request_error"
	if status >= http.StatusInternalServerError {
		errorType = "server_error"
	}
	return WriteJSON(w, status, ConsoleErrorResponse{
		Error: ConsoleErrorBody{
			Code:              code,
			Message:           message,
			Type:              errorType,
			Param:             param,
			RemainingAttempts: remainingAttempts,
		},
	})
}
