package tokenest

import (
	"errors"
	"strings"
	"unicode/utf8"

	tiktoken "github.com/tiktoken-go/tokenizer"
)

// CountText 用 tiktoken 估算一段文本的 token 数；模型无法解析时回退到最新 OpenAI 编码（o200k_base）。
// 这是「内容文本」的计数入口——调用方只应传入真实文本（消息内容、工具名/参数、system 等），
// 不应传入整包 wire JSON 或 base64（那正是旧实现放大估算的根源）。
func CountText(model, text string) int64 {
	if text == "" {
		return 0
	}
	codec, err := codecForModel(model)
	if err != nil || codec == nil {
		// 极端兜底：tiktoken 不可用时按 rune 数保守估算（1 char ≈ 1 token，偏高但安全）。
		return int64(utf8.RuneCountInString(text))
	}
	count, err := codec.Count(text)
	if err != nil {
		return int64(utf8.RuneCountInString(text))
	}
	return int64(count)
}

// codecForModel 按模型名解析 tiktoken 编码器：优先精确匹配，失败按模型族回退，未知族用 o200k_base。
func codecForModel(model string) (tiktoken.Codec, error) {
	model = normalizeModel(model)
	if model == "" {
		return tiktoken.Get(tiktoken.O200kBase)
	}

	if codec, err := tiktoken.ForModel(tiktoken.Model(model)); err == nil {
		return codec, nil
	} else if !errors.Is(err, tiktoken.ErrModelNotSupported) {
		return nil, err
	}

	return tiktoken.Get(fallbackEncoding(model))
}

// fallbackEncoding 为 tiktoken 尚未识别的新模型族选择兼容编码；未知一律回退最新 o200k_base。
func fallbackEncoding(model string) tiktoken.Encoding {
	switch {
	case hasModelPrefix(model, "gpt-5"),
		hasModelPrefix(model, "gpt-4.1"),
		hasModelPrefix(model, "gpt-4o"),
		hasModelPrefix(model, "o1"),
		hasModelPrefix(model, "o3"),
		hasModelPrefix(model, "o4"):
		return tiktoken.O200kBase
	case hasModelPrefix(model, "gpt-4"),
		hasModelPrefix(model, "gpt-3.5"),
		hasModelPrefix(model, "deepseek"),
		hasModelPrefix(model, "claude"):
		return tiktoken.Cl100kBase
	default:
		return tiktoken.O200kBase
	}
}

func hasModelPrefix(model, prefix string) bool {
	return model == prefix || strings.HasPrefix(model, prefix+"-")
}

// normalizeModel 去掉代理路由前缀（如 openrouter 的 provider/model），便于 fallback 匹配。
func normalizeModel(model string) string {
	model = strings.TrimSpace(model)
	if slash := strings.LastIndex(model, "/"); slash >= 0 {
		model = model[slash+1:]
	}
	return model
}
