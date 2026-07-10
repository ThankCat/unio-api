// Package usage 定义协议无关的用量事实（usage facts）。
//
// OpenAI Chat Completions 与 Anthropic Messages 的 usage 字段形状不同，但 settlement、
// recovery 和审计只应消费统一、稳定的事实。Facts 是这层统一事实；各协议 adapter 负责
// 在同一次响应解析中把自己的 wire usage 映射成 Facts。
package usage

// CountState 表示某个 token 维度的可信状态，用于区分“已知值（含已知为 0）”、
// “该协议不适用”和“上游未提供 / 解析不可靠”。
//
// 商业计费绝不能把 unknown 偷偷按 0 处理：unknown 必须显式标记，由 settlement 决定
// 拒绝、告警或按风险敞口处理，而不是当成 0 元请求。
type CountState string

const (
	// CountKnown 表示该维度有可信的具体值（可以是 0）。
	CountKnown CountState = "known"

	// CountNotApplicable 表示该维度在当前协议下不存在，可安全按 0 计费，但必须由 adapter 显式给出。
	CountNotApplicable CountState = "not_applicable"

	// CountUnknown 表示上游未提供或解析不可靠，settlement 不得按 0 处理。
	CountUnknown CountState = "unknown"
)

// TokenCount 是带可信状态的 token 计数。
type TokenCount struct {
	// Value 仅在 State 为 CountKnown 时有计费意义。
	Value int64

	// State 标记该计数的可信状态。
	State CountState
}

// KnownTokens 构造一个已知具体值的 TokenCount。
func KnownTokens(value int64) TokenCount {
	return TokenCount{Value: value, State: CountKnown}
}

// NotApplicableTokens 构造一个该协议不适用的 TokenCount（可安全按 0 计费）。
func NotApplicableTokens() TokenCount {
	return TokenCount{State: CountNotApplicable}
}

// UnknownTokens 构造一个未知 TokenCount（settlement 不得按 0 处理）。
func UnknownTokens() TokenCount {
	return TokenCount{State: CountUnknown}
}

// IsKnown 判断该计数是否为可信具体值。
func (c TokenCount) IsKnown() bool {
	return c.State == CountKnown
}

// BillableValue 返回用于计费的 token 数与是否可计费。
//
// known 返回真实值；not_applicable 返回 (0, true)，即按 0 计费；
// unknown 返回 (0, false)，调用方必须显式处理而不是当成 0。
func (c TokenCount) BillableValue() (int64, bool) {
	switch c.State {
	case CountKnown:
		return c.Value, c.Value >= 0
	case CountNotApplicable:
		return 0, c.Value == 0
	default:
		return 0, false
	}
}

// MeteredKind 是受控的附加计量项类型；不接受任意 provider JSON key。
type MeteredKind string

const (
	// MeteredServerWebSearchRequest 是服务端 web search 调用次数计量项。
	MeteredServerWebSearchRequest MeteredKind = "server_web_search_request"

	// MeteredServerWebFetchRequest 是服务端 web fetch 调用次数计量项。
	MeteredServerWebFetchRequest MeteredKind = "server_web_fetch_request"
)

// Valid 判断附加计量项类型是否已经登记为可持久化账务事实。
func (k MeteredKind) Valid() bool {
	switch k {
	case MeteredServerWebSearchRequest, MeteredServerWebFetchRequest:
		return true
	default:
		return false
	}
}

// MeteredItem 表示一项附加计量事实，例如 server tool 调用次数。
type MeteredItem struct {
	Kind     MeteredKind
	Quantity int64
}

// Valid 判断附加计量项是否可以作为受控账务事实持久化。
func (i MeteredItem) Valid() bool {
	return i.Kind.Valid() && i.Quantity > 0
}

