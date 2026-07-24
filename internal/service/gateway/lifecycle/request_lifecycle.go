package lifecycle

import (
	"context"
	"time"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/core/auth"
	"github.com/ThankCat/unio-gateway/internal/core/requestlog"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
	"github.com/ThankCat/unio-gateway/internal/platform/observability/logfields"
	"github.com/ThankCat/unio-gateway/internal/platform/observability/metrics"
)

// RequestLifecycle 把双协议 service 编排骨架共享的协议无关基础设施集中到一处。
//
// 它持有 request log / metrics / chat authorizer 这些 lifecycle 依赖，
// 以及单一协议特定的取值（IngressProtocol、Endpoint、SafeMessageMapper），统一暴露给两侧
// service 编排骨架使用，使 OpenAI ChatCompletions 与 Anthropic Messages 的授权、指标和
// request record 行为收口为同一份代码。
//
// 创建方式：见 NewRequestLifecycle。两侧 bootstrap 不需要直接构造它，service.go 在懒初始化
// 阶段（lc()）根据已注入字段组装一次。
type RequestLifecycle struct {
	requestLog      requestlog.Service
	authorizer      ChatAuthorizer
	metrics         MetricsRecorder
	credentialGate  CredentialGate
	routingTraces   *RoutingTraceRecorder
	ingressProtocol requestlog.Protocol
	endpoint       requestlog.Endpoint
	safeMessage     func(code string) string

	// costExposures 是可选的成本敞口记录器（DESIGN-bill-on-cancel 阶段一）；nil 表示不启用。
	costExposures              CostExposureRecorder
	costExposureOutputFallback int64
}

// SetRoutingTraceRecorder 注入请求级路由决策持久化器。
func (l *RequestLifecycle) SetRoutingTraceRecorder(recorder *RoutingTraceRecorder) {
	if l != nil {
		l.routingTraces = recorder
	}
}

// RecordRoutingDecision 按采样/异常策略写 trace；写失败不影响客户请求。
func (l *RequestLifecycle) RecordRoutingDecision(ctx context.Context, in RoutingDecisionTraceInput) {
	if l == nil {
		return
	}
	l.recordRoutingPlan(in)
	if l.routingTraces != nil {
		l.routingTraces.Record(context.WithoutCancel(ctx), in)
	}
}

// RecordRoutingFailure 100% 保存候选计划生成前的路由异常（无可用渠道、负毛利全摘除等）。
func (l *RequestLifecycle) RecordRoutingFailure(ctx context.Context, request requestlog.RequestRecord, routeID *int64, err error) {
	if l == nil || routeID == nil {
		return
	}
	marginGuard := false
	for _, field := range failure.FieldsOf(err) {
		if field.Key == "margin_guard_triggered" {
			marginGuard, _ = field.Value.(bool)
		}
	}
	reason := string(failure.CodeOf(err))
	if reason == "" {
		reason = "routing_failure"
	}
	if marginGuard {
		l.recordMarginGuard("runtime_rejected")
	}
	l.RecordRoutingDecision(ctx, RoutingDecisionTraceInput{
		Request: request, RouteID: *routeID, ForceReasons: []string{reason}, MarginGuard: marginGuard,
	})
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
	IngressProtocol requestlog.Protocol
	Endpoint       requestlog.Endpoint
	SafeMessage     func(code string) string
}

// NewRequestLifecycle 构造一个协议无关编排基础设施 bundle。
//
// RequestLog 必填；Authorizer 必填；其余字段允许为 nil（Metrics、SafeMessage 缺省
// 等价于「不采集 / 仅按协议无关兜底文案」）。IngressProtocol 与 Endpoint 必填，
// 它们决定 request_records 的协议归属与 endpoint 列。
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
	if params.Endpoint == "" {
		panic("lifecycle: endpoint is required")
	}

	return &RequestLifecycle{
		requestLog:      params.RequestLog,
		authorizer:      params.Authorizer,
		metrics:         params.Metrics,
		ingressProtocol: params.IngressProtocol,
		endpoint:       params.Endpoint,
		safeMessage:     params.SafeMessage,
	}
}

// SetCredentialGate 注入凭据失效闸门（连续 401 翻 credential_valid=false）。nil 表示不启用。
// 这是可选的启动期后置注入。
func (l *RequestLifecycle) SetCredentialGate(gate CredentialGate) {
	if l == nil {
		return
	}
	l.credentialGate = gate
}

