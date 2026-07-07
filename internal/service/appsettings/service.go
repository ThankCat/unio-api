package appsettings

import (
	"context"

	messagesadapter "github.com/ThankCat/unio-api/internal/core/adapter/anthropic/messages"
)

// Service 是 admin 侧读写全局 provider 设置的服务(薄封装 Store)。
type Service struct {
	store Store
}

// NewService 创建 provider 设置服务。
func NewService(store Store) *Service {
	return &Service{store: store}
}

// GetAnthropicBetaPolicy 读取当前 Anthropic beta 策略(未设置返回默认)。
func (s *Service) GetAnthropicBetaPolicy(ctx context.Context) (messagesadapter.BetaPolicy, error) {
	return GetAnthropicBetaPolicy(ctx, s.store)
}

// SetAnthropicBetaPolicy 校验并写入 Anthropic beta 策略。
func (s *Service) SetAnthropicBetaPolicy(ctx context.Context, policy messagesadapter.BetaPolicy) error {
	return SetAnthropicBetaPolicy(ctx, s.store, policy)
}
