package lifecycle

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/core/billing"
	"github.com/ThankCat/unio-gateway/internal/core/requestlog"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/core/usage"
)

// 成本敞口成因（channel_cost_exposures.reason，DESIGN-bill-on-cancel 阶段一）。
const (
	// CostExposureReasonUpstreamTimeout 等首字节超时：请求已发出、上游大概率仍在生成并计费。
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
	Reason               string
	EstimatedInputTokens int64
	AssumedOutputTokens  int64
	EstimatedCostAmount  pgtype.Numeric
	Currency             string
}

// CostExposureRecorder 定义把成本敞口写入存储的能力（DESIGN-bill-on-cancel 阶段一）。
// 实现必须是纯追加写；失败由调用方按 best-effort 处理（敞口是观测事实，不阻断请求收口）。
type CostExposureRecorder interface {
	RecordChannelCostExposure(ctx context.Context, params CostExposureParams) error
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
	if errors.Is(err, context.Canceled) {
		return CostExposureReasonClientCanceled, true
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
	case adapter.UpstreamErrorServer:
		return CostExposureReasonUpstreamError, true
	default:
		return "", false
	}
}

// RecordCostExposure 在 bill-on-disconnect 渠道的失败/取消路径上记一条平台成本敞口（best-effort）。
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
	if l == nil || l.costExposures == nil || !candidate.BillsOnDisconnect {
		return
	}
	reason, ok := costExposureReason(err)
	if !ok {
		return
	}

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
	if costErr != nil {
		// 渠道成本价异常（理论上路由候选必有生效成本行）：敞口是观测事实，静默放弃本条。
		return
	}

	_ = l.costExposures.RecordChannelCostExposure(context.WithoutCancel(ctx), CostExposureParams{
		RequestRecordID:      requestRecord.ID,
		AttemptID:            attempt.ID,
		ChannelID:            candidate.Channel.ID,
		ProviderID:           candidate.ProviderID,
		Reason:               reason,
		EstimatedInputTokens: estimatedInputTokens,
		AssumedOutputTokens:  assumedOutput,
		EstimatedCostAmount:  cost.TotalCostAmount,
		Currency:             cost.Currency,
	})
}
