package chatcompletions

import (
	"errors"
	"strings"

	"github.com/ThankCat/unio-api/internal/platform/failure"
	tiktoken "github.com/tiktoken-go/tokenizer"
)

// CountChatInputTokens 估算 OpenAI-compatible chat 请求的输入 token。
//
// 这里按即将发送的完整 wire JSON 估算，覆盖 messages、tools、response_format 和
// vendor extensions。authorization 可以偏保守，但不能因只统计 messages 低估平台风险。
func (a *Adapter) CountChatInputTokens(req ChatRequest) (int64, error) {
	codec, err := chatCodec(req.Model)
	if err != nil {
		return 0, err
	}

	body, err := buildChatCompletionRequestBody(req, false)
	if err != nil {
		return 0, failure.Wrap(
			failure.CodeAdapterTokenizeFailed,
			err,
			failure.WithMessage("openai tokenizer build wire request"),
		)
	}

	return countTextTokens(codec, string(body))
}

// CountOutputTokens 估算一段 assistant 输出文本的 token 数，按 upstream model 选 tiktoken 编码。
//
// 仅用于流式 partial settlement：上游未返回 final usage 时，对「已 emit 的可见文本」做保守估算。
// 空文本返回 0（不计费）。这是估算而非上游真实 usage，调用方须标记 partial_stream_estimate。
func CountOutputTokens(model string, text string) (int64, error) {
	if strings.TrimSpace(text) == "" {
		return 0, nil
	}
	codec, err := chatCodec(model)
	if err != nil {
		return 0, err
	}
	return countTextTokens(codec, text)
}

func chatCodec(model string) (tiktoken.Codec, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil, failure.New(
			failure.CodeAdapterTokenizeFailed,
			failure.WithMessage("openai tokenizer model is empty"),
		)
	}

	codec, err := tiktoken.ForModel(tiktoken.Model(normalizeTokenizerModel(model)))
	if err == nil {
		return codec, nil
	}

	if !errors.Is(err, tiktoken.ErrModelNotSupported) {
		return nil, failure.Wrap(
			failure.CodeAdapterTokenizeFailed,
			err,
			failure.WithMessage("openai tokenizer resolve model"),
			failure.WithField("model", model),
		)
	}

	encoding, ok := fallbackEncoding(model)
	if !ok {
		// 未知模型族：authorization 估算用最新 encoding 兜底，避免因新模型名阻塞请求。
		encoding = tiktoken.O200kBase
	}
	codec, err = tiktoken.Get(encoding)
	if err != nil {
		return nil, failure.Wrap(
			failure.CodeAdapterTokenizeFailed,
			err,
			failure.WithMessage("openai tokenizer resolve fallback encoding"),
			failure.WithField("model", model),
		)
	}
	return codec, nil
}

func countTextTokens(codec tiktoken.Codec, text string) (int64, error) {
	count, err := codec.Count(text)
	if err != nil {
		return 0, failure.Wrap(
			failure.CodeAdapterTokenizeFailed,
			err,
			failure.WithMessage("openai tokenizer count text"),
		)
	}
	return int64(count), nil
}

// fallbackEncoding 为 tokenizer 尚未识别的新模型族选择兼容编码。
func fallbackEncoding(model string) (tiktoken.Encoding, bool) {
	model = normalizeTokenizerModel(model)
	switch {
	case hasModelPrefix(model, "gpt-5"),
		hasModelPrefix(model, "gpt-4.1"),
		hasModelPrefix(model, "gpt-4o"),
		hasModelPrefix(model, "o1"),
		hasModelPrefix(model, "o3"),
		hasModelPrefix(model, "o4"):
		return tiktoken.O200kBase, true
	case hasModelPrefix(model, "gpt-4"),
		hasModelPrefix(model, "gpt-3.5"):
		return tiktoken.Cl100kBase, true
	case hasModelPrefix(model, "deepseek"):
		return tiktoken.Cl100kBase, true
	default:
		return "", false
	}
}

func hasModelPrefix(model string, prefix string) bool {
	return model == prefix || strings.HasPrefix(model, prefix+"-")
}

// normalizeTokenizerModel 去掉代理路由前缀（如 openrouter 的 provider/model），便于 fallback 匹配。
func normalizeTokenizerModel(model string) string {
	model = strings.TrimSpace(model)
	if slash := strings.LastIndex(model, "/"); slash >= 0 {
		model = model[slash+1:]
	}
	return model
}
