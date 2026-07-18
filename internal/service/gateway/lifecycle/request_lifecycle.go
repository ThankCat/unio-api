package lifecycle

import (
	"context"
	"time"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/core/auth"
	"github.com/ThankCat/unio-gateway/internal/core/requestlog"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
	"github.com/ThankCat/unio-gateway/internal/platform/observability/logfields"
	"github.com/ThankCat/unio-gateway/internal/platform/observability/metrics"
)

// RequestLifecycle 把双协议 service 编排骨架共享的协议无关基础设施集中到一处。
//
// 它持有 request log / metrics / channel breaker / chat authorizer 这些 lifecycle 依赖，
// 以及单一协议特定的取值（IngressProtocol、Operation、SafeMessageMapper），统一暴露给两侧
// service 编排骨架使用。这样 OpenAI ChatCompletions 与 Anthropic Messages 各自的
// channel_breaker / *_authorization / *_metrics / *_request_record thin wrapper 实现可以收口
// 为同一份代码——两侧 service 只保留协议族命名的 1-line forward，避免逐字复制。
//
// 创建方式：见 NewRequestLifecycle。两侧 bootstrap 不需要直接构造它，service.go 在懒初始化
// 阶段（lc()）根据已注入字段组装一次。
type RequestLifecycle struct {
	requestLog      requestlog.Service
	authorizer      ChatAuthorizer
	metrics         MetricsRecorder
	breaker         ChannelBreaker
	cooldowns       *ChannelCooldownRegistry
	credentialGate  CredentialGate
	ingressProtocol requestlog.Protocol
	operation       requestlog.Operation
	safeMessage     func(code string) string

	// costExposures 是可选的成本敞口记录器（DESIGN-bill-on-cancel 阶段一）；nil 表示不启用。
	costExposures              CostExposureRecorder
	costExposureOutputFallback int64
}

// RequestLifecycleParams 是构造 RequestLifecycle 所需的全部字段。
//
// SafeMessage 用于 service-specific ad-hoc string code（例如 "chat_authorization_failed" /
// "messages_settlement_failed"）的文案映射；未命中时由内部 fall through 到
// BaseSafeRequestLogErrorMessage。允许为 nil，此时一律 fall through 到协议无关兜底。
type RequestLifecycleParams struct {
	RequestLog      requestlog.Service
	Authorizer      ChatAuthorizer
	Metrics         MetricsRecorder
	Breaker         ChannelBreaker
	IngressProtocol requestlog.Protocol
	Operation       requestlog.Operation
	SafeMessage     func(code string) string
}

// NewRequestLifecycle 构造一个协议无关编排基础设施 bundle。
//
// RequestLog 必填；Authorizer 必填；其余字段允许为 nil（Metrics、Breaker、SafeMessage 缺省
// 都等价于「不采集 / 不熔断 / 仅按协议无关兜底文案」）。IngressProtocol 与 Operation 必填，
// 它们决定 request_records 的协议归属与 operation 列。
func NewRequestLifecycle(params RequestLifecycleParams) *RequestLifecycle {
	if params.RequestLog == nil {
		panic("lifecycle: request log service is required")
	}
	if params.Authorizer == nil {
		panic("lifecycle: chat authorizer is required")
	}
	if params.IngressProtocol == "" {
		panic("lifecycle: ingress protocol is required")
	}
	if params.Operation == "" {
		panic("lifecycle: operation is required")
	}

	return &RequestLifecycle{
		requestLog:      params.RequestLog,
		authorizer:      params.Authorizer,
		metrics:         params.Metrics,
		breaker:         params.Breaker,
		ingressProtocol: params.IngressProtocol,
		operation:       params.Operation,
		safeMessage:     params.SafeMessage,
	}
}

// ---------------------------------------------------------------------------
// channel breaker：双协议共享熔断只读判定与状态记录。
// ---------------------------------------------------------------------------

// CandidateAvailable 在启用熔断时只读判断候选是否可进入保守 fallback plan。
// 不占用 half-open 探测名额；真正尝试前必须继续 BreakerAllow。
//
// nil receiver / nil breaker 都等价于「未启用熔断」，全部放行——这与抽取前
// service.candidateAvailable 的语义一致，也允许只关心 candidate planning 的单元测试
// 用字面量构造 service 时不必同时构造 RequestLifecycle。
//
// 同时考虑 P2-7 渠道级 429 冷却：处于冷却窗口内的渠道一并排除出 plan。
func (l *RequestLifecycle) CandidateAvailable(candidate routing.ChatRouteCandidate) bool {
	if l == nil {
		return true
	}
	channelKey := MetricsID(candidate.Channel.ID)
	if !l.cooldowns.Allowed(channelKey) {
		return false
	}
	if l.breaker == nil {
		return true
	}

	return l.breaker.Available(channelKey)
}

