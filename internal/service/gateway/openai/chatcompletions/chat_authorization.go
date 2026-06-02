package chatcompletions

import (
	"context"

	"github.com/ThankCat/unio-api/internal/service/gateway/lifecycle"
)

// chat_authorization.go 内的方法是 lifecycle.RequestLifecycle 的 receiver-bound forward。
// 真正的余额冻结 release 流程（含脱离客户端取消 ctx + 5s 补偿窗口）在 lifecycle 包
// 共享，OpenAI 与 Anthropic 两侧 service 调用同一份实现，避免逐字复制。
//
// 余额冻结类型与 ChatAuthorizationService 实现见 service/gateway/lifecycle/authorization.go。

func (s *ChatCompletionService) releaseChatAuthorization(ctx context.Context, authorization lifecycle.ChatAuthorization) error {
	return s.lifecycle.ReleaseAuthorization(ctx, authorization)
}

func (s *ChatCompletionService) releaseChatAuthorizationForBillingException(ctx context.Context, authorization lifecycle.ChatAuthorization, reasonCode string, reason string) error {
	return s.lifecycle.ReleaseAuthorizationForBillingException(ctx, authorization, reasonCode, reason)
}
