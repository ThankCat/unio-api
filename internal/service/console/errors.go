package console

import "fmt"

const (
	// CodeRequestUnavailable 表示操作暂时无法完成。
	CodeRequestUnavailable = "request_unavailable"
	// CodeInvalidArgument 表示查询或请求参数无效。
	CodeInvalidArgument = "invalid_argument"
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

// RequestUnavailable 包装内部故障，但不暴露故障来源。
func RequestUnavailable(operation string, cause error) *Error {
	return &Error{
		Code:    CodeRequestUnavailable,
		Message: "The request could not be completed. Please try again later.",
		Status:  503,
		Cause:   fmt.Errorf("%s: %w", operation, cause),
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
