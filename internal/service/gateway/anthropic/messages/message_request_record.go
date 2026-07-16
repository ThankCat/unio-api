package messages

import (
	"context"

	gatewayapi "github.com/ThankCat/unio-gateway/internal/app/gatewayapi/anthropic/messages"
	"github.com/ThankCat/unio-gateway/internal/core/auth"
	"github.com/ThankCat/unio-gateway/internal/core/requestlog"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/lifecycle"
)

// message_request_record.go 内的方法是 lifecycle.RequestLifecycle 的 receiver-bound forward。
// 真正的 request log 创建 / 失败流程（含协议常量写入、request_id 生成、log correlation id 注入、
// 错误 facts 摘要 + 截断、客户端取消脱离 ctx 的补偿窗口）在 lifecycle 包共享，OpenAI 与 Anthropic
// 两侧 service 调用同一份实现，避免逐字复制。协议族 ad-hoc string code 文案映射通过 service.go
// 注入的 messagesSafeMessage 闭包提供。

func (s *MessagesService) createMessageRequestRecord(ctx context.Context, principal *auth.APIKeyPrincipal, req gatewayapi.MessageRequest, stream bool) (requestlog.RequestRecord, error) {
	// 推理强度优先取 output_config.effort（官方档位，透传在 Extensions 里），缺失再退回 thinking.budget_tokens。
	reasoning := lifecycle.NormalizeAnthropicReasoning(req.Extensions["output_config"], req.Thinking)
	return s.lifecycle.CreateRequest(ctx, principal, req.Model, stream, reasoning)
}

func (s *MessagesService) markRequestRecordFailed(ctx context.Context, requestRecord requestlog.RequestRecord, code string, err error) {
	s.lifecycle.MarkRequestFailed(ctx, requestRecord, code, err)
}
