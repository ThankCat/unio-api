// Package codexresponses 构造 Codex 订阅后端（chatgpt.com/backend-api/codex）的 Responses wire。
//
// 它不是新的协议实现：Codex 后端说的就是 Responses 协议，事件流、usage、终态语义与官方
// /v1/responses 一致（wire 证据：sandbox/codex/wire/samples/）。因此本包只装配 base
// responses adapter 的 Wire 钩子——路径、账号请求头、会话亲和身份、出站守卫、按账号代理、
// 用量头解析——协议解析、SSE 循环、超时与错误分类全部复用 base，一处不改。
//
// 账号维度经 channel.Runtime 注入（Runtime.APIKey = 账号 access token、Runtime.Account），
// adapter 对号池无感知。
package codexresponses

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	openairesponses "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/responses"
	"github.com/ThankCat/unio-gateway/internal/core/channel"
	"github.com/ThankCat/unio-gateway/internal/core/codexidentity"
	"github.com/ThankCat/unio-gateway/internal/core/servicetier"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

const (
	// responsesPath / compactPath 是 Codex 订阅后端的操作路径（origin = https://chatgpt.com）。
	responsesPath = "/backend-api/codex/responses"
	compactPath   = "/backend-api/codex/responses/compact"
	// modelsPath 是模型清单端点（发现流程数据源，带 client_version 查询参数）。
	modelsPath = "/backend-api/codex/models"
)

// NewAdapter 创建 Codex 订阅 wire 的 Responses adapter。
//
// clientFor 按代理 URL 解析 HTTP client（bootstrap 注入 proxyclient.Resolver），
// 让每个账号从自己绑定的出口出站；nil 表示全部直连。
// version 提供当前生效的客户端版本（Admin 覆写 → 自动同步 → 基线，见 codexidentity），nil 用基线：
// 出站身份三个头由 codexidentity 同源渲染，全部账号使用同一组客户端身份（边界 26 的统一身份，
// 不做随机化伪装）。
func NewAdapter(client *http.Client, clientFor func(proxyURL string) *http.Client, version codexidentity.VersionSource) *openairesponses.Adapter {
	wire := openairesponses.Wire{
		ResponsesPath:         responsesPath,
		CompactPath:           compactPath,
		Decorate:              codexDecorator(version),
		GuardRequest:          guardCodexRequest,
		HeaderFacts:           applyCodexHeaderFacts,
		RetryAfterFromHeaders: codexRetryAfter,
		RetryAfterFromBody:    codexRetryAfterFromBody,
		FinalizeFacts:         finalizeCodexFacts,
		// 真机实测契约（2026-09-03）：后端只收流式 + 结构化 input；出站前统一规范化，
		// 非流式入站由 base adapter 流式聚合还原（openai chat 桥接与 SDK 直连自动受益）。
		NormalizeRequest: normalizeCodexRequest,
		// 会话亲和：按客户 × 账号 × 会话键派生上游会话身份，写入 prompt_cache_key 与亲和头
		// （见 session_affinity.go）。
		BindSession:    bindCodexSession,
		ForceStreaming: true,
	}
	if clientFor != nil {
		wire.ClientFor = func(ch channel.Runtime) *http.Client {
			// 出站代理回退链由 Runtime.OutboundProxyURL 决定（clientFor("") 返回缺省 client）。
			return clientFor(ch.OutboundProxyURL())
		}
	}
	return openairesponses.NewAdapterWithWire(client, wire)
}

// codexDecorator 构造 Codex 订阅后端的请求头装饰器：账号身份、客户端身份（codexidentity 同源
// 渲染 originator / User-Agent / version）与会话亲和头。
//
// Authorization 已由 base 用 Runtime.APIKey（= 账号 access token）设置。
// 防御性剥离两类头：入站鉴权头不会到这里（adapter 全新构造请求），但客户可能经
// client_metadata 以外的途径回带 x-codex-turn-state——那是按账号加密的回合状态，
// 跨账号回放会串号，这里保证它绝不出站。
// 会话亲和头与 BindSession 写入 body 的 prompt_cache_key 同源派生（httpReq 的 ctx 即出站 ctx）。
func codexDecorator(version codexidentity.VersionSource) func(httpReq *http.Request, ch channel.Runtime) {
	return func(httpReq *http.Request, ch channel.Runtime) {
		httpReq.Header.Del("x-codex-turn-state")
		if ch.Account.UpstreamAccountID != "" {
			httpReq.Header.Set("chatgpt-account-id", ch.Account.UpstreamAccountID)
		}
		codexidentity.Resolve(version).ApplyInferenceHeaders(httpReq.Header)
		applySessionAffinityHeaders(httpReq, ch, upstreamSessionID(httpReq.Context(), ch))
	}
}

// guardCodexRequest 拒绝带 previous_response_id 的请求（边界 25）。
//
// Codex 后端 store=false，不保存响应，previous_response_id 在上游会得到 400。
// 在出站前拦截并给出同样的 400 语义，避免白白消耗一次账号出站与上游风控额度。
func guardCodexRequest(req openairesponses.Request) error {
	var probe struct {
		PreviousResponseID *string `json:"previous_response_id"`
	}
	if err := json.Unmarshal(req.Body, &probe); err != nil {
		// 无法解析的 body 交给上游拒绝，不在守卫处猜。
		return nil
	}
	if probe.PreviousResponseID == nil {
		return nil
	}
	return adapter.NewUpstreamError(
		adapter.UpstreamErrorBadRequest,
		adapter.UpstreamMetadata{
			StatusCode:   http.StatusBadRequest,
			ErrorCode:    "previous_response_id_unsupported",
			ErrorMessage: "previous_response_id is not supported on this channel",
		},
		failure.New(
			failure.CodeAdapterUpstreamStatus,
			failure.WithMessage("codex responses adapter rejects previous_response_id (upstream does not store responses)"),
		),
	)
}

