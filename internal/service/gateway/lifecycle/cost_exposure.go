package lifecycle

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/core/billing"
	"github.com/ThankCat/unio-gateway/internal/core/requestlog"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/core/usage"
	"github.com/ThankCat/unio-gateway/internal/platform/logging"
)

// 成本敞口成因（channel_cost_exposures.reason）。
const (
	// CostExposureReasonUpstreamTimeout 上游首字超时：请求已发出、上游大概率仍在生成并计费。
	CostExposureReasonUpstreamTimeout = "upstream_timeout"
	// CostExposureReasonUpstreamError 上游 5xx / 传输层失败：中转边缘报错，后端可能已生成并计费。
	CostExposureReasonUpstreamError = "upstream_error"
	// CostExposureReasonClientCanceled 客户端在上游生成期间断开：bill-on-disconnect 上游照常完成并计费。
	CostExposureReasonClientCanceled = "client_canceled"
)

// CostExposureParams 是一条渠道成本敞口的写入参数。
type CostExposureParams struct {
	RequestRecordID      int64
	AttemptID            int64
	ChannelID            int64
	ProviderID           int64
	ReasonCode           string
	Reason               string
	EstimatedInputTokens int64
	AssumedOutputTokens  int64
	EstimatedCostAmount  pgtype.Numeric
	Currency             string
}

// CostExposureRecorder 定义把成本敞口写入存储的能力。
// 实现必须是纯追加写；失败由调用方按 best-effort 处理（敞口是观测事实，不阻断请求收口）。
type CostExposureRecorder interface {
	RecordChannelCostExposure(ctx context.Context, params CostExposureParams) error
	RecordProviderCostRisk(ctx context.Context, params CostExposureParams) error
}

// SetCostExposureRecorder 注入成本敞口记录器（bootstrap 连线用）。nil 表示不启用。
// assumedOutputFallback 是候选模型未配置 max_output_tokens 时的假定输出 token 兜底
// （与 authorization 的进程级兜底同源，保证敞口上界与冻结上界口径一致）。
func (l *RequestLifecycle) SetCostExposureRecorder(recorder CostExposureRecorder, assumedOutputFallback int64) {
	if l == nil {
		return
	}
	l.costExposures = recorder
	l.costExposureOutputFallback = assumedOutputFallback
}

// costExposureReason 把一次 attempt 失败/取消的错误分类映射成敞口成因。
//
// 只认「请求已发出、上游可能已产生成本」的三类：客户端取消 / 超时 / 上游 5xx（含传输层失败，
// adapter 把它们归为 server_error）。鉴权（401/403）、限流（429）、bad_request 是上游在生成前
// 拒绝的，不产生生成成本，不记敞口。
func costExposureReason(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	if meta, ok := adapter.UpstreamMetadataOf(err); ok && meta.StatusCode >= 400 && meta.StatusCode < 500 {
		return "", false
	}
	if errors.Is(err, context.Canceled) {
		return CostExposureReasonClientCanceled, true
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return CostExposureReasonUpstreamTimeout, true
	}
	category, ok := adapter.UpstreamCategoryOf(err)
	if !ok {
		return "", false
	}
	switch category {
	case adapter.UpstreamErrorCanceled:
		return CostExposureReasonClientCanceled, true
	case adapter.UpstreamErrorTimeout:
		return CostExposureReasonUpstreamTimeout, true
	case adapter.UpstreamErrorServer, adapter.UpstreamErrorUnknown:
		return CostExposureReasonUpstreamError, true
	default:
		return "", false
	}
}

func providerCostRiskReason(reasonCode string) string {
	switch reasonCode {
	case CostExposureReasonUpstreamTimeout:
		return "上游请求超时，可能已经产生费用，需要人工核对"
	case CostExposureReasonUpstreamError:
		return "上游服务或网络中断，可能已经产生费用，需要人工核对"
	case CostExposureReasonClientCanceled:
		return "客户中途取消，但上游可能已经产生费用，需要人工核对"
	default:
		return "上游没有返回完整用量，实际费用需要人工核对"
	}
}

// RecordCostExposure 在失败/取消路径上记录 Provider 待对账风险；bill-on-disconnect 渠道还会保留
// 原有的 Channel 成本敞口（best-effort）。
//
// 只在「本 attempt 不会产生真实结算成本」的路径上调用（结算/partial 结算路径已有 cost_snapshots，
// 再记敞口会双计）。金额为保守上界：输入按预授权保守估算全记 uncached，输出按
// min(模型 max_output_tokens, 进程兜底) 假定；估算不影响客户计费与账本，只供平台成本对账。
// 写入用脱离取消的上下文（客户端可能已断开），失败静默忽略（与 MarkAttemptFailed 同风格）。
func (l *RequestLifecycle) RecordCostExposure(
	ctx context.Context,
	requestRecord requestlog.RequestRecord,
	attempt requestlog.AttemptRecord,
	candidate routing.ChatRouteCandidate,
	estimatedInputTokens int64,
	err error,
) {
	if l == nil || l.costExposures == nil {
		return
	}
	reason, ok := costExposureReason(err)
	if !ok {
		return
	}
	l.recordUnreliableUsageCost(ctx, requestRecord, attempt, candidate, estimatedInputTokens, reason, providerCostRiskReason(reason), candidate.BillsOnDisconnect)
}

