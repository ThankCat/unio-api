package tokenest

import "strings"

// framing 开销常量，对齐 new-api EstimateRequestToken 的 OpenAI 分支：每条消息 +3、每个工具 +8、
// 每个带 name 的字段 +3、请求级 +3（回复引导）。用于近似 OpenAI num_tokens_from_messages 口径。
const (
	perMessageTokens = 3
	perToolTokens    = 8
	perNameTokens    = 3
	replyPrimeTokens = 3
)

// Builder 累积一次请求的「文本内容 + 计数 + 媒体清单」，最终 Estimate 折算成 token 总数。
// 各协议 adapter 遍历自己的 content JSON 往这里喂料，不直接接触 tiktoken / tile 数学。
type Builder struct {
	model    string
	texts    []string
	messages int
	tools    int
	names    int
	media    []Media
}

// NewBuilder 创建针对某模型的估算累加器。
func NewBuilder(model string) *Builder {
	return &Builder{model: model}
}

// AddText 累积一段文本内容（消息文本、system、工具名/参数等）。空串忽略。
func (b *Builder) AddText(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	b.texts = append(b.texts, text)
}

// AddMessage 记一条消息（用于每消息框架开销）。
func (b *Builder) AddMessage() { b.messages++ }

// AddTool 记一个工具定义（用于每工具框架开销）。
func (b *Builder) AddTool() { b.tools++ }

// AddName 记一个带 name 的字段（用于每 name 框架开销）。
func (b *Builder) AddName() { b.names++ }

// AddMedia 记一条多模态附件。
func (b *Builder) AddMedia(m Media) { b.media = append(b.media, m) }

// Count 折算成 token 总数：内容文本走 tiktoken + 框架开销 + 各附件 token。至少返回 1。
func (b *Builder) Count() int64 {
	opts := activeOptions()

	total := CountText(b.model, strings.Join(b.texts, "\n"))
	total += int64(b.messages)*perMessageTokens +
		int64(b.tools)*perToolTokens +
		int64(b.names)*perNameTokens +
		replyPrimeTokens

	for _, m := range b.media {
		total += int64(mediaTokens(b.model, m, opts))
	}

	if total < 1 {
		total = 1
	}
	return total
}