// Facts 是协议无关的用量事实。
//
// 规则：
//   - OutputTokensTotal 是包含 reasoning 的 authoritative output 总量。
//   - ReasoningOutputTokens 是 output 的可选分解项，不是额外生成量，默认不单独计费，
//     避免与 OutputTokensTotal 重复收费。
//   - 不适用维度必须由 adapter 显式标记 not_applicable，不得留空当 0。
type Facts struct {
	// UncachedInputTokens 是未命中上游 prompt cache 的输入 token。
	UncachedInputTokens TokenCount

	// CacheReadInputTokens 是命中上游 prompt cache 读取的输入 token。
	CacheReadInputTokens TokenCount

	// CacheWrite5mInputTokens 是写入 5m TTL cache 的输入 token（Anthropic 5m 档；OpenAI 不适用）。
	CacheWrite5mInputTokens TokenCount

	// CacheWrite1hInputTokens 是写入 1h TTL cache 的输入 token（Anthropic 1h 档；OpenAI 不适用）。
	CacheWrite1hInputTokens TokenCount

	// CacheWrite30mInputTokens 是写入 30m TTL cache 的输入 token（OpenAI GPT-5.6+ 单档缓存写；
	// Anthropic 不适用）。与 5m/1h 并列，按 TTL 语义独立计价与审计，绝不混入其它档位。
	CacheWrite30mInputTokens TokenCount

	// OutputTokensTotal 是包含 reasoning 的 authoritative 输出总量。
	OutputTokensTotal TokenCount

	// ReasoningOutputTokens 是输出中用于内部推理的分解项。
	ReasoningOutputTokens TokenCount

	// ServerToolUsage 是受控的附加计量事实集合，未登记 Kind 不得入账。
	ServerToolUsage []MeteredItem
}

// Valid 判断 usage facts 是否满足持久化和计费前置约束。
//
// unknown 是合法审计状态，因此这里不会拒绝 unknown；billing 会进一步要求每个参与
// 当前公式的维度都可计费。非 known 状态携带非零值、负数、reasoning 超过总输出、
// 未登记 line item 或重复 line item 都会被拒绝。
func (f Facts) Valid() bool {
	counts := []TokenCount{
		f.UncachedInputTokens,
		f.CacheReadInputTokens,
		f.CacheWrite5mInputTokens,
		f.CacheWrite1hInputTokens,
		f.CacheWrite30mInputTokens,
		f.OutputTokensTotal,
		f.ReasoningOutputTokens,
	}
	for _, count := range counts {
		switch count.State {
		case CountKnown:
			if count.Value < 0 {
				return false
			}
		case CountNotApplicable, CountUnknown:
			if count.Value != 0 {
				return false
			}
		default:
			return false
		}
	}

	if f.ReasoningOutputTokens.IsKnown() &&
		f.OutputTokensTotal.IsKnown() &&
		f.ReasoningOutputTokens.Value > f.OutputTokensTotal.Value {
		return false
	}

	seenKinds := make(map[MeteredKind]struct{}, len(f.ServerToolUsage))
	for _, item := range f.ServerToolUsage {
		if !item.Valid() {
			return false
		}
		if _, ok := seenKinds[item.Kind]; ok {
			return false
		}
		seenKinds[item.Kind] = struct{}{}
	}

	return true
}

// Source 表示一次用量事实的来源轨道，用于 settlement 幂等校验。
type Source string

const (
	// SourceUpstreamResponse 表示 usage 来自非流式上游响应。
	SourceUpstreamResponse Source = "upstream_response"

	// SourceUpstreamStream 表示 usage 来自流式 final usage 事实。
	SourceUpstreamStream Source = "upstream_stream"

	// SourcePartialStreamEstimate 表示已向客户端 emit 但上游无 final usage 时，gateway 合成的估算事实。
	// 用于流式 partial settlement（客户端取消 / emit 后中断 / 正常结束缺 usage），与上游真实 usage 严格区分。
	SourcePartialStreamEstimate Source = "partial_stream_estimate"
)

// Valid 判断 Source 是否为已登记来源。
func (s Source) Valid() bool {
	switch s {
	case SourceUpstreamResponse, SourceUpstreamStream, SourcePartialStreamEstimate:
		return true
	default:
		return false
	}
}

// IsPartialEstimate 判断该来源是否为 gateway 合成的 partial 估算事实（非上游真实 usage）。
func (s Source) IsPartialEstimate() bool {
	return s == SourcePartialStreamEstimate
}
