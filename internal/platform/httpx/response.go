package httpx

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/ThankCat/unio-api/internal/platform/failure"
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

// WriteJSON 将 v 以 JSON 格式写入响应，并设置 HTTP 状态码。
func WriteJSON(w http.ResponseWriter, status int, v any) error {
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
