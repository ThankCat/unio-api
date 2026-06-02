// Package deepseek 实现 DeepSeek 在 Anthropic 协议族下的具体 adapter。
//
// 它复用 Anthropic 协议族通用 wire 逻辑（请求编码、响应解析、SSE、usage、ResponseFacts），
// 叠加 DeepSeek 专属规则：按 DEC-012「协议为先」，无法转换的请求字段（不支持的 content block /
// 内置 server tool / 误导性 ignored 字段）在出站前 Drop（不进入 upstream body），而不是返回 400；
// 被 Drop 的字段名写入脱敏 warn 日志供审计。tokenizer 独立实现，不调用 OpenAI tokenizer，
// 也不共享 provider facade。
package deepseek

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/ThankCat/unio-api/internal/core/adapter"
	anthropicadapter "github.com/ThankCat/unio-api/internal/core/adapter/anthropic"
	"github.com/ThankCat/unio-api/internal/core/channel"
)

// Adapter 是 DeepSeek Anthropic endpoint 的 adapter。
type Adapter struct {
	base      *anthropicadapter.Adapter
	tokenizer Tokenizer
	logger    *slog.Logger
}

// NewAdapter 创建 DeepSeek Anthropic 协议族 adapter。
//
// logger 用于记录出站时被 Drop 的请求字段；传 nil 时使用 slog 默认 logger。
func NewAdapter(client *http.Client, logger *slog.Logger) *Adapter {
	if logger == nil {
		logger = slog.Default()
	}

	return &Adapter{
		base:   anthropicadapter.NewAdapter(client),
		logger: logger,
	}
}

// Messages 在调用上游前 Drop DeepSeek 无法转换的字段，其余复用通用 Anthropic 实现。
func (a *Adapter) Messages(ctx context.Context, ch channel.Runtime, req anthropicadapter.MessageRequest) (*anthropicadapter.MessageResponse, error) {
	cleaned, dropped := dropUnsupported(req)
	a.logDropped(ctx, dropped)

	return a.base.Messages(ctx, ch, cleaned)
}

// StreamMessages 在调用上游前 Drop DeepSeek 无法转换的字段，其余复用通用 Anthropic 实现。
func (a *Adapter) StreamMessages(ctx context.Context, ch channel.Runtime, req anthropicadapter.MessageRequest, emit func(anthropicadapter.MessageStreamEvent) error) (adapter.StreamOutcome, error) {
	cleaned, dropped := dropUnsupported(req)
	a.logDropped(ctx, dropped)

	return a.base.StreamMessages(ctx, ch, cleaned, emit)
}

// CountMessagesInputTokens 使用 DeepSeek Anthropic 专属 tokenizer 做保守输入估算。
func (a *Adapter) CountMessagesInputTokens(req anthropicadapter.MessagesInputTokenizeRequest) (int64, error) {
	return a.tokenizer.CountMessagesInputTokens(req)
}

// logDropped 记录本次请求被 Drop 的字段名。
//
// 只记录字段名，不记录字段值，避免把客户内容写入日志；空列表不产生日志。
func (a *Adapter) logDropped(ctx context.Context, dropped []string) {
	if len(dropped) == 0 {
		return
	}

	a.logger.WarnContext(ctx, "deepseek anthropic adapter dropped unsupported request fields",
		slog.String("protocol", "anthropic"),
		slog.String("adapter_key", "deepseek"),
		slog.Any("dropped_request_fields", dropped),
	)
}

var (
	_ anthropicadapter.MessagesAdapter        = (*Adapter)(nil)
	_ anthropicadapter.StreamMessagesAdapter  = (*Adapter)(nil)
	_ anthropicadapter.MessagesInputTokenizer = (*Adapter)(nil)
)
