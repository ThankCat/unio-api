package transport

import (
	"net/http"
	"strconv"

	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
	consoleservice "github.com/ThankCat/unio-gateway/internal/service/console"
)

// DataResponse is the success envelope shared by Console API endpoints.
type DataResponse[T any] struct {
	Data T `json:"data"`
}

// WriteData writes a typed Console success envelope.
func WriteData[T any](w http.ResponseWriter, status int, data T) error {
	return httpx.WriteJSON(w, status, DataResponse[T]{Data: data})
}

// ErrorWriter maps Console service errors to the public HTTP error envelope.
type ErrorWriter struct {
	logger *zap.Logger
}

// NewErrorWriter creates a Console error writer with server-side diagnostics.
func NewErrorWriter(logger *zap.Logger) ErrorWriter {
	if logger == nil {
		logger = zap.NewNop()
	}
	return ErrorWriter{logger: logger}
}

// Write serializes a Console error and logs internal failures without exposing causes.
func (e ErrorWriter) Write(w http.ResponseWriter, err *consoleservice.Error) {
	if err == nil {
		return
	}
	if err.RetryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(err.RetryAfter))
	}
	if err.Status >= http.StatusInternalServerError {
		fields := []zap.Field{zap.String("code", err.Code)}
		if err.Cause != nil {
			fields = append(fields, zap.Error(err.Cause))
		} else {
			fields = append(fields, zap.Error(err))
		}
		e.logger.Error("console request failed", fields...)
	}
	message := err.Message
	if message == "" {
		message = "The request failed."
	}
	var param *string
	if err.Param != "" {
		param = &err.Param
	}
	_ = httpx.WriteConsoleError(w, err.Status, err.Code, message, param)
}
