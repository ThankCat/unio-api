package responses

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
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
			RetryAfter: adapter.ParseRetryAfterHeader(resp.Header),
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

// newUpstreamSendErrorWithContextCause is used by stream calls whose request
// context may be canceled by HeaderTimeoutContext before response headers
// arrive. Some transports surface that as plain "context canceled"; the
// context cause keeps the server-side header timeout distinguishable from a
// real client cancel.
func newUpstreamSendErrorWithContextCause(cause error, ctxCause error, operation string) error {
	classifyCause := cause
	if errors.Is(ctxCause, context.DeadlineExceeded) {
		classifyCause = ctxCause
	}
	return adapter.NewUpstreamError(
		upstreamCategoryForSendError(classifyCause),
		adapter.UpstreamMetadata{},
		failure.Wrap(
			failure.CodeAdapterSendRequestFailed,
			classifyCause,
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

// newUpstreamStreamReadError 把「读流阶段失败」转换成带上游分类的结构化错误。
//
// 关键点（P1-7 / P1-8）：读流失败必须携带稳定上游分类，否则 retry 分类器拿不到 category 会一律判为不可
// 重试——而流式 fallback 只在「首字节前失败(尚未向客户写出任何 SSE 帧)」时才会发生，此时换同模型 channel
// 完全安全。分类规则与 chat adapter 同口径：idle→timeout、取消→canceled、deadline/网络 timeout→timeout、
// 其余传输层失败（连接重置、EOF、proxy 截断、malformed stream 等）→server_error（允许首字节前 fallback）。
// cause 始终保留 CodeAdapterReadStreamFailed（或 idle 专用码），审计 error_code 不变。
func newUpstreamStreamReadError(readErr error, ctxCause error, operation string) error {
	if errors.Is(ctxCause, adapter.ErrStreamIdleTimeout) {
		return adapter.NewUpstreamError(
			adapter.UpstreamErrorTimeout,
			adapter.UpstreamMetadata{},
			failure.Wrap(
				failure.CodeAdapterStreamIdleTimeout,
				adapter.ErrStreamIdleTimeout,
				failure.WithMessage(fmt.Sprintf("%s: upstream stream idle timeout", operation)),
			),
		)
	}
	return adapter.NewUpstreamError(
		classifyStreamReadCategory(readErr, ctxCause),
		adapter.UpstreamMetadata{},
		failure.Wrap(
			failure.CodeAdapterReadStreamFailed,
			readErr,
			failure.WithMessage(operation),
		),
	)
}

// newUpstreamStreamIncompleteError 表示流在出现可靠终态事件前就正常结束（无读错误）。
//
// 通常是上游/中转截断尾包。归为 server_error 让「首字节前」可 fallback；已写出内容后由 lifecycle partial
// settlement 兜底，不会触达 fallback。cause 保留 CodeAdapterReadStreamFailed。
func newUpstreamStreamIncompleteError(operation string) error {
	return adapter.NewUpstreamError(
		adapter.UpstreamErrorServer,
		adapter.UpstreamMetadata{},
		failure.New(
			failure.CodeAdapterReadStreamFailed,
			failure.WithMessage(operation),
		),
	)
}

// classifyStreamReadCategory 依据底层读错误与 context cause 把读流失败映射成稳定上游分类。
func classifyStreamReadCategory(readErr error, ctxCause error) adapter.UpstreamErrorCategory {
	switch {
	case errors.Is(ctxCause, context.Canceled) || errors.Is(readErr, context.Canceled):
		return adapter.UpstreamErrorCanceled
	case errors.Is(ctxCause, context.DeadlineExceeded) || errors.Is(readErr, context.DeadlineExceeded):
		return adapter.UpstreamErrorTimeout
	default:
		var netErr net.Error
		if errors.As(readErr, &netErr) && netErr.Timeout() {
			return adapter.UpstreamErrorTimeout
		}
		return adapter.UpstreamErrorServer
	}
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
