package deepseek

import "github.com/ThankCat/unio-api/internal/core/adapter/openai"

// CountChatInputTokens 按 DeepSeek OpenAI wire 做保守输入估算。
//
// DeepSeek OpenAI endpoint 使用 OpenAI-compatible wire，因此复用 OpenAI 协议族的完整
// wire JSON 估算 primitive。该入口仍独立于 Anthropic tokenizer，不建立跨协议 facade。
func (a *Adapter) CountChatInputTokens(req openai.ChatRequest) (int64, error) {
	return a.base.CountChatInputTokens(req)
}
