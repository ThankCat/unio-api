package messages

import (
	"github.com/ThankCat/unio-api/internal/core/tokenest"
)

// EstimateMessagesInputTokens 估算 Anthropic Messages 请求的输入 token（new-api 口径）。
//
// Anthropic 无公开本地 tokenizer：对提取出的文本内容用 tiktoken 近似 + 每消息/工具框架开销，
// 图片走 tile/像素数学、文档按固定值；绝不把整包 wire JSON 或 base64 当文本计数
// （旧实现按整包字符估算会放大数倍）。真实计费仍以 settlement 阶段上游 usage 为准。
func EstimateMessagesInputTokens(req MessagesInputTokenizeRequest) (int64, error) {
	return buildMessagesEstimate(req).Count(), nil
}

// CountMessagesInputTokens 估算 Anthropic Messages 请求的输入 token（官方 wire 保守估算）。
func (a *Adapter) CountMessagesInputTokens(req MessagesInputTokenizeRequest) (int64, error) {
	return EstimateMessagesInputTokens(req)
}

// CountOutputTokens 估算一段 assistant 输出文本的 token 数（tiktoken 近似，claude 族用 cl100k 编码）。
//
// 仅用于流式 partial settlement：上游未返回 final usage 时，对「已 emit 的可见文本」做保守估算。
// 空文本返回 0（不计费）。这是估算而非上游真实 usage，调用方须标记 partial_stream_estimate。
func CountOutputTokens(text string) int64 {
	return tokenest.CountText("claude", text)
}
