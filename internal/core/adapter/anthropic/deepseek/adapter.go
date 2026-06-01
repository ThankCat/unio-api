// Package deepseek 实现 DeepSeek 在 Anthropic 协议族下的具体 adapter。
//
// 它复用 Anthropic 协议族通用 wire 逻辑（请求编码、响应解析、SSE、usage、ResponseFacts），
// 叠加 DeepSeek 专属规则：请求前置 Reject（不支持的 content block / server tool / 误导性 ignored
// 字段）与独立的 Anthropic 输入 tokenizer。它不调用 OpenAI tokenizer，也不共享 provider facade。
package deepseek

import (
	"context"
	"net/http"

	"github.com/ThankCat/unio-api/internal/core/adapter"
	anthropicadapter "github.com/ThankCat/unio-api/internal/core/adapter/anthropic"
	"github.com/ThankCat/unio-api/internal/core/channel"
)

// Adapter 是 DeepSeek Anthropic endpoint 的 adapter。
type Adapter struct {
	base      *anthropicadapter.Adapter
	tokenizer Tokenizer
}

// NewAdapter 创建 DeepSeek Anthropic 协议族 adapter。
func NewAdapter(client *http.Client) *Adapter {
	return &Adapter{base: anthropicadapter.NewAdapter(client)}
}

// Messages 在调用上游前拒绝 DeepSeek 不支持的字段，其余复用通用 Anthropic 实现。
func (a *Adapter) Messages(ctx context.Context, ch channel.Runtime, req anthropicadapter.MessageRequest) (*anthropicadapter.MessageResponse, error) {
	if err := rejectUnsupportedRequest(req); err != nil {
		return nil, err
	}
	return a.base.Messages(ctx, ch, req)
}

// StreamMessages 在调用上游前拒绝 DeepSeek 不支持的字段，其余复用通用 Anthropic 实现。
func (a *Adapter) StreamMessages(ctx context.Context, ch channel.Runtime, req anthropicadapter.MessageRequest, emit func(anthropicadapter.MessageStreamEvent) error) (adapter.StreamOutcome, error) {
	if err := rejectUnsupportedRequest(req); err != nil {
		return adapter.StreamOutcome{}, err
	}
	return a.base.StreamMessages(ctx, ch, req, emit)
}

// CountMessagesInputTokens 使用 DeepSeek Anthropic 专属 tokenizer 做保守输入估算。
func (a *Adapter) CountMessagesInputTokens(req anthropicadapter.MessagesInputTokenizeRequest) (int64, error) {
	return a.tokenizer.CountMessagesInputTokens(req)
}

var (
	_ anthropicadapter.MessagesAdapter        = (*Adapter)(nil)
	_ anthropicadapter.StreamMessagesAdapter  = (*Adapter)(nil)
	_ anthropicadapter.MessagesInputTokenizer = (*Adapter)(nil)
)
