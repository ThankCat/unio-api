package messages

import "github.com/ThankCat/unio-gateway/internal/core/usage"

// MessageUsage 是 Anthropic Messages 协议族 adapter 解析出的用量事实（wire 形状）。
//
// 各 cache / 分解维度用指针区分"上游未提供"与"已知为 0"：指针为 nil 表示上游本次未返回该维度，
// 由 ToUsageFacts 决定标记 not_applicable；非 nil（含 0）表示上游显式给出。
type MessageUsage struct {
	// InputTokens 是未命中缓存的输入 token（Anthropic 语义：与 cache_read 分离）。
	InputTokens int

	// CacheCreationInputTokens 是写入缓存的输入 token 总量（不含 TTL 拆分）。
	CacheCreationInputTokens *int

	// CacheReadInputTokens 是命中缓存读取的输入 token。
	CacheReadInputTokens *int

	// CacheCreation 是 cache write 的 TTL 拆分；上游未提供拆分时为 nil。
	CacheCreation *CacheCreationUsage

	// OutputTokens 是输出 token 总量（含思考输出）。
	OutputTokens int

	// ThinkingOutputTokens 是 output_tokens_details.thinking_tokens 分解项；nil 表示上游未提供。
	ThinkingOutputTokens *int

	// ServerToolUse 是服务端工具调用计量；nil 表示上游未提供。
	ServerToolUse *ServerToolUsage

	// ServiceTier 是服务等级（安全 metadata，如 "standard"）。
	ServiceTier *string
}

// CacheCreationUsage 是 cache write 的 TTL 拆分。
type CacheCreationUsage struct {
	Ephemeral5mInputTokens *int
	Ephemeral1hInputTokens *int
}

// ServerToolUsage 是服务端工具调用次数计量。
type ServerToolUsage struct {
	WebSearchRequests *int
	WebFetchRequests  *int
}

// ToUsageFacts 把 Anthropic 语义的 MessageUsage 映射为协议无关的 usage.Facts。
//
// 映射规则：
//   - InputTokens 直接是未命中缓存的输入量（UncachedInputTokens）。
//   - cache read：指针存在即 Known，否则 not_applicable（Anthropic 语义下缺失等价 0）。
//   - cache write TTL 拆分：优先用 CacheCreation 拆分；若上游只给 flat 总量（如 DeepSeek 单档缓存，
//     无 TTL 分级），把总量归入默认 5m 档，1h 标记 not_applicable，避免丢失成本 token。
//   - OutputTokensTotal 取 OutputTokens（含 reasoning）；ThinkingOutputTokens 仅作分解项。
//   - server tool 调用映射为受控 MeteredItem，未提供则不入账。
func (u MessageUsage) ToUsageFacts() usage.Facts {
	facts := usage.Facts{
		UncachedInputTokens:  usage.KnownTokens(int64(u.InputTokens)),
		CacheReadInputTokens: optionalTokenCount(u.CacheReadInputTokens),
		// Anthropic 只有 5m/1h 两档缓存写，无 OpenAI 式 30m 单档：30m 恒 not_applicable。
		CacheWrite30mInputTokens: usage.NotApplicableTokens(),
		OutputTokensTotal:        usage.KnownTokens(int64(u.OutputTokens)),
		ReasoningOutputTokens:    optionalTokenCount(u.ThinkingOutputTokens),
	}

	switch {
	case u.CacheCreation != nil:
		facts.CacheWrite5mInputTokens = optionalTokenCount(u.CacheCreation.Ephemeral5mInputTokens)
		facts.CacheWrite1hInputTokens = optionalTokenCount(u.CacheCreation.Ephemeral1hInputTokens)
	case u.CacheCreationInputTokens != nil:
		// 上游只给缓存写入总量（无 TTL 分级）：归入默认 5m 档，1h 不适用。
		facts.CacheWrite5mInputTokens = usage.KnownTokens(int64(*u.CacheCreationInputTokens))
		facts.CacheWrite1hInputTokens = usage.NotApplicableTokens()
	default:
		facts.CacheWrite5mInputTokens = usage.NotApplicableTokens()
		facts.CacheWrite1hInputTokens = usage.NotApplicableTokens()
	}

	facts.ServerToolUsage = serverToolMeteredItems(u.ServerToolUse)
	return facts
}

// optionalTokenCount 把可选 int 指针转成 TokenCount：存在为 Known，缺失为 not_applicable。
func optionalTokenCount(v *int) usage.TokenCount {
	if v == nil {
		return usage.NotApplicableTokens()
	}
	return usage.KnownTokens(int64(*v))
}

// serverToolMeteredItems 把服务端工具计量映射为受控 MeteredItem 列表。
func serverToolMeteredItems(s *ServerToolUsage) []usage.MeteredItem {
	if s == nil {
		return nil
	}

	var items []usage.MeteredItem
	if s.WebSearchRequests != nil {
		items = append(items, usage.MeteredItem{
			Kind:     usage.MeteredServerWebSearchRequest,
			Quantity: int64(*s.WebSearchRequests),
		})
	}
	if s.WebFetchRequests != nil {
		items = append(items, usage.MeteredItem{
			Kind:     usage.MeteredServerWebFetchRequest,
			Quantity: int64(*s.WebFetchRequests),
		})
	}
	return items
}
