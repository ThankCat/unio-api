package messages

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

// upstreamRequestID 从上游响应头提取安全的请求标识。
//
// Anthropic 标准返回 request-id；DeepSeek Anthropic origin 黑盒未稳定返回该头，
// 因此回退尝试 x-request-id，缺失返回空字符串（不影响审计正确性）。
func upstreamRequestID(header http.Header) string {
	if id := header.Get("request-id"); id != "" {
		return id
	}
	return header.Get("x-request-id")
}

// newUpstreamProtocolError 把「已拿到 2xx 响应但 body 无法按协议解析」收成结构化错误。
// Category=unknown（网关侧不重试）；ResponseSnippet 带上响应原文供渠道检测展示。
func newUpstreamProtocolError(statusCode int, requestID string, body []byte, cause error) error {
	return adapter.NewUpstreamError(
		adapter.UpstreamErrorUnknown,
		adapter.UpstreamMetadata{
			StatusCode:      statusCode,
			RequestID:       requestID,
			ResponseSnippet: adapter.SnippetFromBytes(body),
		},
		cause,
	)
}

// newUpstreamStatusError 把上游非 2xx 响应转换成带稳定 category 和 metadata 的结构化错误。
//
// 分类仍不解析 provider 原始错误 body：DeepSeek Anthropic origin 的错误体为 OpenAI 风格信封，
// 与真实 Anthropic error shape 不同，统一以 HTTP status 为主信号分类，gatewayapi/anthropic
// 再渲染原生 Anthropic error shape。但会把原始错误体截断快照放进 metadata.ResponseSnippet，
// 供渠道检测排障留痕（不参与分类，也不进 gateway 请求记录）。
func newUpstreamStatusError(resp *http.Response, operation string) error {
	statusCode := resp.StatusCode

	return adapter.NewUpstreamError(
		upstreamCategoryForStatus(statusCode),
		adapter.UpstreamMetadata{
			StatusCode:      statusCode,
			RequestID:       upstreamRequestID(resp.Header),
			RetryAfter:      adapter.ParseRetryAfterHeader(resp.Header),
			ResponseSnippet: adapter.ReadUpstreamErrorSnippet(resp.Body),
		},
		failure.New(
			failure.CodeAdapterUpstreamStatus,
			failure.WithMessage(fmt.Sprintf("anthropic adapter %s status %d", operation, statusCode)),
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
			failure.WithMessage(fmt.Sprintf("anthropic adapter %s", operation)),
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

// newUpstreamStreamIncompleteError 表示流在出现可靠终态（message_stop）前就正常结束（无读错误）。
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
