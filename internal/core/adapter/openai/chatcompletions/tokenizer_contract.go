package chatcompletions

// ChatInputTokenizer 定义某个 OpenAI 协议族 provider adapter 对 chat 输入 token 的计数能力。
type ChatInputTokenizer interface {
	// CountChatInputTokens 按 provider 即将发送的 OpenAI wire 请求做保守估算。
	//
	// 不能只接收 messages：tools、response_format 和 vendor extensions 也可能增加输入 token。
	CountChatInputTokens(req ChatRequest) (int64, error)
}
