package adapter

import (
	"errors"
	"time"
)

// UpstreamErrorCategory 表示一次上游 provider 调用失败的语义分类。
//
// 它与 failure.Category 是不同维度：
//   - failure.Category 回答“哪个模块出错”（adapter / routing / ledger ...）。
//   - UpstreamErrorCategory 回答“上游为什么失败”（认证、限流、超时 ...）。
//
// gateway 只依据这个 category 决定是否 retry/fallback，不解析 provider 原始错误 body。
type UpstreamErrorCategory string

const (
	// UpstreamErrorUnknown 表示无法归类的上游错误，调用方应保守处理（默认不重试）。
	UpstreamErrorUnknown UpstreamErrorCategory = "unknown"

	// UpstreamErrorAuth 表示上游认证失败（通常 401）。
	// 多为平台 channel 凭据问题，不能让用户误以为自己的 API key 错了，
	// 也不应跨 channel 用同一类凭据问题盲目重试。
	UpstreamErrorAuth UpstreamErrorCategory = "auth"

	// UpstreamErrorPermission 表示上游拒绝授权（通常 403）。
	UpstreamErrorPermission UpstreamErrorCategory = "permission"

	// UpstreamErrorRateLimit 表示上游限流（通常 429）。
	UpstreamErrorRateLimit UpstreamErrorCategory = "rate_limit"

	// UpstreamErrorBadRequest 表示上游判定请求本身非法（通常 400/422）。
	// 请求内容不变时换 channel 重试通常无意义。
	UpstreamErrorBadRequest UpstreamErrorCategory = "bad_request"

	// UpstreamErrorTimeout 表示上游超时或 deadline 超时。
	UpstreamErrorTimeout UpstreamErrorCategory = "timeout"

	// UpstreamErrorCanceled 表示请求被调用方（客户端）取消。
	// 不是上游故障，绝不重试或 fallback。
	UpstreamErrorCanceled UpstreamErrorCategory = "canceled"

	// UpstreamErrorServer 表示上游服务端错误（通常 5xx）。
	UpstreamErrorServer UpstreamErrorCategory = "server_error"
)

// UpstreamMetadata 表示一次上游调用的可审计元信息。
//
// 成功或失败都可能携带，用于渠道审计、request/attempt 记录和 observability。
// 只保存协议级非敏感信息（状态码、请求标识、Retry-After）；gateway 的计费/请求记录只显式
// 读取这些字段，不整体持久化本结构。ResponseSnippet 是唯一携带上游原文的字段，仅在非 2xx
// 错误路径填充、供渠道检测排障留痕，gateway 请求记录不消费它（见字段说明）。
type UpstreamMetadata struct {
	// StatusCode 是上游返回的 HTTP 状态码。
	// 0 表示请求未拿到 HTTP 响应，例如连接失败、超时或客户端取消。
	StatusCode int

	// RequestID 是上游返回的请求标识（如响应头 x-request-id）。
	// 空字符串表示上游未提供。
	RequestID string

	// RetryAfter 是上游 429 响应 Retry-After 头解析出的建议冷却时长（P2-7）。
	// 0 表示上游未提供或无法解析；>0 时 gateway 据此对该渠道做限时 cooldown（跳过 fallback）。
	RetryAfter time.Duration

	// ResponseSnippet 是上游「错误响应体」的截断原文快照（仅非 2xx 错误路径填充）。
	// 用途：渠道检测把上游完整错误记进 channel_test_logs 便于排障——adapter 仍只按 HTTP status
	// 分类（不解析此原文），gateway retry/fallback 也不依赖它；正常响应与 gateway 请求记录不使用此字段。
	ResponseSnippet string
}

// UpstreamError 是 adapter 返回给 gateway 的结构化上游错误。
//
// 设计意图：
//   - adapter 负责把 provider-specific 错误（HTTP status、错误 body、网络错误）
//     解析成稳定 Category 和 Metadata。
//   - gateway 只消费 Category 做 retry/fallback 决策，不再解析 provider 细节。
//   - cause 仍是携带稳定 failure.Code 的错误，因此 failure.CodeOf / errors.Is
//     在 error 链上继续可用，request/attempt error_code 写入逻辑无需改动。
type UpstreamError struct {
	// Category 是上游失败的稳定语义分类。
	Category UpstreamErrorCategory

	// Metadata 是本次上游调用的可审计元信息。
	Metadata UpstreamMetadata

	// cause 是底层错误，通常是携带稳定 failure.Code 的 *failure.Failure。
	cause error
}

// NewUpstreamError 构造一个结构化上游错误。
// cause 应当携带稳定 failure.Code，以便日志和 request/attempt error_code 复用。
func NewUpstreamError(category UpstreamErrorCategory, metadata UpstreamMetadata, cause error) *UpstreamError {
	return &UpstreamError{
		Category: category,
		Metadata: metadata,
		cause:    cause,
	}
}

// Error 返回安全的错误摘要，委托给底层 cause，不暴露 provider 原始 body。
func (e *UpstreamError) Error() string {
	if e == nil {
		return ""
	}

	if e.cause != nil {
		return e.cause.Error()
	}

	return string(e.Category)
}

// Unwrap 返回底层 cause，支持 errors.Is / errors.As 和 failure.CodeOf 沿链匹配。
func (e *UpstreamError) Unwrap() error {
	if e == nil {
		return nil
	}

	return e.cause
}

// UpstreamCategoryOf 从 error 链中提取上游错误分类。
// 链上没有 *UpstreamError 时返回 (UpstreamErrorUnknown, false)，
// 调用方应按“未分类”保守处理（默认不重试）。
func UpstreamCategoryOf(err error) (UpstreamErrorCategory, bool) {
	var e *UpstreamError
	if errors.As(err, &e) {
		return e.Category, true
	}

	return UpstreamErrorUnknown, false
}

// UpstreamMetadataOf 从 error 链中提取上游元信息。
// 链上没有 *UpstreamError 时返回 (UpstreamMetadata{}, false)。
func UpstreamMetadataOf(err error) (UpstreamMetadata, bool) {
	var e *UpstreamError
	if errors.As(err, &e) {
		return e.Metadata, true
	}

	return UpstreamMetadata{}, false
}
