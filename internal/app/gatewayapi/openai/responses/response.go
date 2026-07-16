package responses

import (
	"errors"
	"net/http"

	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
)

// Responses 原生错误的稳定 type / code。Responses error 对象形如
// {"error":{"type","code","message","param"}}，与 httpx OpenAI-compatible error shape 一致。
const (
	errorTypeInvalidRequest = "invalid_request_error"
	errorCodeInvalidRequest = "invalid_request"

	// errorCodeUnsupportedBackground 是 /responses 带 background:true 时的 400 拒绝码
	// （Unio 无状态商业承诺，不支持异步任务；openai_responses_other_endpoints.md）。
	errorCodeUnsupportedBackground = "unsupported_background"

	// errorCodeUnsupportedStateless 是有状态 endpoint（retrieve/delete/cancel/input_items）的 501 拒绝码。
	errorCodeUnsupportedStateless = "unsupported_endpoint_stateless"
)

// responsesValidationError 表示 Responses 请求协议结构校验失败后的用户可见错误。
type responsesValidationError struct {
	// code 为空时回退 invalid_request；用于区分 unsupported_background 等专属拒绝码。
	code    string
	param   string
	message string
}

// writeResponsesValidationError 将 validation 错误写成 Responses 原生 400。
func writeResponsesValidationError(w http.ResponseWriter, validationErr *responsesValidationError) {
	code := validationErr.code
	if code == "" {
		code = errorCodeInvalidRequest
	}
	_ = httpx.WriteOpenAIError(
		w,
		http.StatusBadRequest,
		code,
		validationErr.message,
		errorTypeInvalidRequest,
		stringPtr(validationErr.param),
	)
}

// writeResponsesStatelessUnsupported 将有状态 endpoint 写成 Responses 原生 501。
//
// Unio 第一版无服务端响应存储，retrieve/delete/cancel/input_items 一律 501，提示客户每轮回传完整 input。
func writeResponsesStatelessUnsupported(w http.ResponseWriter, message string) {
	_ = httpx.WriteOpenAIError(
		w,
		http.StatusNotImplemented,
		errorCodeUnsupportedStateless,
		message,
		errorTypeInvalidRequest,
		nil,
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