// BreakerAllow 在启用熔断时判断是否允许尝试该 channel；未启用时始终放行。
// nil receiver / nil breaker 等价于「未启用熔断」，全部放行。
//
// 同时考虑 P2-7 渠道级 429 冷却：处于冷却窗口内的渠道直接跳过，不占用 half-open 探测名额。
func (l *RequestLifecycle) BreakerAllow(channelKey string) bool {
	if l == nil {
		return true
	}
	if !l.cooldowns.Allowed(channelKey) {
		return false
	}
	if l.breaker == nil {
		return true
	}

	return l.breaker.Allow(channelKey)
}

// SetChannelCooldownRegistry 注入 P2-7 渠道级 429 冷却注册表；nil 表示不启用冷却。
func (l *RequestLifecycle) SetChannelCooldownRegistry(registry *ChannelCooldownRegistry) {
	if l == nil {
		return
	}
	l.cooldowns = registry
}

// RecordChannelRateLimit 在上游返回 429 时按 Retry-After 登记渠道冷却（P2-7）。
//
// 仅对 rate_limit 分类生效；其它错误 no-op。nil 冷却注册表（未启用）时 no-op。
// 返回是否成功登记冷却，便于调用方观测/记录。
func (l *RequestLifecycle) RecordChannelRateLimit(channelKey string, err error) bool {
	if l == nil || l.cooldowns == nil {
		return false
	}
	retryAfter, ok := channelRateLimitRetryAfter(err)
	if !ok {
		return false
	}
	_, recorded := l.cooldowns.RecordRateLimit(channelKey, retryAfter)
	return recorded
}

// RecordChannelFailureCooldown 在上游 timeout/5xx 类故障后登记渠道失败软冷却（DEC-029）。
//
// 仅对 timeout/server_error 分类生效；其它错误 no-op。nil 冷却注册表（未启用）时 no-op。
// 软冷却只影响后续请求的候选排序偏好（demote），不会把候选池清空（唯一渠道保护）。
func (l *RequestLifecycle) RecordChannelFailureCooldown(channelKey string, err error) bool {
	if l == nil || l.cooldowns == nil {
		return false
	}
	if !isFailureCooldownError(err) {
		return false
	}
	_, recorded := l.cooldowns.RecordFailure(channelKey)
	return recorded
}

// CandidateFailurePreferred 报告候选渠道当前是否不在失败软冷却窗口内（true=可正常优先使用）。
//
// 供 PrepareCandidates 的 FailurePreferred 软偏好使用：软冷却中的候选被 demote 到 fallback
// 顺序末尾而非剔除，故唯一候选时行为不变（DEC-029）。
func (l *RequestLifecycle) CandidateFailurePreferred(candidate routing.ChatRouteCandidate) bool {
	if l == nil || l.cooldowns == nil {
		return true
	}
	return l.cooldowns.FailurePreferred(MetricsID(candidate.Channel.ID))
}

// ChannelHealthScore 返回某 channel 的健康分（越小越健康），供 stable 线路排序。
// nil receiver / nil breaker 等价于「无健康统计」，返回 0（不改变排序）。
func (l *RequestLifecycle) ChannelHealthScore(channelKey string) float64 {
	if l == nil || l.breaker == nil {
		return 0
	}

	return l.breaker.HealthScore(channelKey)
}

// RecordChannelHealth 按错误分类把一次上游尝试结果记入熔断器。
// 只有归因于 channel 的失败才计为失败；bad_request/canceled 等不惩罚渠道。
// 每次被放行的尝试都必须恰好记录一次，以正确收口 half-open 探测。
//
// nil receiver / nil breaker 等价于「未启用熔断」，no-op。
func (l *RequestLifecycle) RecordChannelHealth(channelKey string, err error) {
	if l == nil || l.breaker == nil {
		return
	}

	if IsChannelFaultError(err) {
		l.breaker.RecordFailure(channelKey)
		return
	}

	l.breaker.RecordSuccess(channelKey)
}

