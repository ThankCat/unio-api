package lifecycle

import (
	"fmt"

	"github.com/ThankCat/unio-api/internal/core/adapter"
	"github.com/ThankCat/unio-api/internal/core/routing"
	"github.com/ThankCat/unio-api/internal/core/usage"
)

// partialUsageMappingVersion 标记 partial 估算事实的映射版本，用于历史账务复算与幂等比对。
const partialUsageMappingVersion = "partial.v1"

// Partial settlement 的合成 finish reason（落到 attempt.upstream_finish_reason，区分 B/D，见 DEC-025）。
const (
	// PartialReasonClientCanceled 路线 B：客户端取消。
	PartialReasonClientCanceled = "stream_client_canceled_without_final_usage"
	// PartialReasonInterrupted 路线 B：emit 后流中断。
	PartialReasonInterrupted = "stream_interrupted_without_final_usage"
	// PartialReasonFinalUsageMissing 路线 D：adapter 正常结束但上游未返回 final usage（渠道异常信号）。
	PartialReasonFinalUsageMissing = "stream_final_usage_missing"
)

// PartialStreamFactsParams 表示合成一份 partial 估算事实所需的输入。
type PartialStreamFactsParams struct {
	// Candidate 是实际 emit 内容的命中候选（提供 Protocol / UpstreamModel）。
	Candidate routing.ChatRouteCandidate
	// StreamResponseID 是从 chunk 收集到的上游响应 id；为空时用 partial-<request_id> 兜底。
	StreamResponseID string
	// RequestRecordID 用于在缺上游 response id 时合成可审计的兜底 id。
	RequestRecordID int64
	// InputTokens 复用预授权阶段的保守输入估算（与 freeze 同源）。
	InputTokens int64
	// OutputTokens 是对「已 emit 可见文本」的增量估算（偏保守）。
	OutputTokens int64
	// Reason 是合成 finish reason（PartialReason*），落到 upstream_finish_reason 区分 B/D。
	Reason string
}

// BuildPartialStreamFacts 在「已 emit 但无 adapter final usage」时合成可计费的估算事实。
//
// 它把字段补齐到能通过 settlement 入口校验（ValidateChatSettlementFacts）：StatusCode 固定 200
// （emitted 意味着上游已回 200 头并开始流），UpstreamModel/Protocol 取自命中候选，response id 兜底，
// usage 仅含保守 input + 估算 output，其余维度 not_applicable。UsageSource 标记 partial_stream_estimate，
// 与上游真实 usage 严格区分；settlement 据此把 attempt.final_usage_received 记为 false。
func BuildPartialStreamFacts(p PartialStreamFactsParams) adapter.ResponseFacts {
	responseID := p.StreamResponseID
	if responseID == "" {
		responseID = fmt.Sprintf("partial-%d", p.RequestRecordID)
	}

	inputTokens := p.InputTokens
	if inputTokens < 0 {
		inputTokens = 0
	}
	outputTokens := p.OutputTokens
	if outputTokens < 0 {
		outputTokens = 0
	}

	return adapter.ResponseFacts{
		UpstreamProtocol:   p.Candidate.Protocol,
		UpstreamResponseID: responseID,
		UpstreamModel:      p.Candidate.UpstreamModel,
		Finish: adapter.FinishFacts{
			Class:     adapter.FinishOther,
			RawReason: p.Reason,
		},
		Usage: usage.Facts{
			UncachedInputTokens:     usage.KnownTokens(inputTokens),
			CacheReadInputTokens:    usage.NotApplicableTokens(),
			CacheWrite5mInputTokens: usage.NotApplicableTokens(),
			CacheWrite1hInputTokens: usage.NotApplicableTokens(),
			OutputTokensTotal:       usage.KnownTokens(outputTokens),
			ReasoningOutputTokens:   usage.NotApplicableTokens(),
		},
		UsageSource:         usage.SourcePartialStreamEstimate,
		UsageMappingVersion: partialUsageMappingVersion,
		Metadata: adapter.UpstreamMetadata{
			StatusCode: 200,
		},
	}
}
