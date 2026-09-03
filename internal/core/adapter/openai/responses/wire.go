package responses

import (
	"errors"
	"net/http"
	"time"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/core/channel"
)

// Wire 描述同一 Responses 直传实现所服务的具体上游 wire 形态。
//
// 官方 /v1/responses 是缺省 wire（NewAdapter），Codex 订阅后端
// /backend-api/codex/responses 是第二个 wire（openai/codex/responses 包构造）。
// 两者共享全部协议解析、SSE 循环、超时与错误分类——差异只有：路径、请求头、
// 出站前守卫、按账号代理的 HTTP client、以及响应头里的账号用量事实。
// 零值 Wire 等价于官方缺省行为，credential 型渠道路径逐字节不变。
type Wire struct {
	// ResponsesPath / CompactPath 覆盖标准操作路径；空串用官方 /v1/responses(/compact)。
	ResponsesPath string
	CompactPath   string

	// Decorate 在标准头（Content-Type / Authorization / Accept / OpenAI-Beta）之后
	// 追加或覆盖 wire 专属头。ch 携带账号出站身份（channel.Runtime.Account）。
	Decorate func(httpReq *http.Request, ch channel.Runtime)

	// GuardRequest 在出站前校验请求体（Codex：previous_response_id 一律拒绝）。
	// 返回非 nil 即中止出站，错误原样上抛。
	GuardRequest func(req Request) error

	// ClientFor 按渠道运行时选择 http.Client（按账号代理，边界 29）。
	// nil 或返回 nil 时使用 adapter 默认 client。
	ClientFor func(ch channel.Runtime) *http.Client

	// HeaderFacts 从上游响应头抽取 wire 专属事实（Codex x-codex-* 用量头）写进账务事实。
	// 只在拿到 2xx 响应头时调用；流式与非流式同源（用量头都在初始响应头上）。
	HeaderFacts func(header http.Header, facts *adapter.ResponseFacts)

	// RetryAfterFromHeaders 从 wire 专属头解析限流恢复时长（Codex 429 的
	// x-codex-primary-reset-after-seconds），供 4xx/5xx 错误的 RetryAfter 元数据使用。
	// 返回 0 表示无 wire 专属信号，回落标准 Retry-After 头。
	RetryAfterFromHeaders func(header http.Header) time.Duration

	// FinalizeFacts 在账务事实装配完成后做 wire 专属修正（非流式/流式终态/compact 三处同源）。
	// Codex 用它把结算档位改为出站请求档位权威（响应档位不可信，边界 15）。nil = 不修正。
	FinalizeFacts func(req Request, facts *adapter.ResponseFacts)

	// RetryAfterFromBody 从错误体快照解析限流恢复时长（Codex 429 的 usage_limit_reached.resets_at）。
	// 仅在 wire 专属头与标准 Retry-After 都缺失时兜底调用；返回 0 表示 body 无信号。
	RetryAfterFromBody func(statusCode int, snippet string) time.Duration

	// NormalizeRequest 在出站前改写请求体成本 wire 能接受的形态（Codex：store 强制 false、
	// stream 强制目标形态、剔除不支持字段、字符串 input 结构化、system 提升 instructions）。
	// stream 是本次出站的目标流式形态。nil = 零转换直传（官方 wire 纪律不变）。
	NormalizeRequest func(body []byte, stream bool) ([]byte, error)

	// ForceStreaming 表示上游只接受流式请求（Codex 后端对 stream=false 直接 400）。
	// 置位后非流式调用由 adapter 以流式出站、聚合终态 response 对象还原成非流式响应。
	ForceStreaming bool
}

// normalizeRequest 应用 wire 出站前规范化；缺省 wire 零转换。
func (a *Adapter) normalizeRequest(req Request, stream bool) (Request, error) {
	if a.wire.NormalizeRequest == nil {
		return req, nil
	}
	body, err := a.wire.NormalizeRequest(req.Body, stream)
	if err != nil {
		return req, err
	}
	req.Body = body
	return req, nil
}

