package chatcompletions

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
			StatusCode:      statusCode,
			RequestID:       resp.Header.Get(upstreamRequestIDHeader),
			RetryAfter:      adapter.ParseRetryAfterHeader(resp.Header),
			ResponseSnippet: adapter.ReadUpstreamErrorSnippet(resp.Body),
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
			failure.WithMessage(fmt.Sprintf("openai adapter %s", operation)),
		),
	)
}

// newUpstreamStreamReadError 把「读流阶段失败」转换成带上游分类的结构化错误。
//
// 关键点（P1-7 / P1-8）：读流失败必须携带稳定上游分类，否则 retry 分类器拿不到 category 会一律判为不可
// 重试——而流式 fallback 只在「首字节前失败(尚未向客户写出任何 SSE 帧)」时才会发生，此时换同模型 channel
// 完全安全。分类规则：
//   - idle 看门狗触发：归为 timeout（携带 CodeAdapterStreamIdleTimeout），区分半开/挂死连接。
//   - context 取消（客户端断开）：归为 canceled，绝不 fallback。
//   - deadline / 网络 timeout：归为 timeout。
//   - 其余传输层失败（连接重置、EOF、proxy 截断、malformed stream 等）：归为 server_error，允许首字节前 fallback。
//
// cause 始终保留 CodeAdapterReadStreamFailed（或 idle 专用码），request/attempt error_code 与既有审计口径不变。
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

// newUpstreamStreamIncompleteError 表示流在出现可靠终态（[DONE]）前就正常结束（无读错误）。
//
// 这通常是上游/中转截断了尾包。归为 server_error 让「首字节前」可 fallback 到同模型其他 channel；
// 已写出内容后 lifecycle 会先走 partial settlement，不会触达 fallback。cause 保留 CodeAdapterReadStreamFailed。
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
