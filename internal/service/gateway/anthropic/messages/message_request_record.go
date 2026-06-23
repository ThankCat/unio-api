package messages

import (
	"context"

	gatewayapi "github.com/ThankCat/unio-api/internal/app/gatewayapi/anthropic/messages"
	"github.com/ThankCat/unio-api/internal/core/auth"
	"github.com/ThankCat/unio-api/internal/core/requestlog"
	"github.com/ThankCat/unio-api/internal/core/routing"
)

// message_request_record.go 内的方法是 lifecycle.RequestLifecycle 的 receiver-bound forward。
// 真正的 request log 创建 / 推进 / 失败 / 取消流程（含协议常量写入、request_id 生成、
// log correlation id 注入、错误 facts 摘要 + 截断、客户端取消脱离 ctx 的补偿窗口）
// 在 lifecycle 包共享，OpenAI 与 Anthropic 两侧 service 调用同一份实现，避免逐字复制。
// 协议族 ad-hoc string code 文案映射通过 service.go 注入的 messagesSafeMessage 闭包提供。

func (s *MessagesService) createMessageRequestRecord(ctx context.Context, principal *auth.APIKeyPrincipal, req gatewayapi.MessageRequest, stream bool) (requestlog.RequestRecord, error) {
	return s.lifecycle.CreateRequest(ctx, principal, req.Model, stream)
}

func (s *MessagesService) createAttemptRecord(ctx context.Context, requestRecord requestlog.RequestRecord, attemptIndex int, candidate routing.ChatRouteCandidate) (requestlog.AttemptRecord, error) {
	return s.lifecycle.CreateAttempt(ctx, requestRecord, attemptIndex, candidate)
}

func (s *MessagesService) markRequestRecordFailed(ctx context.Context, requestRecord requestlog.RequestRecord, code string, err error) {
	s.lifecycle.MarkRequestFailed(ctx, requestRecord, code, err)
}

func (s *MessagesService) markAttemptRecordFailed(ctx context.Context, attempt requestlog.AttemptRecord, code string, err error) {
	s.lifecycle.MarkAttemptFailed(ctx, attempt, code, err)
}

func (s *MessagesService) markRequestCanceled(ctx context.Context, requestRecord requestlog.RequestRecord, attemptRecord requestlog.AttemptRecord, err error) {
	s.lifecycle.MarkRequestCanceled(ctx, requestRecord, attemptRecord, err)
}