// SetCredentialGate 注入凭据失效闸门（连续 401 翻 credential_valid=false）。nil 表示不启用。
// 与 SetChannelCooldownRegistry 一样是可选的启动期后置注入。
func (l *RequestLifecycle) SetCredentialGate(gate CredentialGate) {
	if l == nil {
		return
	}
	l.credentialGate = gate
}

// RecordCredentialResult 把一次上游尝试结果喂给凭据闸门（按 channel id）。
// nil receiver / nil gate 等价于「未启用凭据闸门」，no-op。
func (l *RequestLifecycle) RecordCredentialResult(channelID int64, err error) {
	if l == nil || l.credentialGate == nil {
		return
	}
	l.credentialGate.RecordResult(channelID, err)
}

// ---------------------------------------------------------------------------
// authorization release：脱离客户端取消 context，给冻结余额释放留补偿窗口。
// ---------------------------------------------------------------------------

// ReleaseAuthorization 脱离客户端取消上下文释放冻结余额。
// 用于请求未进入成功扣费语义、且不存在已产生上游成本风险的边界。
func (l *RequestLifecycle) ReleaseAuthorization(ctx context.Context, authorization ChatAuthorization) error {
	if authorization.RequestRecordID == 0 || authorization.ReservationID == 0 {
		return nil
	}

	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	return l.authorizer.ReleaseChat(releaseCtx, ChatReleaseAuthorizationParams{
		RequestRecordID: authorization.RequestRecordID,
		ReservationID:   authorization.ReservationID,
	})
}

// ReleaseAuthorizationForBillingException 脱离客户端取消上下文释放冻结余额并记录账务异常事实。
// 它用于 stream 已可能产生上游成本、但没有可靠 final usage、因而不能扣用户余额的边界。
func (l *RequestLifecycle) ReleaseAuthorizationForBillingException(ctx context.Context, authorization ChatAuthorization, reasonCode string, reason string) error {
	if authorization.RequestRecordID == 0 || authorization.ReservationID == 0 {
		return nil
	}

	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	return l.authorizer.ReleaseChatForBillingException(releaseCtx, ChatReleaseBillingExceptionParams{
		RequestRecordID: authorization.RequestRecordID,
		ReservationID:   authorization.ReservationID,
		ReasonCode:      reasonCode,
		Reason:          reason,
	})
}

// ---------------------------------------------------------------------------
// metrics：协议无关业务指标上报；metrics 为 nil 时全部 no-op。
// ---------------------------------------------------------------------------

// RecordRequest 记录一次请求最终结果（流式或非流式 + 业务 outcome）。
//
// nil receiver / nil metrics 等价于「不采集业务指标」，no-op。Record* 系列均如此。
func (l *RequestLifecycle) RecordRequest(stream bool, outcome metrics.ChatOutcome) {
	if l == nil || l.metrics == nil {
		return
	}

	l.metrics.IncChatRequest(stream, outcome)
}

// RecordRoutingSelected 记录一次实际选中的 provider/channel/model。
func (l *RequestLifecycle) RecordRoutingSelected(providerID int64, channelID int64, model string) {
	if l == nil || l.metrics == nil {
		return
	}

	l.metrics.IncRoutingSelected(MetricsID(providerID), MetricsID(channelID), model)
}

// RecordUpstream 记录一次上游 adapter 调用的结果、错误分类和耗时。
// err 为 nil 表示调用成功；否则用 adapter.UpstreamCategoryOf 提取稳定上游错误分类。
func (l *RequestLifecycle) RecordUpstream(providerID int64, channelID int64, duration time.Duration, err error) {
	if l == nil || l.metrics == nil {
		return
	}

	if err == nil {
		l.metrics.ObserveUpstream(MetricsID(providerID), MetricsID(channelID), true, "", duration)
		return
	}

	category, _ := adapter.UpstreamCategoryOf(err)
	l.metrics.ObserveUpstream(MetricsID(providerID), MetricsID(channelID), false, string(category), duration)
}

// RecordSettlement 记录一次结算调用的结果。
func (l *RequestLifecycle) RecordSettlement(outcome metrics.SettlementOutcome) {
	if l == nil || l.metrics == nil {
		return
	}

	l.metrics.IncSettlement(outcome)
}

// RecordStreamEvent 记录一次流式请求生命周期事件。
func (l *RequestLifecycle) RecordStreamEvent(event metrics.StreamEvent) {
	if l == nil || l.metrics == nil {
		return
	}

	l.metrics.IncStreamEvent(event)
}

