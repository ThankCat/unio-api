package lifecycle

import (
	"context"
	"errors"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/core/requestlog"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

// ChannelSampleRecorder 是评分样本/观测的最小写入契约（§12，与 admission 解耦）。
// 由 breakerstore.Store 实现；写失败只增指标不影响客户请求（§12.5）。
type ChannelSampleRecorder interface {
	RecordChannelSample(ctx context.Context, in breakerstore.ChannelSampleInput) error
}

type sampleMetricsRecorder interface {
	IncRoutingSampleAggregationFailure()
}

type attemptScoringSampleRecorder interface {
	RecordAttemptScoringSample(context.Context, requestlog.RecordAttemptScoringSampleParams) error
}

// SetChannelSampleRecorder 注入评分样本/观测记录器（§12）。nil 表示不启用。
func (l *RequestLifecycle) SetChannelSampleRecorder(recorder ChannelSampleRecorder) {
	if l != nil {
		l.sampleRecorder = recorder
	}
}

// RecordAttemptSample 在 attempt 终态写一次评分样本与 RPM/RPD/TPM 观测（§12）。
// 仅当真实发起过上游调用（transport 已开始）才产生观测；写入幂等、best-effort。
func (l *RequestLifecycle) RecordAttemptSample(
	ctx context.Context,
	candidate routing.ChatRouteCandidate,
	attempt requestlog.AttemptRecord,
	stream bool,
	facts AttemptTimingFacts,
	outcome breakerstore.FinishOutcome,
	err error,
) {
	if l == nil || l.sampleRecorder == nil {
		return
	}
	if attempt.ID == 0 || candidate.Channel.ID == 0 {
		return
	}
	// transport 未开始（如并发满/冷却跳过、prepare 后即 abort）不算真实上游调用，不产生观测。
	if facts.UpstreamStartedAt == nil {
		return
	}

	eligible, isError := classifyChannelScoringSample(outcome, err)
	ttft := sampleTTFTMs(stream, facts)
	if recorder, ok := l.requestLog.(attemptScoringSampleRecorder); ok {
		if recordErr := recorder.RecordAttemptScoringSample(context.WithoutCancel(ctx), requestlog.RecordAttemptScoringSampleParams{
			ID: attempt.ID, TTFTScoringSample: ttft != nil,
			ErrorScoringSample: eligible, ErrorScoringFailure: isError,
		}); recordErr != nil {
			if m, metricsOK := l.metrics.(sampleMetricsRecorder); metricsOK {
				m.IncRoutingSampleAggregationFailure()
			}
		}
	}
	in := breakerstore.ChannelSampleInput{
		ChannelID:       candidate.Channel.ID,
		AttemptID:       attempt.ID,
		TTFTMs:          ttft,
		ErrorEligible:   eligible,
		IsError:         isError,
		ObservedRequest: true,
		TokenCount:      outcome.ActualTotalTokens,
		TokenCovered:    outcome.ActualTotalTokens != nil,
	}
	// 脱离客户端取消：观测写入不应因下游断开而丢失当前 attempt 事实。
	if recErr := l.sampleRecorder.RecordChannelSample(context.WithoutCancel(ctx), in); recErr != nil {
		if m, ok := l.metrics.(sampleMetricsRecorder); ok {
			m.IncRoutingSampleAggregationFailure()
		}
	}
}

func classifyChannelScoringSample(outcome breakerstore.FinishOutcome, err error) (eligible bool, isError bool) {
	eligible, isError = classifyChannelSampleError(err)
	if err == nil && outcome.ChannelOutcome == breakerstore.OutcomeEligibleFailure {
		// adapter 正常返回但没有形成协议/usage 事实时，permit finish 已把它判为渠道协议失败；
		// 评分样本必须消费同一个事实，不能同时把该 attempt 记成错误率成功。
		return true, true
	}
	return eligible, isError
}

// sampleTTFTMs 返回本次 attempt 的 TTFT 样本（毫秒），无有效样本时返回 nil（§12.3）。
//   - 流式收到首个有效生成 Token：upstream_first_token_at - upstream_started_at；
//   - 非流式：不产生 TTFT 样本。
func sampleTTFTMs(stream bool, facts AttemptTimingFacts) *int64 {
	if !stream {
		return nil
	}
	if ms := facts.FirstTokenMs(); ms != nil {
		return ms
	}
	return nil
}

// classifyChannelSampleError 按 §7.5/D3 划分错误率分母(eligible)与分子(isError)。
//   - 分母：真实发出上游且结果可归因于 Channel 的 attempt（含成功）；
//   - 从分母排除：客户端取消/下游断开、普通客户 4xx、429（另有冷却）、Gateway DB/Redis/结算/内部错误；
//   - 分子：连接失败、各类超时、上游 5xx、上游异常中断、响应格式损坏、401、403。
func classifyChannelSampleError(err error) (eligible bool, isError bool) {
	if err == nil {
		return true, false
	}
	if errors.Is(err, context.Canceled) {
		return false, false
	}
	category, categoryOK := adapter.UpstreamCategoryOf(err)
	if categoryOK && category == adapter.UpstreamErrorCanceled {
		return false, false
	}
	status := 0
	if meta, ok := adapter.UpstreamMetadataOf(err); ok {
		status = meta.StatusCode
	}
	// 429 已单独进入冷却，避免双重惩罚。
	if status == 429 || (categoryOK && category == adapter.UpstreamErrorRateLimit) {
		return false, false
	}
	// 401 / 403 计入分子（凭据/权限失效可归因于渠道）。
	if status == 401 || status == 403 ||
		(categoryOK && (category == adapter.UpstreamErrorAuth || category == adapter.UpstreamErrorPermission)) {
		return true, true
	}
	// 其余客户 4xx 属客户请求问题，分母分子都排除。
	if status >= 400 && status < 500 {
		return false, false
	}
	if categoryOK {
		switch category {
		case adapter.UpstreamErrorServer, adapter.UpstreamErrorTimeout:
			return true, true
		case adapter.UpstreamErrorBadRequest:
			return false, false
		case adapter.UpstreamErrorUnknown:
			// 2xx 但违反协议契约（响应格式损坏）可归因于渠道。
			if protocolFailureCode(failure.CodeOf(err)) {
				return true, true
			}
			return false, false
		default:
			return false, false
		}
	}
	// 无上游分类：协议损坏归因渠道；其余（Gateway/DB/Redis/结算/内部）排除。
	if protocolFailureCode(failure.CodeOf(err)) {
		return true, true
	}
	return false, false
}
