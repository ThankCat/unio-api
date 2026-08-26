package responses

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"unicode/utf8"

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
			StatusCode:      statusCode,
			RequestID:       resp.Header.Get(upstreamRequestIDHeader),
			RetryAfter:      adapter.ParseRetryAfterHeader(resp.Header),
			ResponseSnippet: adapter.ReadUpstreamErrorSnippet(resp.Body),
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

// newUpstreamSendErrorWithContextCause preserves server-side timeout causes when
// the transport only surfaces "context canceled" before response headers arrive.
// This keeps response-header and first-token timeouts distinguishable from a real
// client cancel.
func newUpstreamSendErrorWithContextCause(cause error, ctxCause error, operation string) error {
	classifyCause := cause
	if errors.Is(ctxCause, context.DeadlineExceeded) || errors.Is(ctxCause, adapter.ErrFirstTokenTimeout) {
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
	return newUpstreamStreamErrorWithPayload(meta, code, message, nil)
}

// maxDiagnosticPayloadLen 限制附进诊断详情的上游原文长度。
const maxDiagnosticPayloadLen = 512

// newUpstreamStreamErrorWithPayload 在 newUpstreamStreamError 之上多接一份上游事件原文。
//
// 上游把错误放在 code/message 之外的字段上时（中转常见），detail 只会剩一句默认文案，
// 排障既看不到上游说了什么，也分不清是上游的问题还是我们解析错了。raw 非空时把原文截断后
// 附进 detail——它只进 internal_error_detail，不进 meta.ErrorMessage，后者要回给客户。
func newUpstreamStreamErrorWithPayload(meta adapter.UpstreamMetadata, code, message string, raw []byte) error {
	detail := message
	if code != "" {
		detail = fmt.Sprintf("%s: %s", code, message)
	}
	if payload := diagnosticPayload(raw); payload != "" {
		detail = fmt.Sprintf("%s; upstream payload: %s", detail, payload)
	}
	// 与内联透传共用同一套脱敏：首字前失败时内联事件会被丢弃（首字前不向客户暴露失败渠道事件），
	// 上游真实原因只能靠这里带到最终错误响应，否则客户永远只看到一句通用文案。
	// 在构造处就脱敏，保证任何读取方都拿不到未脱敏内容。
	meta.ErrorCode = code
	meta.ErrorMessage = sanitizeInlineErrorMessage(message)
	return adapter.NewUpstreamError(
		upstreamCategoryForStreamError(code),
		meta,
		failure.New(
			failure.CodeAdapterUpstreamStatus,
			failure.WithMessage(fmt.Sprintf("openai responses adapter upstream stream failed (%s)", detail)),
		),
	)
}

// diagnosticPayload 把上游事件原文压成单行、截断到上限，供内部排障阅读。
// 空白折叠是为了让整条 detail 留在一行日志里，否则多行 JSON 会把日志切碎。
func diagnosticPayload(raw []byte) string {
	payload := strings.Join(strings.Fields(string(raw)), " ")
	if payload == "" {
		return ""
	}
	if len(payload) > maxDiagnosticPayloadLen {
		// 按 rune 边界回退，避免把多字节字符截成乱码。
		cut := maxDiagnosticPayloadLen
		for cut > 0 && !utf8.RuneStart(payload[cut]) {
			cut--
		}
		payload = strings.TrimSpace(payload[:cut]) + "…"
	}
	return payload
}

func upstreamCategoryForStreamError(code string) adapter.UpstreamErrorCategory {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case "rate_limit", "rate_limit_error", "rate_limit_exceeded":
		return adapter.UpstreamErrorRateLimit
	default:
		return adapter.UpstreamErrorServer
	}
}

// newUpstreamStreamReadError 把「读流阶段失败」转换成带上游分类的结构化错误。
//
// 关键点（P1-7 / P1-8）：读流失败必须携带稳定上游分类，否则 retry 分类器拿不到 category 会一律判为不可
// 重试——而流式 fallback 只在「客户帧写出前失败」时才会发生，此时换同模型 channel
// 完全安全。分类规则与 chat adapter 同口径：idle→timeout、取消→canceled、deadline/网络 timeout→timeout、
// 其余传输层失败（连接重置、EOF、proxy 截断、malformed stream 等）→server_error（允许客户帧写出前 fallback）。
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
	if errors.Is(ctxCause, adapter.ErrFirstTokenTimeout) {
		return adapter.NewUpstreamError(
			adapter.UpstreamErrorTimeout,
			adapter.UpstreamMetadata{},
			failure.Wrap(
				failure.CodeAdapterReadStreamFailed,
				adapter.ErrFirstTokenTimeout,
				failure.WithMessage(fmt.Sprintf("%s: upstream first token timeout", operation)),
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

func newUpstreamBodyReadError(readErr error, ctxCause error, operation string) error {
	cause := readErr
	if errors.Is(ctxCause, context.DeadlineExceeded) {
		cause = ctxCause
	}
	return adapter.NewUpstreamError(
		classifyStreamReadCategory(readErr, ctxCause),
		adapter.UpstreamMetadata{},
		failure.Wrap(
			failure.CodeAdapterReadStreamFailed,
			cause,
			failure.WithMessage(operation),
		),
	)
}

// newUpstreamStreamIncompleteError 表示流在出现可靠终态事件前就正常结束（无读错误）。
//
// 通常是上游/中转截断尾包。归为 server_error 让「客户帧写出前」可 fallback；已写出内容后由 lifecycle partial
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
	case errors.Is(cause, context.DeadlineExceeded), errors.Is(cause, adapter.ErrFirstTokenTimeout):
		return adapter.UpstreamErrorTimeout
	default:
		var netErr net.Error
		if errors.As(cause, &netErr) && netErr.Timeout() {
			return adapter.UpstreamErrorTimeout
		}
		return adapter.UpstreamErrorServer
	}
}