// RecordPartialSettlement 记录一次流式 partial settlement（P2-2 监控偏少收/滥用）。
func (l *RequestLifecycle) RecordPartialSettlement(reason string) {
	if l == nil || l.metrics == nil {
		return
	}

	l.metrics.IncPartialSettlement(reason)
}

// RecordRetryableFallback 记录一次因可重试上游错误而切换候选（P2-3 监控前序候选潜在未计费成本）。
// err 为触发 fallback 的上游错误；用 adapter.UpstreamCategoryOf 提取稳定分类。
func (l *RequestLifecycle) RecordRetryableFallback(err error) {
	if l == nil || l.metrics == nil {
		return
	}

	category, _ := adapter.UpstreamCategoryOf(err)
	l.metrics.IncRetryableFallback(string(category))
}

// RecordZeroPriceServed 记录一次以零售价（客户侧 $0）成功结算的请求（P2-4 零价渠道误配告警）。
func (l *RequestLifecycle) RecordZeroPriceServed(providerID int64, channelID int64, model string) {
	if l == nil || l.metrics == nil {
		return
	}

	l.metrics.IncZeroPriceServed(MetricsID(providerID), MetricsID(channelID), model)
}

// ---------------------------------------------------------------------------
// request log：创建 / 推进 / 失败 / 取消，按构造时的 IngressProtocol + Operation 写。
// ---------------------------------------------------------------------------

// CreateRequest 创建用户可见请求记录，并立即推进到 running 状态。
// request_records.request_id 由服务端生成，用作数据库唯一事实 ID；
// HTTP X-Request-ID 只作为日志 correlation id，不能直接复用为账务请求 ID。
// reasoning 为归一后的推理强度（协议编排从请求 DTO 提取）；线路快照取自 principal.RouteID，
// 客户端 IP 取自 ctx（gateway ClientIP 中间件写入）。
func (l *RequestLifecycle) CreateRequest(ctx context.Context, principal *auth.APIKeyPrincipal, requestedModelID string, stream bool, reasoning ReasoningInfo) (requestlog.RequestRecord, error) {
	requestID, err := requestlog.GenerateRequestID()
	if err != nil {
		return requestlog.RequestRecord{}, err
	}

	// 访问日志：request 创建时即可确定的维度先写入；provider/channel 等 CreateAttempt。
	logfields.SetRequestID(ctx, requestID)
	logfields.SetModel(ctx, requestedModelID)

	var routeID *int64
	if principal != nil {
		routeID = principal.RouteID
	}
	if routeID != nil {
		logfields.SetRouteID(ctx, *routeID)
	}
	var clientIP *string
	if ip := httpx.ClientIP(ctx); ip != "" {
		clientIP = &ip
	}

	record, err := l.requestLog.CreateRequest(ctx, requestlog.CreateRequestParams{
		RequestID:             requestID,
		UserID:                principal.UserID,
		APIKeyID:              principal.APIKeyID,
		RequestedModelID:      requestedModelID,
		IngressProtocol:       l.ingressProtocol,
		Operation:             l.operation,
		Stream:                stream,
		StartedAt:             time.Now(),
		RouteID:               routeID,
		ReasoningEffort:       reasoning.Effort,
		ReasoningBudgetTokens: reasoning.BudgetTokens,
		ClientIP:              clientIP,
	})
	if err != nil {
		return requestlog.RequestRecord{}, err
	}

	record, err = l.requestLog.MarkRequestRunning(ctx, record.ID)
	if err != nil {
		return requestlog.RequestRecord{}, err
	}

	return record, nil
}

// CreateAttempt 创建一次上游 channel 尝试记录。
// attempt 记录 fallback 链路中的单次 provider/channel 调用，必须先于 adapter 调用创建。
func (l *RequestLifecycle) CreateAttempt(ctx context.Context, requestRecord requestlog.RequestRecord, attemptIndex int, candidate routing.ChatRouteCandidate) (requestlog.AttemptRecord, error) {
	// 覆盖为当前尝试；失败停在某次 attempt 时访问日志即显示最后打过的渠道。
	logfields.SetUpstreamAttempt(ctx, logfields.UpstreamAttempt{
		ModelID:    candidate.ModelDBID,
		Router:     candidate.RouteName,
		ProviderID: candidate.ProviderID,
		Provider:   candidate.Channel.ProviderSlug,
		ChannelID:  candidate.Channel.ID,
		Channel:    candidate.Channel.Name,
	})

	return l.requestLog.CreateAttempt(ctx, requestlog.CreateAttemptParams{
		RequestRecordID:  requestRecord.ID,
		AttemptIndex:     attemptIndex,
		ProviderID:       candidate.ProviderID,
		ChannelID:        candidate.Channel.ID,
		AdapterKey:       candidate.AdapterKey,
		UpstreamModel:    candidate.UpstreamModel,
		UpstreamProtocol: requestlog.Protocol(candidate.Protocol),
		StartedAt:        time.Now(),
	})
}

