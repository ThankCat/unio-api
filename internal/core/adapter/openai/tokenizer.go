package openai

import (
	"errors"
	"strings"

	"github.com/ThankCat/unio-api/internal/core/adapter"
	"github.com/ThankCat/unio-api/internal/platform/failure"
	tiktoken "github.com/tiktoken-go/tokenizer"
)

const (
	chatTokensPerMessage = 3
	chatReplyPrimer      = 3
)

// CountChatInputTokens 估算 OpenAI-compatible chat 请求的输入 token。
func (a *Adapter) CountChatInputTokens(req adapter.ChatInputTokenizeRequest) (int64, error) {
	codec, err := chatCodec(req.Model)
	if err != nil {
		return 0, err
	}

	total := int64(chatReplyPrimer)
	for _, msg := range req.Messages {
		roleTokens, err := countTextTokens(codec, msg.Role)
		if err != nil {
			return 0, err
		}

		contentTokens, err := countTextTokens(codec, msg.Content)
		if err != nil {
			return 0, err
		}

		total += int64(chatTokensPerMessage) + roleTokens + contentTokens
	}

	return total, nil
}

func chatCodec(model string) (tiktoken.Codec, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil, failure.New(
			failure.CodeAdapterTokenizeFailed,
			failure.WithMessage("openai tokenizer model is empty"),
		)
	}

	codec, err := tiktoken.ForModel(tiktoken.Model(model))
	if err == nil {
		return codec, nil
	}

	if errors.Is(err, tiktoken.ErrModelNotSupported) {
		if encoding, ok := fallbackEncoding(model); ok {
			codec, err := tiktoken.Get(encoding)
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
	}

	return nil, failure.Wrap(
		failure.CodeAdapterTokenizeFailed,
		err,
		failure.WithMessage("openai tokenizer resolve model"),
		failure.WithField("model", model),
	)
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
