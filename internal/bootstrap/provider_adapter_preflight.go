package bootstrap

import (
	"context"
	"errors"

	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
)

var (
	// ErrProviderAdapterCapabilityMissing 表示启用 provider 配置的 adapter 缺少当前进程要求的能力。
	ErrProviderAdapterCapabilityMissing = errors.New("provider adapter capability missing")
)

// ProviderAdapterStore 定义 adapter preflight 需要的最小存储能力。
//
// Phase 10 起 adapter 运行时绑定下沉到 channel，preflight 按启用 channel 的
// (protocol, adapter_key) 校验，不再读取 providers.adapter。
type ProviderAdapterStore interface {
	ListEnabledChannelAdapters(ctx context.Context) ([]sqlc.ListEnabledChannelAdaptersRow, error)
}

// AdapterCapabilityRegistry 定义 provider adapter preflight 需要查询的 adapter 能力注册表。
type AdapterCapabilityRegistry interface {
	HasAny(protocol string, adapterKey string) bool
}

// ProviderAdapterPreflight 校验启用 provider 的 adapter 配置是否能被当前进程支持。
type ProviderAdapterPreflight struct {
	store    ProviderAdapterStore
	registry AdapterCapabilityRegistry
}

// NewProviderAdapterPreflight 创建 provider adapter 启动前校验器。
func NewProviderAdapterPreflight(store ProviderAdapterStore, registry AdapterCapabilityRegistry) *ProviderAdapterPreflight {
	return &ProviderAdapterPreflight{
		store:    store,
		registry: registry,
	}
}

// ValidateEnabledChannelBindings 校验所有启用 channel 的复合键都存在代码注册。
//
// 具体 operation 所需的 non-stream、stream 与 input tokenizer 能力由 lifecycle 在
// SQL routing 后继续过滤；preflight 不强制每个 channel 实现全部能力。
func (p *ProviderAdapterPreflight) ValidateEnabledChannelBindings(ctx context.Context) error {
	rows, err := p.store.ListEnabledChannelAdapters(ctx)
	if err != nil {
		return failure.Wrap(
			failure.CodeBootstrapStoreFailed,
			err,
			failure.WithMessage("failed to list enabled channel adapters"),
		)
	}

	for _, row := range rows {
		if !p.registry.HasAny(row.Protocol, row.AdapterKey) {
			return capabilityMissingError(row)
		}
	}

	return nil
}

func capabilityMissingError(row sqlc.ListEnabledChannelAdaptersRow) error {
	return failure.Wrap(
		failure.CodeBootstrapProviderAdapterCapabilityMissing,
		ErrProviderAdapterCapabilityMissing,
		failure.WithMessage("channel adapter capability missing"),
		failure.WithField("channel_id", row.ChannelID),
		failure.WithField("provider_slug", row.ProviderSlug),
		failure.WithField("protocol", row.Protocol),
		failure.WithField("adapter_key", row.AdapterKey),
		failure.WithField("capability", "binding"),
	)
}
