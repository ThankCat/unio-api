// Package deepseek 实现 DeepSeek 在 OpenAI 协议族下的具体 adapter。
//
// 它复用 OpenAI 协议族通用 wire 逻辑（请求编码、响应解析、SSE、usage、ResponseFacts），
// 并叠加 DeepSeek 专属规则。按 DEC-012「协议为先」，无法转换的请求字段在出站前 Drop
// （不进入 upstream body），而不是返回 400；被 Drop 的字段名写入脱敏 warn 日志供审计。
//
// 流式翻译已收口：DeepSeek 流式与 OpenAI 基线一致（reasoning_content 是已登记扩展，由 base
// 直接透传），没有 DeepSeek 专属 stream 差异，因此本包不再持有 stream translator；如未来出现
// DeepSeek 专属流式 framing，再新增 stream.go 在调用 base 前后收口。
package deepseek

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/ThankCat/unio-api/internal/core/adapter"
	"github.com/ThankCat/unio-api/internal/core/adapter/openai"
	"github.com/ThankCat/unio-api/internal/core/channel"
)

// Adapter 是 DeepSeek OpenAI endpoint 的 adapter。
type Adapter struct {
	base   *openai.Adapter
	logger *slog.Logger
}

// NewAdapter 创建 DeepSeek OpenAI 协议族 adapter。
//
// logger 用于记录出站时被 Drop 的请求字段；传 nil 时使用 slog 默认 logger。
func NewAdapter(client *http.Client, logger *slog.Logger) *Adapter {
	if logger == nil {
		logger = slog.Default()
	}

	return &Adapter{
		base:   openai.NewAdapter(client),
		logger: logger,
	}
}

// ChatCompletions 在调用上游前 Drop DeepSeek 无法转换的字段，其余复用通用 OpenAI 实现。
func (a *Adapter) ChatCompletions(ctx context.Context, ch channel.Runtime, req openai.ChatRequest) (*openai.ChatResponse, error) {
	cleaned, dropped := dropUnsupported(req)
	a.logDropped(ctx, dropped)

	return a.base.ChatCompletions(ctx, ch, cleaned)
}

// StreamChatCompletions 在调用上游前 Drop DeepSeek 无法转换的字段，其余复用通用 OpenAI 实现。
func (a *Adapter) StreamChatCompletions(ctx context.Context, ch channel.Runtime, req openai.ChatRequest, emit func(openai.ChatStreamChunk) error) (adapter.StreamOutcome, error) {
	cleaned, dropped := dropUnsupported(req)
	a.logDropped(ctx, dropped)

	return a.base.StreamChatCompletions(ctx, ch, cleaned, emit)
}

// logDropped 记录本次请求被 Drop 的字段名。
//
// 只记录字段名，不记录字段值，避免把客户内容写入日志；空列表不产生日志。
func (a *Adapter) logDropped(ctx context.Context, dropped []string) {
	if len(dropped) == 0 {
		return
	}

	a.logger.WarnContext(ctx, "deepseek openai adapter dropped unsupported request fields",
		slog.String("protocol", "openai"),
		slog.String("adapter_key", "deepseek"),
		slog.Any("dropped_request_fields", dropped),
	)
}

var (
	_ openai.ChatAdapter        = (*Adapter)(nil)
	_ openai.StreamChatAdapter  = (*Adapter)(nil)
	_ openai.ChatInputTokenizer = (*Adapter)(nil)
)
