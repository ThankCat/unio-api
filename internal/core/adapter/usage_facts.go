package adapter

import "github.com/ThankCat/unio-api/internal/core/usage"

// ToUsageFacts 把 OpenAI 语义的 ChatUsage 映射为协议无关的 usage.Facts。
//
// 这是 OpenAI 协议族的 usage 映射事实来源（见 RESPONSE_FACTS 第 4 节）：
//   - 输入拆分为命中 cache（CacheRead）与未命中 cache（Uncached）两部分；
//   - OpenAI Chat Completions 没有 cache write TTL 事实，5m/1h 维度显式标记 not_applicable；
//   - OutputTokensTotal 取 completion_tokens（含 reasoning），ReasoningOutputTokens 仅作分解项。
//
// 入参应来自已校验的上游 usage（chatUsageFromOpenAI 已保证字段存在且自洽），因此这里只做
// 结构映射，不重复做存在性校验。
func (u ChatUsage) ToUsageFacts() usage.Facts {
	uncached := u.PromptTokens - u.CachedTokens
	if uncached < 0 {
		uncached = 0
	}

	return usage.Facts{
		UncachedInputTokens:     usage.KnownTokens(int64(uncached)),
		CacheReadInputTokens:    usage.KnownTokens(int64(u.CachedTokens)),
		CacheWrite5mInputTokens: usage.NotApplicableTokens(),
		CacheWrite1hInputTokens: usage.NotApplicableTokens(),
		OutputTokensTotal:       usage.KnownTokens(int64(u.CompletionTokens)),
		ReasoningOutputTokens:   usage.KnownTokens(int64(u.ReasoningTokens)),
	}
}