// RecordCredentialResult 把一次上游尝试结果连同其冻结的 Channel/Origin 版本喂给凭据闸门。
// nil receiver / nil gate 等价于「未启用凭据闸门」，no-op。
func (l *RequestLifecycle) RecordCredentialResult(candidate routing.ChatRouteCandidate, err error) {
	if l == nil || l.credentialGate == nil {
		return
	}
	l.credentialGate.RecordResult(CredentialRevision{
		ChannelID:               candidate.Channel.ID,
		ChannelConfigRevision:   candidate.ChannelConfigRevision,
		OriginBaseURLRevision: candidate.ProviderOriginBaseURLRevision,
		OriginStatusRevision:  candidate.ProviderOriginStatusRevision,
	}, err)
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
	if l == nil {
		return
	}
	if l.metrics == nil {
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
// request log：创建 / 推进 / 失败 / 取消，按构造时的 IngressProtocol + Endpoint 写。
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
		Endpoint:             l.endpoint,
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
	return l.CreateAttemptForEndpoint(
		ctx,
		requestRecord,
		attemptIndex,
		attemptIndex,
		candidate,
		l.upstreamEndpoint(),
	)
}

// CreateAttemptForEndpoint freezes routing identity separately from the real
// transport sequence. Compact fallback can therefore create two consecutive
// attempts while retaining the same routing candidate index.
func (l *RequestLifecycle) CreateAttemptForEndpoint(
	ctx context.Context,
	requestRecord requestlog.RequestRecord,
	attemptIndex int,
	routingCandidateIndex int,
	candidate routing.ChatRouteCandidate,
	endpoint requestlog.UpstreamEndpoint,
) (requestlog.AttemptRecord, error) {
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
		RequestRecordID:                 requestRecord.ID,
		AttemptIndex:                    attemptIndex,
		ProviderID:                      candidate.ProviderID,
		ChannelID:                       candidate.Channel.ID,
		AdapterKey:                      candidate.AdapterKey,
		UpstreamModel:                   candidate.UpstreamModel,
		UpstreamProtocol:                requestlog.Protocol(candidate.Protocol),
		ProviderOriginID:              positiveInt64Ptr(candidate.ProviderOriginID),
		ProviderOriginBaseURLRevision: positiveInt64Ptr(candidate.ProviderOriginBaseURLRevision),
		ProviderOriginStatusRevision:  positiveInt64Ptr(candidate.ProviderOriginStatusRevision),
		ChannelConfigRevision:           positiveInt64Ptr(candidate.ChannelConfigRevision),
		RoutingCandidateIndex:           nonNegativeIntPtr(routingCandidateIndex),
		UpstreamEndpoint:               endpoint,
		StartedAt:                       time.Now(),
	})
}

func upstreamEndpointForRequest(endpoint requestlog.Endpoint) requestlog.UpstreamEndpoint {
	switch endpoint {
	case requestlog.EndpointChatCompletions:
		return requestlog.UpstreamEndpointChatCompletions
	case requestlog.EndpointResponses:
		return requestlog.UpstreamEndpointResponses
	case requestlog.EndpointMessages:
		return requestlog.UpstreamEndpointMessages
	default:
		return ""
	}
}

func (l *RequestLifecycle) upstreamEndpoint() requestlog.UpstreamEndpoint {
	if l == nil {
		return ""
	}
	return upstreamEndpointForRequest(l.endpoint)
}

func positiveInt64Ptr(value int64) *int64 {
	if value <= 0 {
		return nil
	}
	return &value
}

func nonNegativeIntPtr(value int) *int {
	if value < 0 {
		return nil
	}
	return &value
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

type attemptTimingRecorder interface {
	RecordAttemptTiming(ctx context.Context, params requestlog.RecordAttemptTimingParams) (requestlog.AttemptRecord, error)
}

type attemptBreakerDispositionRecorder interface {
	RecordAttemptBreakerDisposition(ctx context.Context, params requestlog.RecordAttemptBreakerDispositionParams) (requestlog.AttemptRecord, error)
}

// RecordAttemptTiming first-write-wins 地保存 upstream transport 时间事实。
// 该写入脱离客户取消；流式 FirstToken 到达和 adapter 返回各调用一次。
func (l *RequestLifecycle) RecordAttemptTiming(ctx context.Context, attemptRecord requestlog.AttemptRecord, facts AttemptTimingFacts) {
	recorder, ok := l.requestLog.(attemptTimingRecorder)
	if !ok || attemptRecord.ID == 0 {
		return
	}

	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	_, _ = recorder.RecordAttemptTiming(auditCtx, requestlog.RecordAttemptTimingParams{
		ID:                   attemptRecord.ID,
		UpstreamStartedAt:    facts.UpstreamStartedAt,
		UpstreamFirstTokenAt: facts.UpstreamFirstTokenAt,
		UpstreamCompletedAt:  facts.UpstreamCompletedAt,
	})
}

// RecordAttemptBreakerDisposition first-write-wins 保存 Finish applied/stale 结果；审计失败不改写客户结果。
func (l *RequestLifecycle) RecordAttemptBreakerDisposition(ctx context.Context, attemptRecord requestlog.AttemptRecord, origin, channel string) {
	recorder, ok := l.requestLog.(attemptBreakerDispositionRecorder)
	if !ok || attemptRecord.ID == 0 {
		return
	}
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	_, _ = recorder.RecordAttemptBreakerDisposition(auditCtx, requestlog.RecordAttemptBreakerDispositionParams{
		ID:                  attemptRecord.ID,
		OriginDisposition: origin,
		ChannelDisposition:  channel,
	})
}

// MarkDeliveryCompleted 尽力把请求交付状态推进到 completed（响应已完整交给客户写出路径）。
// 最佳努力审计：脱离请求 ctx 取消，写失败不影响已成功的主链路与账务。
func (l *RequestLifecycle) MarkDeliveryCompleted(ctx context.Context, requestRecord requestlog.RequestRecord) {
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()

	_, _ = l.requestLog.MarkRequestDeliveryCompleted(auditCtx, requestRecord.ID, time.Now())
}

// MarkDeliveryInterrupted 尽力把请求交付状态推进到 interrupted（客户端取消、上游中断或尾部错误，
// 客户未拿到完整响应）。最佳努力审计，写失败不影响主链路与账务。
func (l *RequestLifecycle) MarkDeliveryInterrupted(ctx context.Context, requestRecord requestlog.RequestRecord) {
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()

	_, _ = l.requestLog.MarkRequestDeliveryInterrupted(auditCtx, requestRecord.ID)
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
