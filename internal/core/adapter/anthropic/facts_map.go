package anthropic

import (
	"github.com/ThankCat/unio-api/internal/core/adapter"
	"github.com/ThankCat/unio-api/internal/core/usage"
)

// usageMappingVersionAnthropic 标记 Anthropic usage→facts 映射规则版本，用于历史账务复算与回归。
const usageMappingVersionAnthropic = "anthropic.v1"

// anthropicFinishClass 把 Anthropic stop_reason 映射为协议无关的稳定 FinishClass。
//
// 原始 stop_reason 仍保存在 FinishFacts.RawReason 用于审计；空 stop_reason（如流式中途）
// 按 stop 处理，与既有默认行为一致。
func anthropicFinishClass(rawReason string) adapter.FinishClass {
	switch rawReason {
	case "", "end_turn", "stop_sequence":
		return adapter.FinishStop
	case "max_tokens":
		return adapter.FinishLength
	case "tool_use":
		return adapter.FinishToolUse
	case "pause_turn":
		return adapter.FinishPause
	case "refusal":
		return adapter.FinishRefusal
	default:
		return adapter.FinishOther
	}
}

// ResponseFacts 用 Anthropic 协议族语义构造协议无关的 ResponseFacts。
//
// responseID/upstreamModel 为上游返回的标识与实际模型；rawReason 为上游 stop_reason；
// u 为已解析的 Anthropic usage；source 区分非流式响应与流式终态事实。
func ResponseFacts(responseID, upstreamModel, rawReason string, u MessageUsage, meta adapter.UpstreamMetadata, source usage.Source) adapter.ResponseFacts {
	return adapter.ResponseFacts{
		UpstreamProtocol:   "anthropic",
		UpstreamResponseID: responseID,
		UpstreamModel:      upstreamModel,
		Finish: adapter.FinishFacts{
			Class:     anthropicFinishClass(rawReason),
			RawReason: rawReason,
		},
		Usage:               u.ToUsageFacts(),
		UsageSource:         source,
		UsageMappingVersion: usageMappingVersionAnthropic,
		Metadata:            meta,
	}
}

// ResponseFactsNonStream 构造非流式响应的 ResponseFacts。
func ResponseFactsNonStream(responseID, upstreamModel, rawReason string, u MessageUsage, meta adapter.UpstreamMetadata) adapter.ResponseFacts {
	return ResponseFacts(responseID, upstreamModel, rawReason, u, meta, usage.SourceUpstreamResponse)
}

// ResponseFactsStream 构造流式终态事实的 ResponseFacts。
func ResponseFactsStream(responseID, upstreamModel, rawReason string, u MessageUsage, meta adapter.UpstreamMetadata) adapter.ResponseFacts {
	return ResponseFacts(responseID, upstreamModel, rawReason, u, meta, usage.SourceUpstreamStream)
}
