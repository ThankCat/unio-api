package openai

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
// 用于渠道审计和 observability，不包含敏感信息。
const upstreamRequestIDHeader = "X-Request-Id"

// newUpstreamStatusError 把上游非 2xx 响应转换成带稳定 category 和 metadata 的结构化错误。
//
// cause 仍使用 failure.CodeAdapterUpstreamStatus，因此 failure.CodeOf 和既有
// request/attempt error_code、日志逻辑完全不变；新增的只是 gateway 可消费的上游分类。
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
			failure.WithMessage(fmt.Sprintf("openai adapter %s status %d", operation, statusCode)),
		),
	)
}

// newUpstreamSendError 把发送上游请求阶段的网络错误转换成带 category 的结构化错误。
//
// 这一步还没有拿到 HTTP 响应，因此 metadata.StatusCode 为 0。
// cause 仍使用 failure.CodeAdapterSendRequestFailed，保留 errors.Is 对底层网络错误的匹配能力。
func newUpstreamSendError(cause error, operation string) error {
	return adapter.NewUpstreamError(
		upstreamCategoryForSendError(cause),
		adapter.UpstreamMetadata{},
		failure.Wrap(
			failure.CodeAdapterSendRequestFailed,
			cause,
			failure.WithMessage(fmt.Sprintf("openai adapter %s", operation)),
		),
	)
}

// upstreamCategoryForStatus 把上游 HTTP 状态码映射成稳定的上游错误分类。
//
// 分类原则：
//   - 401/403 多为平台 channel 凭据/授权问题，单独归类，避免误导用户也避免盲目重试。
//   - 429 是上游限流，408 是上游超时，5xx 是上游服务端错误，这些属于瞬时故障，可由策略 fallback。
//   - 其他 4xx 视为请求本身非法，换 channel 重试无意义。
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
//
//   - 客户端取消 ctx 归为 canceled，绝不重试。
//   - deadline 超时或底层网络 timeout 归为 timeout，可由策略 fallback。
//   - 其余连接级失败（连接拒绝、DNS 等）归为 server_error，视为可尝试其他 channel 的瞬时故障。
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