// MarkResponseStarted 尽力记录首个客户可见响应时间。
//
// 该写入只服务观测指标，不能影响主响应链路：流式请求已经开始向客户发数据时，写审计字段失败
// 不应中断 SSE。
func (l *RequestLifecycle) MarkResponseStarted(ctx context.Context, requestRecord requestlog.RequestRecord, attemptRecord requestlog.AttemptRecord, responseStartedAt time.Time) {
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()

	_, _ = l.requestLog.MarkRequestResponseStarted(auditCtx, requestlog.MarkResponseStartedParams{
		ID:                requestRecord.ID,
		ResponseStartedAt: responseStartedAt,
	})
	_, _ = l.requestLog.MarkAttemptResponseStarted(auditCtx, requestlog.MarkAttemptResponseStartedParams{
		ID:                attemptRecord.ID,
		ResponseStartedAt: responseStartedAt,
	})
}

// deliveryStatusMarker 是交付状态机的可选写入能力。
//
// 生产注入的 requestlog.Service 实现（*requestlog.Store）满足它；测试 fake 不实现时交付写入静默跳过。
// 交付状态只服务审计/展示（Admin），与 settlement 状态相互独立，写失败绝不影响主响应链路。
type deliveryStatusMarker interface {
	MarkRequestDeliveryCompleted(ctx context.Context, id int64, completedAt time.Time) (requestlog.RequestRecord, error)
	MarkRequestDeliveryInterrupted(ctx context.Context, id int64) (requestlog.RequestRecord, error)
}

// MarkDeliveryCompleted 尽力把请求交付状态推进到 completed（响应已完整交给客户写出路径）。
// 最佳努力审计：脱离请求 ctx 取消，写失败不影响已成功的主链路与账务。
func (l *RequestLifecycle) MarkDeliveryCompleted(ctx context.Context, requestRecord requestlog.RequestRecord) {
	marker, ok := l.requestLog.(deliveryStatusMarker)
	if !ok {
		return
	}

	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()

	_, _ = marker.MarkRequestDeliveryCompleted(auditCtx, requestRecord.ID, time.Now())
}

// MarkDeliveryInterrupted 尽力把请求交付状态推进到 interrupted（客户端取消、上游中断或尾部错误，
// 客户未拿到完整响应）。最佳努力审计，写失败不影响主链路与账务。
func (l *RequestLifecycle) MarkDeliveryInterrupted(ctx context.Context, requestRecord requestlog.RequestRecord) {
	marker, ok := l.requestLog.(deliveryStatusMarker)
	if !ok {
		return
	}

	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()

	_, _ = marker.MarkRequestDeliveryInterrupted(auditCtx, requestRecord.ID)
}

// MarkRequestFailed 把 request record 标记为失败。
// 失败状态写入是审计动作，调用方仍然返回原始业务错误，避免状态写入细节覆盖根因。
func (l *RequestLifecycle) MarkRequestFailed(ctx context.Context, requestRecord requestlog.RequestRecord, fallbackCode string, err error) {
	errorCode, safeMessage, internalDetail := l.requestLogErrorFacts(fallbackCode, err)

	_, _ = l.requestLog.MarkRequestFailed(ctx, requestlog.MarkRequestFailedParams{
		ID:                  requestRecord.ID,
		ErrorCode:           errorCode,
		ErrorMessage:        safeMessage,
		InternalErrorDetail: internalDetail,
		CompletedAt:         time.Now(),
	})
}

