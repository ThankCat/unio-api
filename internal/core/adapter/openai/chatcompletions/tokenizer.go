package chatcompletions

import (
	"github.com/ThankCat/unio-gateway/internal/core/tokenest"
)

// CountChatInputTokens 估算 OpenAI-compatible chat 请求的输入 token（new-api 口径）。
//
// 只对提取出的文本内容跑 tiktoken + 每消息/工具框架开销，图片走 tile/patch 数学、音频/文件按固定值；
// 绝不把整包 wire JSON 或 base64 当文本计数（旧实现按整包字符估算会放大数倍）。
func (a *Adapter) CountChatInputTokens(req ChatRequest) (int64, error) {
	return buildChatEstimate(req).Count(), nil
}

// CountOutputTokens 估算一段 assistant 输出文本的 token 数，按 upstream model 选 tiktoken 编码。
//
// 仅用于流式 partial settlement：上游未返回 final usage 时，对「已 emit 的可见文本」做保守估算。
// 空文本返回 0（不计费）。这是估算而非上游真实 usage，调用方须标记 partial_stream_estimate。
func CountOutputTokens(model string, text string) (int64, error) {
	return tokenest.CountText(model, text), nil
}
