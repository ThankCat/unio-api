package anthropic

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"

	"github.com/ThankCat/unio-api/internal/core/adapter"
	"github.com/ThankCat/unio-api/internal/platform/failure"
)

// upstreamRequestID 从上游响应头提取安全的请求标识。
//
// Anthropic 标准返回 request-id；DeepSeek Anthropic endpoint 黑盒未稳定返回该头，
// 因此回退尝试 x-request-id，缺失返回空字符串（不影响审计正确性）。
func upstreamRequestID(header http.Header) string {
	if id := header.Get("request-id"); id != "" {
		return id
	}
	return header.Get("x-request-id")
}

// newUpstreamStatusError 把上游非 2xx 响应转换成带稳定 category 和 metadata 的结构化错误。
//
// 不解析 provider 原始错误 body：DeepSeek Anthropic endpoint 的错误体为 OpenAI 风格信封，
// 与真实 Anthropic error shape 不同，统一以 HTTP status 为主信号分类，gatewayapi/anthropic
// 再渲染原生 Anthropic error shape。
func newUpstreamStatusError(resp *http.Response, operation string) error {
	statusCode := resp.StatusCode

	return adapter.NewUpstreamError(
		upstreamCategoryForStatus(statusCode),
		adapter.UpstreamMetadata{
			StatusCode: statusCode,
			RequestID:  upstreamRequestID(resp.Header),
		},
		failure.New(
			failure.CodeAdapterUpstreamStatus,
			failure.WithMessage(fmt.Sprintf("anthropic adapter %s status %d", operation, statusCode)),
		),
	)
}

// newUpstreamSendError 把发送上游请求阶段的网络错误转换成带 category 的结构化错误。
func newUpstreamSendError(cause error, operation string) error {
	return adapter.NewUpstreamError(
		upstreamCategoryForSendError(cause),
		adapter.UpstreamMetadata{},
		failure.Wrap(
			failure.CodeAdapterSendRequestFailed,
			cause,
			failure.WithMessage(fmt.Sprintf("anthropic adapter %s", operation)),
		),
	)
}

// upstreamCategoryForStatus 把上游 HTTP 状态码映射成稳定的上游错误分类。
func upstreamCategoryForStatus(statusCode int) adapter.UpstreamErrorCategory {
	switch {
	case statusCode == http.StatusUnauthorized:
		return adapter.UpstreamErrorAuth
	case statusCode == http.StatusForbidden:
		return adapter.UpstreamErrorPermission
	case statusCode == http.StatusTooManyRequests:
		return adapter.UpstreamErrorRateLimit
	case statusCode == http.StatusRequestTimeout:
		return adapter.UpstreamErrorTimeout
	case statusCode >= 500:
		return adapter.UpstreamErrorServer
	case statusCode >= 400:
		return adapter.UpstreamErrorBadRequest
	default:
		return adapter.UpstreamErrorUnknown
	}
}

// upstreamCategoryForSendError 把发送阶段的网络错误映射成稳定分类。
func upstreamCategoryForSendError(cause error) adapter.UpstreamErrorCategory {
	switch {
	case errors.Is(cause, context.Canceled):
		return adapter.UpstreamErrorCanceled
	case errors.Is(cause, context.DeadlineExceeded):
		return adapter.UpstreamErrorTimeout
	default:
		var netErr net.Error
		if errors.As(cause, &netErr) && netErr.Timeout() {
			return adapter.UpstreamErrorTimeout
		}
		return adapter.UpstreamErrorServer
	}
}
