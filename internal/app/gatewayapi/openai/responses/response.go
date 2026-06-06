package responses

import (
	"errors"
	"net/http"

	"github.com/ThankCat/unio-api/internal/platform/httpx"
)

// Responses 原生错误的稳定 type / code。Responses error 对象形如
// {"error":{"type","code","message","param"}}，与 httpx OpenAI-compatible error shape 一致。
const (
	errorTypeInvalidRequest = "invalid_request_error"
	errorCodeInvalidRequest = "invalid_request"
)

// responsesValidationError 表示 Responses 请求协议结构校验失败后的用户可见错误。
type responsesValidationError struct {
	param   string
	message string
}

// writeResponsesValidationError 将 validation 错误写成 Responses 原生 400。
func writeResponsesValidationError(w http.ResponseWriter, validationErr *responsesValidationError) {
	_ = httpx.WriteOpenAIError(
		w,
		http.StatusBadRequest,
		errorCodeInvalidRequest,
		validationErr.message,
		errorTypeInvalidRequest,
		stringPtr(validationErr.param),
	)
}

// writeResponsesDecodeError 将 JSON decode 错误转换成 Responses 原生 400 / 4xx。
func writeResponsesDecodeError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	message := "invalid json body"

	switch {
	case errors.Is(err, httpx.ErrUnsupportedContentType):
		status = http.StatusUnsupportedMediaType
		message = "content type must be application/json"
	case errors.Is(err, httpx.ErrRequestBodyTooLarge):
		status = http.StatusRequestEntityTooLarge
		message = "request body too large"
	case errors.Is(err, httpx.ErrEmptyJSONBody):
		message = "request body is required"
	case errors.Is(err, httpx.ErrTrailingJSONToken):
		message = "request body must contain a single JSON object"
	}

	_ = httpx.WriteOpenAIError(
		w,
		status,
		errorCodeInvalidRequest,
		message,
		errorTypeInvalidRequest,
		nil,
	)
}

// stringPtr 返回字符串指针，用于构造 optional 字段。
func stringPtr(v string) *string {
	return &v
}
