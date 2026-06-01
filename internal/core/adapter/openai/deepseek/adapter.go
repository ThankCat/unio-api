// Package deepseek 实现 DeepSeek 在 OpenAI 协议族下的具体 adapter。
//
// 它复用 OpenAI 协议族通用 wire 逻辑（请求编码、响应解析、SSE、usage、ResponseFacts），
// 并叠加 DeepSeek 专属规则（当前为请求前置 Reject）。后续会把 adapter/openai 根目录的通用
// 逻辑抽成可复用函数，并把 streamtranslate 收口到本包；当前先以组合方式复用 openai.Adapter。
package deepseek

import (
	"context"
	"net/http"

	"github.com/ThankCat/unio-api/internal/core/adapter"
	"github.com/ThankCat/unio-api/internal/core/adapter/openai"
	"github.com/ThankCat/unio-api/internal/core/adapter/openai/streamtranslate"
	"github.com/ThankCat/unio-api/internal/core/channel"
)

// Adapter 是 DeepSeek OpenAI endpoint 的 adapter。
type Adapter struct {
	base *openai.Adapter
}

// NewAdapter 创建 DeepSeek OpenAI 协议族 adapter。
func NewAdapter(client *http.Client) *Adapter {
	translators := streamtranslate.NewRegistry(
		streamtranslate.Default{},
		streamtranslate.DeepSeek{},
	)

	return &Adapter{base: openai.NewAdapter(client, translators)}
}

// ChatCompletions 在调用上游前拒绝 DeepSeek 不支持的字段，其余复用通用 OpenAI 实现。
func (a *Adapter) ChatCompletions(ctx context.Context, ch channel.Runtime, req openai.ChatRequest) (*openai.ChatResponse, error) {
	if err := rejectUnsupportedRequest(req); err != nil {
		return nil, err
	}

	return a.base.ChatCompletions(ctx, ch, req)
}

// StreamChatCompletions 在调用上游前拒绝 DeepSeek 不支持的字段，其余复用通用 OpenAI 实现。
func (a *Adapter) StreamChatCompletions(ctx context.Context, ch channel.Runtime, req openai.ChatRequest, emit func(openai.ChatStreamChunk) error) (adapter.StreamOutcome, error) {
	if err := rejectUnsupportedRequest(req); err != nil {
		return adapter.StreamOutcome{}, err
	}

	return a.base.StreamChatCompletions(ctx, ch, req, emit)
}

var (
	_ openai.ChatAdapter        = (*Adapter)(nil)
	_ openai.StreamChatAdapter  = (*Adapter)(nil)
	_ openai.ChatInputTokenizer = (*Adapter)(nil)
)