// MarkAttemptFailed 把一次上游尝试标记为失败。
// 失败状态写入是审计动作，调用方仍然返回原始业务错误，避免状态写入细节覆盖根因。
func (l *RequestLifecycle) MarkAttemptFailed(ctx context.Context, attempt requestlog.AttemptRecord, fallbackCode string, err error) {
	errorCode, safeMessage, internalDetail := l.requestLogErrorFacts(fallbackCode, err)
	var upstreamStatusCode *int
	var upstreamRequestID *string
	if meta, ok := adapter.UpstreamMetadataOf(err); ok {
		if meta.StatusCode >= 100 && meta.StatusCode <= 599 {
			statusCode := meta.StatusCode
			upstreamStatusCode = &statusCode
		}
		upstreamRequestID = UpstreamRequestIDPtr(meta.RequestID)
	}

	_, _ = l.requestLog.MarkAttemptFailed(ctx, requestlog.MarkAttemptFailedParams{
		ID:                  attempt.ID,
		UpstreamStatusCode:  upstreamStatusCode,
		UpstreamRequestID:   upstreamRequestID,
		ErrorCode:           errorCode,
		ErrorMessage:        safeMessage,
		InternalErrorDetail: internalDetail,
		CompletedAt:         time.Now(),
	})
}

// MarkRequestCanceled 把 request record 和当前 attempt 标记为客户端取消。
// 账务 release 或 risk_exposure 由调用方在进入 canceled 状态前处理；这里仅写请求审计状态。
//
// 客户端断开时原请求 ctx 通常已经取消；这里脱离请求取消，
// 给审计写入一个很短的补偿窗口，避免 canceled 状态写不进去。
func (l *RequestLifecycle) MarkRequestCanceled(ctx context.Context, requestRecord requestlog.RequestRecord, attemptRecord requestlog.AttemptRecord, err error) {
	errorCode, safeMessage, internalDetail := l.requestLogCancelFacts(err)

	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()

	_, _ = l.requestLog.MarkAttemptCanceled(auditCtx, requestlog.MarkAttemptCanceledParams{
		ID:                  attemptRecord.ID,
		ErrorCode:           errorCode,
		ErrorMessage:        safeMessage,
		InternalErrorDetail: internalDetail,
		CompletedAt:         time.Now(),
	})

	_, _ = l.requestLog.MarkRequestCanceled(auditCtx, requestlog.MarkRequestCanceledParams{
		ID:                  requestRecord.ID,
		ErrorCode:           errorCode,
		ErrorMessage:        safeMessage,
		InternalErrorDetail: internalDetail,
		CompletedAt:         time.Now(),
	})
}

// requestLogErrorFacts 生成 request log 的安全错误摘要和内部诊断详情。
// error_message 只保存可展示文案；internal_error_detail 才保存截断后的内部错误文本。
//
// 协议无关 helper：FailureCodeOrFallback / InternalErrorDetail 由 lifecycle 提供；
// safeMessage 先尝试 service-specific ad-hoc string code 映射（构造时注入），
// 未命中再 fall through 到 BaseSafeRequestLogErrorMessage。
func (l *RequestLifecycle) requestLogErrorFacts(fallbackCode string, err error) (errorCode string, safeMessage string, internalDetail string) {
	errorCode = FailureCodeOrFallback(err, fallbackCode)
	return errorCode, l.safeMessageFor(errorCode), InternalErrorDetail(err)
}

// requestLogCancelFacts 生成客户端取消的 request log 事实。
// 取消不是上游故障：adapter 可能把 context.Canceled 包装成 adapter_send_request_failed，
// 但审计字段仍固定为 client_canceled；internal_error_detail 保留 unwrap 链供排查。
func (l *RequestLifecycle) requestLogCancelFacts(err error) (errorCode string, safeMessage string, internalDetail string) {
	const cancelCode = "client_canceled"
	return cancelCode, l.safeMessageFor(cancelCode), InternalErrorDetail(err)
}

// safeMessageFor 优先调用构造时注入的 service-specific 映射；未命中（空串）才回退到协议无关兜底。
//
// 注入 closure 的语义约定：返回非空字符串表示命中并直接用；返回空字符串表示「未识别此 ad-hoc
// code，请协议无关兜底处理」。这样两侧 service 既能管理自己的局部 string code 文案，又不需要
// 把 BaseSafeRequestLogErrorMessage 的所有 case 复制一份。
func (l *RequestLifecycle) safeMessageFor(code string) string {
	if l.safeMessage != nil {
		if msg := l.safeMessage(code); msg != "" {
			return msg
		}
	}
	return BaseSafeRequestLogErrorMessage(code)
}
