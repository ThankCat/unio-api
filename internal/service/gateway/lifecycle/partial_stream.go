package lifecycle

import (
	"fmt"
	"math"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/core/usage"
)

// partialUsageMappingVersion 标记 partial 估算事实的映射版本，用于历史账务复算与幂等比对。
const partialUsageMappingVersion = "partial.v1"

// defaultPartialAssumedCacheReadRatio 是假定缓存率的内置默认（60% cache_read / 40% uncached）。
const defaultPartialAssumedCacheReadRatio = 0.60

// partialAssumedCacheReadRatio 是「无上游真实 usage 的流式 partial 结算」下，对估算输入假定的缓存命中比例。
// 由 PARTIAL_ASSUMED_CACHE_READ_RATIO 环境变量配置，进程启动时经 SetPartialAssumedCacheReadRatio 注入。
//
// 拿不到 final usage 时无从得知真实 cache 拆分：全按 uncached 计会把缓存重的 agent/Codex 请求超扣约 10 倍
// （用户吃亏），一点不扣又让平台白担成本。这里按固定比例把估算输入拆成 cache_read / uncached，
// 计费恒落在「全缓存」与「全未缓存」之间（按构造安全）。
//
// TODO(临时方案 · billing): 这只是一个固定比例的临时口径。最优方案是「根据该用户/会话距本次失败最近的
// 一次成功请求的真实缓存率来拆分」（缓存率强自相关，预测精度高），fallback 再退回本固定比例。
// 届时应把本固定比例替换为「历史缓存率 → 本固定比例」的兜底链，并注意：仅用于 partial 估算的输入拆分，
// 不影响冻结/限流/真实 usage/非流式（见 attempt_runner_stream.go 的 settleStreamFacts 与 releaseUnreconciledTPM）。
//
// 注意：仅当客户售价配置了 cache_read 单价时该拆分才真正生效——否则 billing 会把 cache_read 回退到
// uncached 单价（见 core/billing/price.go），拆分退化为「全 uncached」而静默失效。
var partialAssumedCacheReadRatio = defaultPartialAssumedCacheReadRatio

// SetPartialAssumedCacheReadRatio 在进程启动时注入假定缓存率（bootstrap 调用）。
// 入参预期在 [0,1]（由 config 层校验），这里再夹一层兜底，避免异常值把拆分推出边界。
func SetPartialAssumedCacheReadRatio(r float64) {
	if r < 0 {
		r = 0
	}
	if r > 1 {
		r = 1
	}
	partialAssumedCacheReadRatio = r
}

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

	// 无真实 usage：按固定假定缓存率把估算输入拆成 cache_read / uncached（见 partialAssumedCacheReadRatio）。
	cacheReadInput := int64(math.Round(float64(inputTokens) * partialAssumedCacheReadRatio))
	if cacheReadInput > inputTokens {
		cacheReadInput = inputTokens
	}
	uncachedInput := inputTokens - cacheReadInput

	return adapter.ResponseFacts{
		UpstreamProtocol:   p.Candidate.Protocol,
		UpstreamResponseID: responseID,
		UpstreamModel:      p.Candidate.UpstreamModel,
		Finish: adapter.FinishFacts{
			Class:     adapter.FinishOther,
			RawReason: p.Reason,
		},
		Usage: usage.Facts{
			UncachedInputTokens:      usage.KnownTokens(uncachedInput),
			CacheReadInputTokens:     usage.KnownTokens(cacheReadInput),
			CacheWrite5mInputTokens:  usage.NotApplicableTokens(),
			CacheWrite1hInputTokens:  usage.NotApplicableTokens(),
			CacheWrite30mInputTokens: usage.NotApplicableTokens(),
			OutputTokensTotal:        usage.KnownTokens(outputTokens),
			ReasoningOutputTokens:    usage.NotApplicableTokens(),
		},
		UsageSource:         usage.SourcePartialStreamEstimate,
		UsageMappingVersion: partialUsageMappingVersion,
		Metadata: adapter.UpstreamMetadata{
			StatusCode: 200,
		},
	}
}
