package responses

import (
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

var _ ResponsesInputTokenizer = (*Adapter)(nil)

// CountResponsesInputTokens 估算一次 Responses 请求的输入 token（new-api 口径）。
//
// 解析即将上送的请求体，只对提取出的文本内容跑 tiktoken + 每消息/工具框架开销，图片走 tile/patch
// 数学、音频/文件按固定值；绝不把整包 JSON 或 base64 当文本计数（旧实现按整包字符估算会放大数倍）。
func (a *Adapter) CountResponsesInputTokens(req Request) (int64, error) {
	if len(req.Body) == 0 {
		return 0, failure.New(
			failure.CodeAdapterTokenizeFailed,
			failure.WithMessage("openai responses adapter tokenize empty request body"),
		)
	}
	return buildResponsesEstimate(req.Body).Count(), nil
}
