package messages

import (
	"strings"
	"unicode/utf8"

	"github.com/ThankCat/unio-api/internal/platform/failure"
)

const (
	// charsPerToken 是保守的「字符数 / token」折算系数（authorization 宁可高估）。
	//
	// 真实计费仍以 settlement 阶段上游 usage 为准；DS-ANT-15 负责用黑盒校准本系数。
	charsPerToken = 3

	// wireOverheadTokens 是请求级 framing 固定开销估算（HTTP JSON 字段名等）。
	wireOverheadTokens = 16
)

// EstimateMessagesInputTokens 按即将发送的完整 Anthropic wire JSON 做保守输入 token 估算。
//
// 官方 1P adapter 零 Drop，因此直接对 buildMessagesRequestBody 产物估算；DeepSeek 仍使用
// 其 drop 后 wire 的专属 tokenizer（见 adapter/anthropic/deepseek/tokenizer.go）。
func EstimateMessagesInputTokens(req MessagesInputTokenizeRequest) (int64, error) {
	model := strings.TrimSpace(req.Model)
	if model == "" {
		return 0, failure.New(
			failure.CodeAdapterTokenizeFailed,
			failure.WithMessage("anthropic tokenizer model is empty"),
		)
	}

	body, err := buildMessagesRequestBody(MessageRequest{
		Model:    model,
		System:   req.System,
		Messages: req.Messages,
		Tools:    req.Tools,
	})
	if err != nil {
		return 0, failure.Wrap(
			failure.CodeAdapterTokenizeFailed,
			err,
			failure.WithMessage("anthropic tokenizer build wire request"),
		)
	}

	tokens := int64(utf8.RuneCount(body))/charsPerToken + wireOverheadTokens
	if tokens < 1 {
		tokens = 1
	}

	return tokens, nil
}

// CountMessagesInputTokens 估算 Anthropic Messages 请求的输入 token（官方 wire 保守估算）。
func (a *Adapter) CountMessagesInputTokens(req MessagesInputTokenizeRequest) (int64, error) {
	return EstimateMessagesInputTokens(req)
}

// CountOutputTokens 估算一段 assistant 输出文本的 token 数（与输入同口径的 chars/token 启发式）。
//
// 仅用于流式 partial settlement：上游未返回 final usage 时，对「已 emit 的可见文本」做保守估算。
// 不含 wireOverheadTokens（纯输出文本，无 framing）。空文本返回 0（不计费）。
func CountOutputTokens(text string) int64 {
	if strings.TrimSpace(text) == "" {
		return 0
	}
	tokens := int64(utf8.RuneCount([]byte(text))) / charsPerToken
	if tokens < 1 {
		tokens = 1
	}
	return tokens
}