// finalizeCodexFacts 把结算档位改为出站请求档位权威（边界 15 的 Fast 结算例外）。
//
// Codex 订阅后端对 service_tier=priority 的请求仍回 auto/default——响应档位不可信。
// 按「响应事实结算」的总原则在此 wire 开例外：出站带 priority/fast 即按 Fast 结算，
// 否则按 Standard；响应原始值保留在 UpstreamRaw 供审计对照。是否真的按 Fast 计费
// 仍受结算侧「Fast 价格已配置」闸门约束（未配置回落 Standard，方向少收不多收）。
func finalizeCodexFacts(req openairesponses.Request, facts *adapter.ResponseFacts) {
	var probe struct {
		ServiceTier *string `json:"service_tier"`
	}
	if err := json.Unmarshal(req.Body, &probe); err != nil {
		return
	}
	facts.ServiceTier = servicetier.ResolveWireOutboundAuthoritative(probe.ServiceTier, facts.ServiceTier.UpstreamRaw)
}

// applyCodexHeaderFacts 解析 x-codex-* 用量头（upstream-usage-headers.json 逐字段对照）。
//
// primary = 5h 窗口、secondary = 7d 窗口——这是实测口径；Sub2API 源码把两者弄反，勿照抄。
// 任一窗口都可能缺失（Business Premium 无 5h 窗口、Enterprise/Edu 弹性额度），缺失时
// Present=false，消费方不得臆断水位。
func applyCodexHeaderFacts(header http.Header, facts *adapter.ResponseFacts) {
	usage := adapter.AccountUsageFacts{
		PlanType:  strings.TrimSpace(header.Get("x-codex-plan-type")),
		Primary:   parseCodexUsageWindow(header, "x-codex-primary"),
		Secondary: parseCodexUsageWindow(header, "x-codex-secondary"),
	}
	if usage.PlanType == "" && !usage.Primary.Present && !usage.Secondary.Present {
		return
	}
	facts.AccountUsage = &usage
}

// codexRetryAfter 从 429 响应头解析账号冷却时长（归因分层：429 归账号，优先取上游重置时刻）。
// 优先 reset-after-seconds 相对秒（免时钟偏差），缺失时用 reset-at 绝对时间戳减当前时间。
func codexRetryAfter(header http.Header) time.Duration {
	if v, err := strconv.ParseInt(strings.TrimSpace(header.Get("x-codex-primary-reset-after-seconds")), 10, 64); err == nil && v > 0 {
		return time.Duration(v) * time.Second
	}
	if v, err := strconv.ParseInt(strings.TrimSpace(header.Get("x-codex-primary-reset-at")), 10, 64); err == nil && v > 0 {
		if until := time.Until(time.Unix(v, 0)); until > 0 {
			return until
		}
	}
	return 0
}

// codexRetryAfterFromBody 从 429 错误体解析重置时刻（x-codex 头缺失时的兜底，与 sub2api 对齐）。
// 已知形状：{"error":{"type":"usage_limit_reached"|"rate_limit_exceeded","resets_at":<unix>,
// "resets_in_seconds":<sec>,...}}。解析不出返回 0（上层落秒级兜底冷却）。
func codexRetryAfterFromBody(statusCode int, snippet string) time.Duration {
	if statusCode != http.StatusTooManyRequests || snippet == "" {
		return 0
	}
	var payload struct {
		Error struct {
			Type            string          `json:"type"`
			ResetsAt        json.Number     `json:"resets_at"`
			ResetsInSeconds json.RawMessage `json:"resets_in_seconds"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(snippet), &payload); err != nil {
		return 0
	}
	switch payload.Error.Type {
	case "usage_limit_reached", "rate_limit_exceeded":
	default:
		return 0
	}
	var resetsIn int64
	if len(payload.Error.ResetsInSeconds) > 0 {
		_ = json.Unmarshal(payload.Error.ResetsInSeconds, &resetsIn)
	}
	if resetsIn > 0 {
		return time.Duration(resetsIn) * time.Second
	}
	if ts, err := payload.Error.ResetsAt.Int64(); err == nil && ts > 0 {
		if until := time.Until(time.Unix(ts, 0)); until > 0 {
			return until
		}
	}
	return 0
}

// parseCodexUsageWindow 解析一个用量窗口的四个头；used-percent 缺失即视为窗口不存在。
func parseCodexUsageWindow(header http.Header, prefix string) adapter.AccountUsageWindowFacts {
	usedRaw := strings.TrimSpace(header.Get(prefix + "-used-percent"))
	if usedRaw == "" {
		return adapter.AccountUsageWindowFacts{}
	}
	used, err := strconv.ParseFloat(usedRaw, 64)
	if err != nil {
		return adapter.AccountUsageWindowFacts{}
	}
	window := adapter.AccountUsageWindowFacts{Present: true, UsedPercent: used}
	if v, err := strconv.ParseInt(strings.TrimSpace(header.Get(prefix+"-window-minutes")), 10, 64); err == nil {
		window.WindowMinutes = v
	}
	if v, err := strconv.ParseInt(strings.TrimSpace(header.Get(prefix+"-reset-at")), 10, 64); err == nil {
		window.ResetAtUnix = v
	}
	if v, err := strconv.ParseInt(strings.TrimSpace(header.Get(prefix+"-reset-after-seconds")), 10, 64); err == nil {
		window.ResetAfterSeconds = v
	}
	return window
}
