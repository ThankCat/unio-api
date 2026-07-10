package adapter

// ChatUsage 表示 adapter 从上游响应中解析出的 token 用量。
//
// 当前字段偏 OpenAI Chat Completions 语义，作为协议无关 usage.Facts 落地前的过渡事实
// 留在 adapter 根包：openai 协议族 DTO 与 stream translator 都消费它，
// Phase 10 的 usage.Facts 改造（TASK-10.04）会替换它。
type ChatUsage struct {
	// PromptTokens 是输入 prompt token 总数。
	PromptTokens int

	// CompletionTokens 是输出 completion token 总数，包含 reasoning tokens。
	CompletionTokens int

	// TotalTokens 是本次请求总 token 数，通常等于 PromptTokens + CompletionTokens。
	TotalTokens int

	// CachedTokens 是 prompt tokens 中命中上游 prompt cache 的数量。
	CachedTokens int

	// CacheWriteTokens 是 prompt tokens 中写入上游 prompt cache 的数量（OpenAI GPT-5.6+ 的
	// cache_write_tokens，按未缓存输入价 1.25x 计费）。它是 uncached 输入的子集，映射到 30m 缓存写维度。
	CacheWriteTokens int

	// ReasoningTokens 是 completion tokens 中用于模型内部推理的数量。
	ReasoningTokens int
}
