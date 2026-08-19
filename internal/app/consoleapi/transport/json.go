package transport

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
	consoleservice "github.com/ThankCat/unio-gateway/internal/service/console"
)

const (
	// CodeInvalidJSONBody 表示 Console JSON 请求体无效。
	CodeInvalidJSONBody = "http_invalid_json_body"
	maxRequestBodyBytes = 1 << 20
)

// DecodeJSON 严格解码单个 JSON 对象，并将请求体限制为 1 MiB。
// 未知字段和尾随 JSON 值会在传输边界被拒绝，避免错误协议继续进入业务层。
func DecodeJSON(w http.ResponseWriter, r *http.Request, target any) *consoleservice.Error {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != httpx.ContentTypeJSON {
		return invalidJSONBody("The request body must use application/json.", err)
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return invalidJSONBody("The JSON request body is invalid.", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return invalidJSONBody("The request body must contain exactly one JSON object.", err)
	}
	return nil
}

func invalidJSONBody(message string, cause error) *consoleservice.Error {
	return &consoleservice.Error{
		Code:    CodeInvalidJSONBody,
		Message: message,
		Status:  http.StatusBadRequest,
		Cause:   cause,
	}
}
