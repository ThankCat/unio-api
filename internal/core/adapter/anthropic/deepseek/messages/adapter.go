// Package messages 实现 DeepSeek 在 Anthropic 协议族下的具体 adapter。
//
// 它复用 Anthropic 协议族通用 wire 逻辑（请求编码、响应解析、SSE、usage、ResponseFacts），
// 叠加 DeepSeek 专属规则：按 DEC-012「协议为先」，无法转换的请求字段（不支持的 content block /
// 内置 server tool / 误导性 ignored 字段）在出站前 Drop（不进入 upstream body），而不是返回 400；
// 被 Drop 的字段名写入脱敏 warn 日志供审计。tokenizer 独立实现，不调用 OpenAI tokenizer，
// 也不共享 provider facade。
package messages

import (
	"context"
	"net/http"

	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	messagesadapter "github.com/ThankCat/unio-gateway/internal/core/adapter/anthropic/messages"
	"github.com/ThankCat/unio-gateway/internal/core/channel"
)

// Adapter 是 DeepSeek Anthropic endpoint 的 adapter。
type Adapter struct {
	base      *messagesadapter.Adapter
	tokenizer Tokenizer
	logger    *zap.Logger
}

// NewAdapter 创建 DeepSeek Anthropic 协议族 adapter。
//
// logger 用于记录出站时被 Drop 的请求字段；传 nil 时使用 zap no-op logger。
func NewAdapter(client *http.Client, logger *zap.Logger) *Adapter {
	if logger == nil {
		logger = zap.NewNop()
	}

	return &Adapter{
		base:   messagesadapter.NewAdapter(client),
		logger: logger,
	}
}

// Messages 在调用上游前 Drop DeepSeek 无法转换的字段，其余复用通用 Anthropic 实现。
func (a *Adapter) Messages(ctx context.Context, ch channel.Runtime, req messagesadapter.MessageRequest) (*messagesadapter.MessageResponse, error) {
	cleaned, dropped := dropUnsupported(req)
	a.logDropped(ctx, dropped)

	return a.base.Messages(ctx, ch, cleaned)
}

// StreamMessages 在调用上游前 Drop DeepSeek 无法转换的字段，其余复用通用 Anthropic 实现。
func (a *Adapter) StreamMessages(ctx context.Context, ch channel.Runtime, req messagesadapter.MessageRequest, emit func(messagesadapter.MessageStreamEvent) error) (adapter.StreamOutcome, error) {
	cleaned, dropped := dropUnsupported(req)
	a.logDropped(ctx, dropped)

	return a.base.StreamMessages(ctx, ch, cleaned, emit)
}

// CountMessagesInputTokens 使用 DeepSeek Anthropic 专属 tokenizer 做保守输入估算。
func (a *Adapter) CountMessagesInputTokens(req messagesadapter.MessagesInputTokenizeRequest) (int64, error) {
	return a.tokenizer.CountMessagesInputTokens(req)
}

// logDropped 记录本次请求被 Drop 的字段名。
//
// 只记录字段名，不记录字段值，避免把客户内容写入日志；空列表不产生日志。
func (a *Adapter) logDropped(ctx context.Context, dropped []string) {
	if len(dropped) == 0 {
		return
	}

	a.logger.Warn("deepseek anthropic adapter dropped unsupported request fields",
		zap.String("protocol", "anthropic"),
		zap.String("adapter_key", "deepseek"),
		zap.Any("dropped_request_fields", dropped),
	)
}

var (
	_ messagesadapter.MessagesAdapter        = (*Adapter)(nil)
	_ messagesadapter.StreamMessagesAdapter  = (*Adapter)(nil)
	_ messagesadapter.MessagesInputTokenizer = (*Adapter)(nil)
)
