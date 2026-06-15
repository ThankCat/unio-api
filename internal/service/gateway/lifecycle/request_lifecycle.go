package lifecycle

import (
	"context"
	"time"

	"github.com/ThankCat/unio-api/internal/core/adapter"
	"github.com/ThankCat/unio-api/internal/core/auth"
	"github.com/ThankCat/unio-api/internal/core/requestlog"
	"github.com/ThankCat/unio-api/internal/core/routing"
	"github.com/ThankCat/unio-api/internal/platform/observability/logfields"
	"github.com/ThankCat/unio-api/internal/platform/observability/metrics"
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
	ingressProtocol requestlog.Protocol
	operation       requestlog.Operation
	safeMessage     func(code string) string
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
func (l *RequestLifecycle) CandidateAvailable(candidate routing.ChatRouteCandidate) bool {
	if l == nil || l.breaker == nil {
		return true
	}

	return l.breaker.Available(MetricsID(candidate.Channel.ID))
}

// BreakerAllow 在启用熔断时判断是否允许尝试该 channel；未启用时始终放行。
// nil receiver / nil breaker 等价于「未启用熔断」，全部放行。
func (l *RequestLifecycle) BreakerAllow(channelKey string) bool {
	if l == nil || l.breaker == nil {
		return true
	}

	return l.breaker.Allow(channelKey)
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

// ---------------------------------------------------------------------------
// request log：创建 / 推进 / 失败 / 取消，按构造时的 IngressProtocol + Operation 写。
// ---------------------------------------------------------------------------

// CreateRequest 创建用户可见请求记录，并立即推进到 running 状态。
// request_records.request_id 由服务端生成，用作数据库唯一事实 ID；
// HTTP X-Request-ID 只作为日志 correlation id，不能直接复用为账务请求 ID。
func (l *RequestLifecycle) CreateRequest(ctx context.Context, principal *auth.APIKeyPrincipal, requestedModelID string, stream bool) (requestlog.RequestRecord, error) {
	requestID, err := requestlog.GenerateRequestID()
	if err != nil {
		return requestlog.RequestRecord{}, err
	}

	// 把业务 request_id 写入结构化日志字段，使其与 HTTP correlation_id 在同一条访问日志可关联。
	logfields.SetRequestID(ctx, requestID)

	record, err := l.requestLog.CreateRequest(ctx, requestlog.CreateRequestParams{
		RequestID:        requestID,
		UserID:           principal.UserID,
		ProjectID:        principal.ProjectID,
		APIKeyID:         principal.APIKeyID,
		RequestedModelID: requestedModelID,
		IngressProtocol:  l.ingressProtocol,
		Operation:        l.operation,
		Stream:           stream,
		StartedAt:        time.Now(),
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

// RecordCapabilityResult 把 capability 闸门 observe 判定结论写入 request_records 审计列（阶段 12）。
//
// 在 PlanChat 成功后调用：observation 为 nil（闸门未启用 / 无 required / 无候选）时不写，列保持 NULL（bypassed）。
// 纯审计动作，best-effort：写失败不影响主流程，与 MarkRequest* 审计写入一致地吞掉错误。
func (l *RequestLifecycle) RecordCapabilityResult(ctx context.Context, requestRecord requestlog.RequestRecord, observation *routing.CapabilityObservation) {
	if observation == nil {
		return
	}

	_ = l.requestLog.SetCapabilityCheckResult(ctx, requestRecord.ID, string(observation.Result))
}

// CreateAttempt 创建一次上游 channel 尝试记录。
// attempt 记录 fallback 链路中的单次 provider/channel 调用，必须先于 adapter 调用创建。
func (l *RequestLifecycle) CreateAttempt(ctx context.Context, requestRecord requestlog.RequestRecord, attemptIndex int, candidate routing.ChatRouteCandidate, requiredCapabilities []string) (requestlog.AttemptRecord, error) {
	return l.requestLog.CreateAttempt(ctx, requestlog.CreateAttemptParams{
		RequestRecordID:      requestRecord.ID,
		AttemptIndex:         attemptIndex,
		ProviderID:           candidate.ProviderID,
		ChannelID:            candidate.Channel.ID,
		AdapterKey:           candidate.AdapterKey,
		UpstreamModel:        candidate.UpstreamModel,
		UpstreamProtocol:     requestlog.Protocol(candidate.Protocol),
		RequiredCapabilities: requiredCapabilities,
		StartedAt:            time.Now(),
	})
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

	_, _ = l.requestLog.MarkAttemptFailed(ctx, requestlog.MarkAttemptFailedParams{
		ID:                  attempt.ID,
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
	errorCode, safeMessage, internalDetail := l.requestLogErrorFacts("client_canceled", err)

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
