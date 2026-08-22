package console

import (
	"context"
	"errors"
	"fmt"
)

const (
	// CodeRequestUnavailable 表示操作暂时无法完成。
	CodeRequestUnavailable = "request_unavailable"
	// CodeRequestCanceled 表示客户端在响应写出前取消了请求。
	CodeRequestCanceled = "request_canceled"
	// CodeInvalidArgument 表示查询或请求参数无效。
	CodeInvalidArgument = "invalid_argument"
	// StatusClientClosedRequest 是 nginx 约定的「客户端断开」，不是服务端故障。
	StatusClientClosedRequest = 499
)

// Error 是可安全映射到 HTTP 的稳定 Console 应用错误。
// Cause 仅用于服务端诊断，永远不会序列化到响应中。
type Error struct {
	Code              string
	Message           string
	Param             string
	Status            int
	RetryAfter        int
	RemainingAttempts *int
	Cause             error
}

// Error 返回安全的公开消息；消息为空时回退到稳定错误码。
func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	return e.Code
}

// Unwrap 向 errors.Is 和 errors.As 暴露内部原因。
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// IsClientCanceled 判断错误是否由客户端取消请求引起。
func IsClientCanceled(err error) bool {
	return errors.Is(err, context.Canceled)
}

// ClientCanceled 是客户端取消的稳定公开错误，映射为 499。
func ClientCanceled(cause error) *Error {
	return &Error{
		Code:    CodeRequestCanceled,
		Message: "The request was canceled.",
		Status:  StatusClientClosedRequest,
		Cause:   cause,
	}
}

// AsClientCanceled 把 5xx 且原因为取消的错误改写成 499；其它错误原样返回。
func AsClientCanceled(err *Error, requestErr error) *Error {
	if err == nil || err.Status < 500 {
		return err
	}
	if err.Status == StatusClientClosedRequest && err.Code == CodeRequestCanceled {
		return err
	}
	if IsClientCanceled(err) || IsClientCanceled(requestErr) {
		if err.Cause != nil {
			return ClientCanceled(err.Cause)
		}
		return ClientCanceled(requestErr)
	}
	return err
}

// RequestUnavailable 包装内部故障，但不暴露故障来源。
// 客户端取消不是故障：context.Canceled 映射为 499，避免刷新/切页被记成 503。
func RequestUnavailable(operation string, cause error) *Error {
	wrapped := fmt.Errorf("%s: %w", operation, cause)
	if IsClientCanceled(cause) {
		return ClientCanceled(wrapped)
	}
	return &Error{
		Code:    CodeRequestUnavailable,
		Message: "The request could not be completed. Please try again later.",
		Status:  503,
		Cause:   wrapped,
	}
}

// InvalidArgument 表示公开查询或请求参数无法解析。
func InvalidArgument(param, message string) *Error {
	return &Error{
		Code:    CodeInvalidArgument,
		Message: message,
		Param:   param,
		Status:  400,
	}
}
