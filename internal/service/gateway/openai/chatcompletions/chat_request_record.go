package chatcompletions

import (
	"context"

	gatewayapi "github.com/ThankCat/unio-gateway/internal/app/gatewayapi/openai/chatcompletions"
	"github.com/ThankCat/unio-gateway/internal/core/auth"
	"github.com/ThankCat/unio-gateway/internal/core/requestlog"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/lifecycle"
)

// chat_request_record.go 内的方法是 lifecycle.RequestLifecycle 的 receiver-bound forward。
// 真正的 request log 创建 / 推进 / 失败 / 取消流程（含协议常量写入、request_id 生成、
// log correlation id 注入、错误 facts 摘要 + 截断、客户端取消脱离 ctx 的补偿窗口）
// 在 lifecycle 包共享，OpenAI 与 Anthropic 两侧 service 调用同一份实现，避免逐字复制。
// 协议族 ad-hoc string code 文案映射通过 service.go 注入的 chatCompletionsSafeMessage 闭包提供。

func (s *ChatCompletionService) createRequestRecord(ctx context.Context, principal *auth.APIKeyPrincipal, req gatewayapi.ChatCompletionRequest, stream bool) (requestlog.RequestRecord, error) {
	var effort string
	if req.ReasoningEffort != nil {
		effort = *req.ReasoningEffort
	}
	return s.lifecycle.CreateRequest(ctx, principal, req.Model, stream, lifecycle.NormalizeOpenAIEffort(effort, req.Model))
}

func (s *ChatCompletionService) markRequestRecordFailed(ctx context.Context, requestRecord requestlog.RequestRecord, code string, err error) {
	s.lifecycle.MarkRequestFailed(ctx, requestRecord, code, err)
}
