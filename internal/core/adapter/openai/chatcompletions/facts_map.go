package chatcompletions

import (
	"github.com/ThankCat/unio-api/internal/core/adapter"
	"github.com/ThankCat/unio-api/internal/core/usage"
)

// usageMappingVersionOpenAI 标记 OpenAI usage→facts 映射规则版本，用于历史账务复算与回归。
// v2：新增解析 cache_write_tokens（GPT-5.6+），从 uncached 拆出并计入 30m 缓存写维度。
const usageMappingVersionOpenAI = "openai.v2"

// openAIFinishClass 把 OpenAI finish_reason 映射为协议无关的稳定 FinishClass。
//
// 原始 finish reason 仍保存在 FinishFacts.RawReason 用于审计；这里只做跨模块业务可消费的稳定分类。
// 空 finish reason 与现有 gateway 默认行为一致按 stop 处理。
func openAIFinishClass(rawFinish string) adapter.FinishClass {
	switch rawFinish {
	case "", "stop":
		return adapter.FinishStop
	case "length":
		return adapter.FinishLength
	case "tool_calls", "function_call":
		return adapter.FinishToolUse
	case "content_filter":
		return adapter.FinishContentFilter
	default:
		return adapter.FinishOther
	}
}

// responseFacts 用 OpenAI 协议族语义构造协议无关的 ResponseFacts。
func responseFacts(responseID, upstreamModel, rawFinish string, u adapter.ChatUsage, meta adapter.UpstreamMetadata, source usage.Source) adapter.ResponseFacts {
	return adapter.ResponseFacts{
		UpstreamProtocol:   "openai",
		UpstreamResponseID: responseID,
		UpstreamModel:      upstreamModel,
		Finish: adapter.FinishFacts{
			Class:     openAIFinishClass(rawFinish),
			RawReason: rawFinish,
		},
		Usage:               u.ToUsageFacts(),
		UsageSource:         source,
		UsageMappingVersion: usageMappingVersionOpenAI,
		Metadata:            meta,
	}
}

// responseFactsNonStream 构造非流式响应的 ResponseFacts。
func responseFactsNonStream(responseID, upstreamModel, rawFinish string, u adapter.ChatUsage, meta adapter.UpstreamMetadata) adapter.ResponseFacts {
	return responseFacts(responseID, upstreamModel, rawFinish, u, meta, usage.SourceUpstreamResponse)
}

// responseFactsStream 构造流式响应的最终 ResponseFacts。
func responseFactsStream(responseID, upstreamModel, rawFinish string, u adapter.ChatUsage, meta adapter.UpstreamMetadata) adapter.ResponseFacts {
	return responseFacts(responseID, upstreamModel, rawFinish, u, meta, usage.SourceUpstreamStream)
}

// streamOutcome 构造流式调用结束后交给 lifecycle 的最终事实。
//
// finalUsage 为 nil 表示上游没有给出可靠 usage；此时返回空 outcome，由 lifecycle 按
// release 或 risk_exposure 规则收口，不能把缺失 usage 偷偷当成 0 元请求。
func streamOutcome(responseID, upstreamModel, rawFinish string, finalUsage *adapter.ChatUsage, meta adapter.UpstreamMetadata) adapter.StreamOutcome {
	if finalUsage == nil {
		return adapter.StreamOutcome{}
	}

	facts := responseFactsStream(responseID, upstreamModel, rawFinish, *finalUsage, meta)
	return adapter.StreamOutcome{Facts: &facts}
}
