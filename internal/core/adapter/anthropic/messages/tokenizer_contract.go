package messages

// MessagesInputTokenizer 定义某个 Anthropic 协议族 provider adapter 对 Messages 输入 token 的计数能力。
//
// 它独立于 OpenAI tokenizer：消费 Anthropic Messages 内部请求 DTO，按 provider 即将发送的
// Anthropic wire（system、messages[].content[]、tools 与 framing）估算，返回 authorization
// 使用的保守输入 token 估算，不是 settlement 使用的 upstream usage 事实。
type MessagesInputTokenizer interface {
	CountMessagesInputTokens(req MessagesInputTokenizeRequest) (int64, error)
}

// MessagesInputTokenizeRequest 表示 adapter 计算 Messages 输入 token 所需的最小输入。
type MessagesInputTokenizeRequest struct {
	// Model 是 routing 选中的 upstream model。
	Model string

	// System 是顶层 system prompt 原始 JSON（string 或 text block 数组）。
	System []byte

	// Messages 是多轮对话消息。
	Messages []Message

	// Tools 是工具定义原始 JSON。
	Tools []byte
}
