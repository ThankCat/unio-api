package appsettings

import (
	"context"
	"encoding/json"

	messagesadapter "github.com/ThankCat/unio-gateway/internal/core/adapter/anthropic/messages"
)

// Service 是 admin 侧读写全局运行时配置的服务(封装 SettingsStore)。
type Service struct {
	store *SettingsStore
}

// NewService 创建配置服务。
func NewService(store *SettingsStore) *Service {
	return &Service{store: store}
}

// SettingItem 是通用配置列表项:注册元数据 + 当前生效值 + 生效来源。
type SettingItem struct {
	Key         string
	Category    string
	Label       string
	Description string
	HotReload   bool
	Default     json.RawMessage
	Value       json.RawMessage
	Source      string // redis | db | default
}

// List 返回全部已注册配置项(含元数据与本进程当前生效值/来源),供 admin 面板通用渲染。
func (s *Service) List(ctx context.Context) []SettingItem {
	defs := s.store.registry.List()
	out := make([]SettingItem, 0, len(defs))
	for _, d := range defs {
		item := SettingItem{
			Key:         d.Key,
			Category:    d.Category,
			Label:       d.Label,
			Description: d.Description,
			HotReload:   d.HotReload,
			Default:     d.Default,
		}
		if v, ok := s.store.Effective(ctx, d.Key); ok {
			item.Value = v.Value
			item.Source = v.Source
		}
		out = append(out, item)
	}
	return out
}

// SetRaw 按 key 校验并写入原始 JSON 值(通用写入路径)。
func (s *Service) SetRaw(ctx context.Context, key string, value json.RawMessage) error {
	return s.store.Set(ctx, key, value)
}

// GetAnthropicBetaPolicy 读取当前 Anthropic beta 策略(生效值)。
func (s *Service) GetAnthropicBetaPolicy(ctx context.Context) messagesadapter.BetaPolicy {
	return GetAnthropicBetaPolicy(ctx, s.store)
}

// SetAnthropicBetaPolicy 校验并写入 Anthropic beta 策略。
func (s *Service) SetAnthropicBetaPolicy(ctx context.Context, policy messagesadapter.BetaPolicy) error {
	return SetAnthropicBetaPolicy(ctx, s.store, policy)
}
