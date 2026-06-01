package deepseek

import (
	"unicode/utf8"

	anthropicadapter "github.com/ThankCat/unio-api/internal/core/adapter/anthropic"
)

const (
	// charsPerToken 是保守的"字符数/ token"折算系数。
	//
	// 取 3 偏小，使估算偏大：authorization 宁可多冻结余额，也不能少冻结导致平台亏空。
	// 真实计费仍以 settlement 阶段上游返回 usage 为准。DS-ANT-15 负责校准本系数。
	charsPerToken = 3

	// perMessageOverheadTokens 是每条消息的 framing 开销估算（role、分隔符等）。
	perMessageOverheadTokens = 8

	// baseOverheadTokens 是请求级固定开销估算。
	baseOverheadTokens = 8
)

// Tokenizer 是 DeepSeek Anthropic 协议族的输入 token 估算器。
//
// 它按即将发送的 Anthropic wire（system、messages[].content[]、tools）做保守字符级估算，
// 返回 authorization 使用的输入 token 估算，不是 settlement 使用的 upstream usage 事实。
// 当前为字符启发式（无官方 tokenizer），偏向高估；DS-ANT-15 用真实 usage.input_tokens 校准。
type Tokenizer struct{}

// CountMessagesInputTokens 估算 Anthropic Messages 请求的输入 token（保守高估）。
func (Tokenizer) CountMessagesInputTokens(req anthropicadapter.MessagesInputTokenizeRequest) (int64, error) {
	runes := utf8.RuneCount(req.System) + utf8.RuneCount(req.Tools)
	for _, msg := range req.Messages {
		runes += utf8.RuneCount(msg.Content)
	}

	tokens := int64(runes)/charsPerToken +
		int64(len(req.Messages))*perMessageOverheadTokens +
		baseOverheadTokens

	return tokens, nil
}
