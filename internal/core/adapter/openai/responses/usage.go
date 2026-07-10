package responses

import (
	"github.com/ThankCat/unio-api/internal/core/adapter"
	"github.com/ThankCat/unio-api/internal/core/usage"
)

// usageMappingVersionResponses 标记 Responses usage→facts 映射规则版本，用于历史账务复算与回归。
// v2：新增解析 input_tokens_details.cache_write_tokens（GPT-5.6+），拆入 30m 缓存写维度。
const usageMappingVersionResponses = "openai.responses.v2"

// wireResponse 是上游 /responses 响应体（及流式终态事件内 response 对象）中 adapter 关心的最小子集。
//
// 只解析账务/审计需要的字段；其余字段对 adapter 透明，由 Raw / Data 原文透传给客户（零转换）。
type wireResponse struct {
	ID                string                 `json:"id"`
	Model             string                 `json:"model"`
	Status            string                 `json:"status"`
	Usage             *wireUsage             `json:"usage"`
	IncompleteDetails *wireIncompleteDetails `json:"incomplete_details"`
	Error             *wireError             `json:"error"`
}

// wireUsage 是上游 Responses usage 对象。
type wireUsage struct {
	InputTokens         int64                  `json:"input_tokens"`
	OutputTokens        int64                  `json:"output_tokens"`
	TotalTokens         int64                  `json:"total_tokens"`
	InputTokensDetails  *wireInputTokenDetail  `json:"input_tokens_details"`
	OutputTokensDetails *wireOutputTokenDetail `json:"output_tokens_details"`
	// CacheCreationInputTokens 是部分 OpenAI 兼容上游（如 sub2api）在顶层回传的缓存写入 token
	// （Anthropic 风字段名）。作为 input_tokens_details.cache_write_tokens 的别名兜底。
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
}

type wireInputTokenDetail struct {
	CachedTokens int64 `json:"cached_tokens"`
	// CacheWriteTokens 是 GPT-5.6+ 起 input_tokens_details 中「写入缓存」的 token 数（按 1.25x 计费）。
	CacheWriteTokens int64 `json:"cache_write_tokens"`
	// CacheCreationTokens 是 sub2api 等兼容上游的别名字段（等价 cache_write_tokens）。
	CacheCreationTokens int64 `json:"cache_creation_tokens"`
}

type wireOutputTokenDetail struct {
	ReasoningTokens int64 `json:"reasoning_tokens"`
}

type wireIncompleteDetails struct {
	Reason string `json:"reason"`
}

// wireError 是上游 Responses 原生 error 对象（response.error / 顶层 error）。
type wireError struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// chatUsageFromWire 把上游 Responses usage 归一为 adapter 内部 usage DTO。
//
// 与桥接侧 mapResponsesUsage 反向：input/output/total + cached/reasoning 分解项一一对应。
// usage 为 nil 时返回 (零值, false)，由调用方按「缺失 usage」处理，绝不当成 0 元请求。
func chatUsageFromWire(u *wireUsage) (adapter.ChatUsage, bool) {
	if u == nil {
		return adapter.ChatUsage{}, false
	}

	result := adapter.ChatUsage{
		PromptTokens:     int(u.InputTokens),
		CompletionTokens: int(u.OutputTokens),
		TotalTokens:      int(u.TotalTokens),
	}
	if u.InputTokensDetails != nil {
		result.CachedTokens = int(u.InputTokensDetails.CachedTokens)
	}
	// 缓存写入 token 按优先级取首个正值，兼容官方与 sub2api 等兼容上游的多种字段名：
	// input_tokens_details.cache_write_tokens > input_tokens_details.cache_creation_tokens > 顶层 cache_creation_input_tokens。
	result.CacheWriteTokens = firstPositiveInt64(
		detailCacheWrite(u.InputTokensDetails),
		detailCacheCreation(u.InputTokensDetails),
		u.CacheCreationInputTokens,
	)
	if u.OutputTokensDetails != nil {
		result.ReasoningTokens = int(u.OutputTokensDetails.ReasoningTokens)
	}
	return result, true
}

func detailCacheWrite(d *wireInputTokenDetail) int64 {
	if d == nil {
		return 0
	}
	return d.CacheWriteTokens
}

func detailCacheCreation(d *wireInputTokenDetail) int64 {
	if d == nil {
		return 0
	}
	return d.CacheCreationTokens
}

// firstPositiveInt64 返回首个 > 0 的值（都非正则返回 0）；用于多字段别名兜底。
func firstPositiveInt64(values ...int64) int {
	for _, v := range values {
		if v > 0 {
			return int(v)
		}
	}
	return 0
}

// responsesFinishClass 把 Responses status + incomplete 原因映射为协议无关的稳定 FinishClass。
//
// completed → stop；incomplete 按 reason 分流（max_output_tokens→length，content_filter→content_filter）；
// 其余归 other。原始 finish 语义串另存 FinishFacts.RawReason 供审计。
func responsesFinishClass(status, incompleteReason string) adapter.FinishClass {
	switch status {
	case "completed":
		return adapter.FinishStop
	case "incomplete":
		switch incompleteReason {
		case "max_output_tokens":
			return adapter.FinishLength
		case "content_filter":
			return adapter.FinishContentFilter
		default:
			return adapter.FinishOther
		}
	default:
		return adapter.FinishOther
	}
}

// responsesRawFinish 构造写入 FinishFacts.RawReason 的原始 finish 语义串。
func responsesRawFinish(status, incompleteReason string) string {
	if status == "incomplete" && incompleteReason != "" {
		return incompleteReason
	}
	return status
}

// responsesFacts 用 Responses 直传语义构造协议无关的 ResponseFacts。
func responsesFacts(resp wireResponse, u adapter.ChatUsage, meta adapter.UpstreamMetadata, source usage.Source) adapter.ResponseFacts {
	incompleteReason := ""
	if resp.IncompleteDetails != nil {
		incompleteReason = resp.IncompleteDetails.Reason
	}

	return adapter.ResponseFacts{
		UpstreamProtocol:   "openai",
		UpstreamResponseID: resp.ID,
		UpstreamModel:      resp.Model,
		Finish: adapter.FinishFacts{
			Class:     responsesFinishClass(resp.Status, incompleteReason),
			RawReason: responsesRawFinish(resp.Status, incompleteReason),
		},
		Usage:               u.ToUsageFacts(),
		UsageSource:         source,
		UsageMappingVersion: usageMappingVersionResponses,
		Metadata:            meta,
	}
}