// responsesPath 返回本 wire 的 responses 操作路径。
func (a *Adapter) responsesPath() string {
	if a.wire.ResponsesPath != "" {
		return a.wire.ResponsesPath
	}
	return adapter.OperationPathResponses
}

// compactPath 返回本 wire 的 compact 操作路径。
func (a *Adapter) compactPath() string {
	if a.wire.CompactPath != "" {
		return a.wire.CompactPath
	}
	return adapter.OperationPathResponsesCompact
}

// httpClient 返回本次调用应使用的 HTTP client。
// wire.ClientFor（codex：账号代理→渠道代理）优先；无 wire 钩子时按渠道代理选（proxyClientFor 注入），
// 都缺省回构造时的直连 client。
func (a *Adapter) httpClient(ch channel.Runtime) *http.Client {
	if a.wire.ClientFor != nil {
		if client := a.wire.ClientFor(ch); client != nil {
			return client
		}
	}
	if a.proxyClientFor != nil && ch.ProxyURL != "" {
		if client := a.proxyClientFor(ch.ProxyURL); client != nil {
			return client
		}
	}
	return a.client
}

// guardRequest 执行 wire 出站前守卫；缺省 wire 无守卫。
func (a *Adapter) guardRequest(req Request) error {
	if a.wire.GuardRequest == nil {
		return nil
	}
	return a.wire.GuardRequest(req)
}

// decorateRequest 应用 wire 专属请求头；缺省 wire 无追加头。
func (a *Adapter) decorateRequest(httpReq *http.Request, ch channel.Runtime) {
	if a.wire.Decorate != nil {
		a.wire.Decorate(httpReq, ch)
	}
}

// applyHeaderFacts 把 wire 专属响应头事实写进账务事实；缺省 wire 无事实。
func (a *Adapter) applyHeaderFacts(header http.Header, facts *adapter.ResponseFacts) {
	if a.wire.HeaderFacts != nil {
		a.wire.HeaderFacts(header, facts)
	}
}

// finalizeFacts 执行 wire 专属的事实收尾修正（非流式/流式终态/compact 三处统一入口）。
func (a *Adapter) finalizeFacts(req Request, facts *adapter.ResponseFacts) {
	if a.wire.FinalizeFacts != nil {
		a.wire.FinalizeFacts(req, facts)
	}
}

// upstreamStatusError 构造非 2xx 错误，RetryAfter 取值优先级：wire 专属头（Codex 重置头）
// → 标准 Retry-After（base 已解析）→ wire 错误体兜底（usage_limit_reached.resets_at）。
func (a *Adapter) upstreamStatusError(resp *http.Response, operation string) error {
	err := newUpstreamStatusError(resp, operation)
	var upstream *adapter.UpstreamError
	if !errors.As(err, &upstream) {
		return err
	}
	meta := upstream.Metadata
	if a.wire.RetryAfterFromHeaders != nil {
		if retryAfter := a.wire.RetryAfterFromHeaders(resp.Header); retryAfter > 0 {
			meta.RetryAfter = retryAfter
		}
	}
	if meta.RetryAfter <= 0 && a.wire.RetryAfterFromBody != nil {
		if retryAfter := a.wire.RetryAfterFromBody(resp.StatusCode, meta.ResponseSnippet); retryAfter > 0 {
			meta.RetryAfter = retryAfter
		}
	}
	// 失败响应头同样携带 wire 专属用量观测（Codex 429 的 x-codex-* 水位是最新鲜的 100% 快照）；
	// 复用 HeaderFacts 解析器塞进元数据，供账号观测回写（白采白不采）。
	if a.wire.HeaderFacts != nil {
		var probe adapter.ResponseFacts
		a.wire.HeaderFacts(resp.Header, &probe)
		if probe.AccountUsage != nil {
			meta.AccountUsage = probe.AccountUsage
		}
	}
	if meta.RetryAfter == upstream.Metadata.RetryAfter && meta.AccountUsage == nil {
		return err
	}
	return adapter.NewUpstreamError(upstream.Category, meta, upstream.Unwrap())
}
