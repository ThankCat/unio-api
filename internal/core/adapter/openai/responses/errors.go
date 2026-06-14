package responses

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"

	"github.com/ThankCat/unio-api/internal/core/adapter"
	"github.com/ThankCat/unio-api/internal/platform/failure"
)

// upstreamRequestIDHeader 是 OpenAI-compatible 上游返回请求标识的响应头。
const upstreamRequestIDHeader = "X-Request-Id"

// newUpstreamStatusError 把上游非 2xx 响应转换成带稳定 category 和 metadata 的结构化错误。
//
// 与 chat adapter 同口径：cause 使用 failure.CodeAdapterUpstreamStatus，gateway 据 category 决定
// retry/fallback，不解析上游原始 body。
func newUpstreamStatusError(resp *http.Response, operation string) error {
	statusCode := resp.StatusCode

	return adapter.NewUpstreamError(
		upstreamCategoryForStatus(statusCode),
		adapter.UpstreamMetadata{
			StatusCode: statusCode,
			RequestID:  resp.Header.Get(upstreamRequestIDHeader),
		},
		failure.New(
			failure.CodeAdapterUpstreamStatus,
			failure.WithMessage(fmt.Sprintf("openai responses adapter %s status %d", operation, statusCode)),
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
			failure.WithMessage(fmt.Sprintf("openai responses adapter %s", operation)),
		),
	)
}

// newUpstreamStreamError 把上游 SSE 内联终态错误（response.failed / error 事件）转换成结构化错误。
//
// meta 携带本次流式调用的 HTTP 状态与 request id；cause 用 CodeAdapterUpstreamStatus，保持与
// 非流式错误同一审计 error_code 维度。
func newUpstreamStreamError(meta adapter.UpstreamMetadata, code, message string) error {
	detail := message
	if code != "" {
		detail = fmt.Sprintf("%s: %s", code, message)
	}
	return adapter.NewUpstreamError(
		adapter.UpstreamErrorServer,
		meta,
		failure.New(
			failure.CodeAdapterUpstreamStatus,
			failure.WithMessage(fmt.Sprintf("openai responses adapter upstream stream failed (%s)", detail)),
		),
	)
}

// upstreamCategoryForStatus 把上游 HTTP 状态码映射成稳定的上游错误分类（与 chat adapter 同口径）。
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

// upstreamCategoryForSendError 把发送阶段的网络错误映射成稳定分类（与 chat adapter 同口径）。
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
