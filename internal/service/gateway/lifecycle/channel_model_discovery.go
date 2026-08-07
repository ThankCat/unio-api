package lifecycle

import (
	"context"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/core/channel"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

// ListChannelModels 用 (protocol, adapter_key) 注册的枚举能力读取真实上游模型列表。
func (r *AdapterRegistry) ListChannelModels(
	ctx context.Context,
	protocol, adapterKey string,
	runtime channel.Runtime,
) (adapter.ModelListResult, error) {
	if r == nil {
		return adapter.ModelListResult{}, modelDiscoveryUnsupported(protocol, adapterKey)
	}

	var lister adapter.ModelLister
	var ok bool
	switch protocol {
	case routing.ProtocolOpenAI:
		if r.OpenAI != nil {
			lister, ok = r.OpenAI.Models(adapterKey)
		}
	case routing.ProtocolAnthropic:
		if r.Anthropic != nil {
			lister, ok = r.Anthropic.Models(adapterKey)
		}
	}
	if !ok || lister == nil {
		return adapter.ModelListResult{}, modelDiscoveryUnsupported(protocol, adapterKey)
	}
	return lister.ListModels(ctx, runtime)
}

func modelDiscoveryUnsupported(protocol, adapterKey string) error {
	return failure.New(
		failure.CodeAdapterInvalidRegistration,
		failure.WithMessage("channel (protocol, adapter_key) is not registered for model discovery"),
		failure.WithField("protocol", protocol),
		failure.WithField("adapter_key", adapterKey),
	)
}
