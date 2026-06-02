package deepseek

import "github.com/ThankCat/unio-api/internal/core/adapter/openai"

// CountChatInputTokens 按 DeepSeek OpenAI wire 做保守输入估算。
//
// 估算必须基于「实际会发送给上游的 wire」，因此先按 DEC-012 Drop 不支持字段，再交由 OpenAI
// 协议族的完整 wire JSON 估算 primitive。该入口仍独立于 Anthropic tokenizer，不建立跨协议 facade。
func (a *Adapter) CountChatInputTokens(req openai.ChatRequest) (int64, error) {
	cleaned, _ := dropUnsupported(req)
	return a.base.CountChatInputTokens(cleaned)
}
