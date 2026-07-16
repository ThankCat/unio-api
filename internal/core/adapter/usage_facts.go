package adapter

import "github.com/ThankCat/unio-gateway/internal/core/usage"

// ToUsageFacts 把 OpenAI 语义的 ChatUsage 映射为协议无关的 usage.Facts。
//
// 这是 OpenAI 协议族的 usage 映射事实来源（见 RESPONSE_FACTS 第 4 节）：
//   - 输入拆成三段：命中 cache 读（CacheRead）、写入 cache（CacheWrite30m）、余下未缓存（Uncached）；
//   - OpenAI GPT-5.6+ 的 cache_write_tokens 是「本次处理并写入缓存」的 token，属 uncached 的子集，
//     按未缓存输入价 1.25x 计费，故从 uncached 中扣出、单列到 30m 缓存写维度；旧模型/DeepSeek 不返回
//     该字段（=0），uncached 口径与历史一致，30m 计为 0 不影响账目；
//   - OpenAI 无 Anthropic 式 5m/1h TTL 分档，5m/1h 维度显式标记 not_applicable；
//   - OutputTokensTotal 取 completion_tokens（含 reasoning），ReasoningOutputTokens 仅作分解项。
//
// 入参应来自已校验的上游 usage（chatUsageFromOpenAI 已保证字段存在且自洽），因此这里只做
// 结构映射，不重复做存在性校验。
func (u ChatUsage) ToUsageFacts() usage.Facts {
	uncached := u.PromptTokens - u.CachedTokens - u.CacheWriteTokens
	if uncached < 0 {
		uncached = 0
	}

	return usage.Facts{
		UncachedInputTokens:      usage.KnownTokens(int64(uncached)),
		CacheReadInputTokens:     usage.KnownTokens(int64(u.CachedTokens)),
		CacheWrite5mInputTokens:  usage.NotApplicableTokens(),
		CacheWrite1hInputTokens:  usage.NotApplicableTokens(),
		CacheWrite30mInputTokens: usage.KnownTokens(int64(u.CacheWriteTokens)),
		OutputTokensTotal:        usage.KnownTokens(int64(u.CompletionTokens)),
		ReasoningOutputTokens:    usage.KnownTokens(int64(u.ReasoningTokens)),
	}
}
