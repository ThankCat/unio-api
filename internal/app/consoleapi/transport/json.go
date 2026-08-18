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
	// CodeInvalidJSONBody identifies an invalid Console JSON request body.
	CodeInvalidJSONBody = "http_invalid_json_body"
	maxRequestBodyBytes = 1 << 20
)

// DecodeJSON decodes one strict JSON object with a one-megabyte body limit.
// Unknown fields and trailing JSON values are rejected so API contract mistakes
// fail at the transport boundary.
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
