package transport

import (
	"net/http"
	"strconv"

	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
	consoleservice "github.com/ThankCat/unio-gateway/internal/service/console"
)

// DataResponse 是 Console API 接口共用的成功响应结构。
type DataResponse[T any] struct {
	Data T `json:"data"`
}

// WriteData 写入带类型的 Console 成功响应结构。
func WriteData[T any](w http.ResponseWriter, status int, data T) error {
	return httpx.WriteJSON(w, status, DataResponse[T]{Data: data})
}

// ErrorWriter 将 Console 服务错误映射为公开的 HTTP 错误响应结构。
type ErrorWriter struct {
	logger *zap.Logger
}

// NewErrorWriter 创建支持服务端诊断日志的 Console 错误写入器。
func NewErrorWriter(logger *zap.Logger) ErrorWriter {
	if logger == nil {
		logger = zap.NewNop()
	}
	return ErrorWriter{logger: logger}
}

// Write 序列化 Console 错误，并记录内部故障但不向客户端暴露原因。
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
	_ = httpx.WriteConsoleErrorDetails(
		w,
		err.Status,
		err.Code,
		message,
		param,
		err.RemainingAttempts,
	)
}
