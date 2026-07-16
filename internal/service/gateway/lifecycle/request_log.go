package lifecycle

import (
	"errors"
	"strings"

	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

// MaxRequestLogInternalErrorDetailBytes 是 request_records.internal_error_detail
// 字段允许保留的最大字节数；超出部分被截断并追加 "...[truncated]"。
//
// 协议无关：OpenAI / Anthropic 双协议共享同一个 request log 表与字段长度策略。
const MaxRequestLogInternalErrorDetailBytes = 2048

// FailureCodeOrFallback 优先取 failure.CodeOf(err) 作为稳定错误身份；
// 没有 failure code 时返回调用方提供的 fallback 字符串。
//
// 协议无关：所有 gateway 编排骨架（chat / messages / 未来 operation）记 attempt/request
// error_code 都遵循同一规则——稳定 failure code 优先于 service 局部字符串 code。
func FailureCodeOrFallback(err error, fallback string) string {
	if code := failure.CodeOf(err); code != "" {
		return string(code)
	}

	return fallback
}

// InternalErrorDetail 返回供内部排查的错误详情，并限制长度避免单行日志/单条 record
// 无限膨胀。返回值仅用于 request_records.internal_error_detail，绝不出现在公开响应中。
//
// 会沿 errors.Unwrap 链拼接各层消息：failure.Failure.Error() 只返回归类文案、不含 cause，
// 因此若只取顶层 Error() 会丢掉真正根因（如 context canceled / unexpected EOF /
// connection reset by peer）——而这些恰是排查流式断连最关键的信息。逐层去重拼接后截断。
//
// 协议无关：双协议共享同一截断策略与字段长度上限。
func InternalErrorDetail(err error) string {
	if err == nil {
		return ""
	}

	var detail string
	for cur := err; cur != nil; cur = errors.Unwrap(cur) {
		msg := strings.TrimSpace(cur.Error())
		if msg == "" {
			continue
		}
		// 标准库 %w 包装的错误其 Error() 往往已内联子 cause，避免重复拼接。
		if detail == "" {
			detail = msg
		} else if !strings.Contains(detail, msg) {
			detail = detail + ": " + msg
		}
	}

	if len(detail) <= MaxRequestLogInternalErrorDetailBytes {
		return detail
	}

	return detail[:MaxRequestLogInternalErrorDetailBytes] + "...[truncated]"
}

// RoutingFailureCode 将 routing 内部错误转换成 request_records.error_code。
//
// 协议无关：routing 给出的 sentinel error 与 failure code 不分协议；双协议 service
// 都用同一条映射，避免每个 operation 维护自己的 routing → code 表。
func RoutingFailureCode(err error) string {
	if code := failure.CodeOf(err); code != "" {
		return string(code)
	}

	switch {
	case errors.Is(err, routing.ErrModelNotFound):
		return "model_not_found"
	case errors.Is(err, routing.ErrModelNotAvailable):
		return "model_not_available"
	case errors.Is(err, routing.ErrNoAvailableChannel):
		return "no_available_channel"
	case errors.Is(err, routing.ErrRouteNotConfigured):
		return string(failure.CodeRoutingRouteNotConfigured)
	default:
		return "routing_error"
	}
}

// BaseSafeRequestLogErrorMessage 将协议无关 error code 映射成可展示的安全文案。
//
// 该函数只处理双协议共享的 code（client_canceled / failure.Code* / 按 category 兜底）；
// service-specific ad-hoc 字符串 code（例如 "chat_authorization_failed" /
// "messages_authorization_failed"）由各 service 局部 safeRequestLogErrorMessage 包装，
// 未命中再 fall through 到本函数。
//
// 这里不能直接用 err.Error()，否则会把 SQL、上游响应、路径或配置细节暴露给
// 后台 / console 日志展示。
func BaseSafeRequestLogErrorMessage(code string) string {
	switch code {
	case "client_canceled":
		return "Request was canceled by the client."
	case "model_not_found", string(failure.CodeRoutingModelNotFound):
		return "The requested model was not found."
	case "model_not_available", "no_available_channel",
		string(failure.CodeRoutingModelNotAvailable), string(failure.CodeRoutingNoAvailableChannel),
		string(failure.CodeRoutingRouteNotConfigured):
		return "The requested model is temporarily unavailable."
	case string(failure.CodeGatewayChatAuthorizationFailed):
		return "Request authorization failed."
	case string(failure.CodeGatewayChatSettlementFailed):
		return "Request settlement failed."
	case string(failure.CodeLedgerInsufficientBalance):
		return "Insufficient balance."
	case string(failure.CodeGatewayStreamUsageMissing):
		return "Stream usage is missing."
	}

	switch failure.Code(code).Category() {
	case failure.CategoryAdapter:
		return "Upstream provider request failed."
	case failure.CategoryRouting:
		return "Request routing failed."
	case failure.CategoryLedger, failure.CategoryBilling:
		return "Request billing failed."
	case failure.CategoryGateway:
		return "Gateway request failed."
	default:
		return "Request failed."
	}
}
