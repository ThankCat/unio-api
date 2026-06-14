package responses

import (
	"unicode/utf8"

	"github.com/ThankCat/unio-api/internal/platform/failure"
)

const (
	// charsPerToken 是保守的「字符数 / token」折算系数（authorization 宁可高估）。
	//
	// 直传 adapter 不持有 provider 私有 tokenizer，也不应因新模型名解析失败阻塞请求（GAP：tiktoken
	// 不识别新 model 族）。真实计费仍以 settlement 阶段上游 usage facts 为准；本估算只用于授权预扣保守边界。
	charsPerToken = 3

	// wireOverheadTokens 是请求级 framing 固定开销估算（HTTP JSON 字段名等）。
	wireOverheadTokens = 16
)

var _ ResponsesInputTokenizer = (*Adapter)(nil)

// CountResponsesInputTokens 按即将发往上游的 Responses 请求体做保守输入 token 估算。
//
// 与 anthropic 官方 1P tokenizer 同口径（wire 字符折算 + framing 开销），避免依赖按 model 选择的
// 精确 tokenizer 在新模型名上失败。
func (a *Adapter) CountResponsesInputTokens(req Request) (int64, error) {
	if len(req.Body) == 0 {
		return 0, failure.New(
			failure.CodeAdapterTokenizeFailed,
			failure.WithMessage("openai responses adapter tokenize empty request body"),
		)
	}

	tokens := int64(utf8.RuneCount(req.Body))/charsPerToken + wireOverheadTokens
	if tokens < 1 {
		tokens = 1
	}
	return tokens, nil
}
