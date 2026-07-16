package messages

import (
	messagesadapter "github.com/ThankCat/unio-gateway/internal/core/adapter/anthropic/messages"
)

// Tokenizer 是 DeepSeek Anthropic 协议族的输入 token 估算器。
//
// 直接复用官方 Anthropic 估算（tiktoken 近似 + 每消息/工具框架开销 + 图片 tile 数学，不数 base64），
// 返回 authorization/限流使用的输入 token 估算，而非 settlement 使用的 upstream usage 事实。
// DeepSeek 出站会 Drop 多模态块，本估算仍计入图片属保守上界，不影响资金安全。
type Tokenizer struct{}

// CountMessagesInputTokens 估算 Anthropic Messages 请求的输入 token（委托官方 wire 估算）。
func (Tokenizer) CountMessagesInputTokens(req messagesadapter.MessagesInputTokenizeRequest) (int64, error) {
	return messagesadapter.EstimateMessagesInputTokens(req)
}