// RecordUsageMissingCostRisk 记录上游已响应但缺少可靠 usage 的 Provider 待对账风险。
// 这类风险与 Channel 的 bill-on-disconnect 配置无关，也不写入旧的 channel_cost_exposures。
func (l *RequestLifecycle) RecordUsageMissingCostRisk(
	ctx context.Context,
	requestRecord requestlog.RequestRecord,
	attempt requestlog.AttemptRecord,
	candidate routing.ChatRouteCandidate,
	estimatedInputTokens int64,
	reasonCode string,
) {
	if l == nil || l.costExposures == nil {
		return
	}
	if reasonCode == "" {
		reasonCode = "upstream_usage_missing"
	}
	l.recordUnreliableUsageCost(
		ctx,
		requestRecord,
		attempt,
		candidate,
		estimatedInputTokens,
		reasonCode,
		"上游没有返回完整用量，实际费用需要人工核对",
		false,
	)
}

func (l *RequestLifecycle) recordUnreliableUsageCost(
	ctx context.Context,
	requestRecord requestlog.RequestRecord,
	attempt requestlog.AttemptRecord,
	candidate routing.ChatRouteCandidate,
	estimatedInputTokens int64,
	reasonCode string,
	reason string,
	recordChannelExposure bool,
) {

	if estimatedInputTokens < 0 {
		estimatedInputTokens = 0
	}
	assumedOutput := candidate.MaxOutputTokens
	if assumedOutput <= 0 {
		assumedOutput = l.costExposureOutputFallback
	}
	if assumedOutput < 0 {
		assumedOutput = 0
	}

	// 复用结算同一套成本计算（NUMERIC 全程）：输入全记 uncached（cache 拆分未知，上界口径）。
	cost, costErr := billing.Service{}.CalculateProviderCost(usage.Facts{
		UncachedInputTokens:      usage.KnownTokens(estimatedInputTokens),
		CacheReadInputTokens:     usage.NotApplicableTokens(),
		CacheWrite5mInputTokens:  usage.NotApplicableTokens(),
		CacheWrite1hInputTokens:  usage.NotApplicableTokens(),
		CacheWrite30mInputTokens: usage.NotApplicableTokens(),
		OutputTokensTotal:        usage.KnownTokens(assumedOutput),
		ReasoningOutputTokens:    usage.NotApplicableTokens(),
	}, candidate.ChannelCost)
	params := CostExposureParams{
		RequestRecordID:      requestRecord.ID,
		AttemptID:            attempt.ID,
		ChannelID:            candidate.Channel.ID,
		ProviderID:           candidate.ProviderID,
		ReasonCode:           reasonCode,
		Reason:               reason,
		EstimatedInputTokens: estimatedInputTokens,
		AssumedOutputTokens:  assumedOutput,
	}
	if costErr == nil {
		params.EstimatedCostAmount = cost.TotalCostAmount
		params.Currency = cost.Currency
	}
	fields := l.completeAttemptLogContext(ctx, requestRecord, attempt, candidate, requestRecord.Stream)
	fields = append(fields,
		zap.String("reason_code", reasonCode),
		zap.Int64("estimated_input_tokens", estimatedInputTokens),
		zap.Int64("assumed_output_tokens", assumedOutput),
	)
	if costErr == nil {
		fields = append(fields,
			zap.String("estimated_cost_amount", numericLogString(cost.TotalCostAmount)),
			zap.String("currency", cost.Currency),
		)
	} else {
		fields = append(fields, zap.Bool("estimated_amount_unknown", true))
	}
	detachedCtx := context.WithoutCancel(ctx)
	if recordErr := l.costExposures.RecordProviderCostRisk(detachedCtx, params); recordErr != nil {
		providerFields := append([]zap.Field{}, fields...)
		providerFields = append(providerFields, l.safeErrorLogFields(recordErr, "provider_cost_risk_record_failed", requestRecord.Stream, AttemptTimingFacts{})...)
		logging.Error(l.logger, "billing", "provider_cost_risk", "provider cost risk record failed", nonEmptyLogFields(providerFields)...)
	} else {
		logging.Warn(l.logger, "billing", "provider_cost_risk", "provider cost risk recorded", nonEmptyLogFields(fields)...)
	}

	// 旧 Channel 敞口表要求金额与币种完整；无法估价时仍保留上面的 Provider 风险。
	if !recordChannelExposure || costErr != nil {
		return
	}
	if recordErr := l.costExposures.RecordChannelCostExposure(detachedCtx, params); recordErr != nil {
		channelFields := append([]zap.Field{}, fields...)
		channelFields = append(channelFields, l.safeErrorLogFields(recordErr, "channel_cost_exposure_record_failed", requestRecord.Stream, AttemptTimingFacts{})...)
		logging.Error(l.logger, "billing", "cost_exposure", "channel cost exposure record failed", nonEmptyLogFields(channelFields)...)
		return
	}
	logging.Warn(l.logger, "billing", "cost_exposure", "channel cost exposure recorded", nonEmptyLogFields(fields)...)
}
