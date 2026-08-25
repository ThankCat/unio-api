// Package routingdiagnostic defines stable, sensitive-data-free route exclusion reasons.
package routingdiagnostic

import "slices"

type Filter struct {
	ModelID  string
	Protocol string
}

type PoolFacts struct {
	ChannelStatus   string
	ProviderStatus  string
	CredentialValid bool
	HasCredential   bool
	HasBaseURL      bool
	// Protocols 是渠道声明支持的入口协议集合；一条渠道可以同时接多个协议。
	Protocols      []string
	ModelExists    bool
	ModelStatus    string
	BindingStatus  string
	HasModelPrice  bool
	HasChannelCost bool
}

func ExcludedReason(facts PoolFacts, filter Filter) string {
	switch {
	case facts.ChannelStatus != "enabled":
		return "channel_" + facts.ChannelStatus
	case facts.ProviderStatus != "enabled":
		return "provider_" + facts.ProviderStatus
	case !facts.CredentialValid:
		return "credential_invalid"
	case !facts.HasCredential:
		return "credential_missing"
	case !facts.HasBaseURL:
		return "base_url_missing"
	case filter.Protocol != "" && !slices.Contains(facts.Protocols, filter.Protocol):
		return "protocol_mismatch"
	case filter.ModelID != "" && !facts.ModelExists:
		return "model_not_found"
	case filter.ModelID != "" && facts.ModelStatus != "enabled":
		return "model_" + facts.ModelStatus
	case filter.ModelID != "" && facts.BindingStatus == "":
		return "model_not_bound"
	case filter.ModelID != "" && facts.BindingStatus != "enabled":
		return "binding_" + facts.BindingStatus
	case filter.ModelID != "" && !facts.HasModelPrice:
		return "model_price_missing"
	case filter.ModelID != "" && !facts.HasChannelCost:
		return "channel_cost_missing"
	default:
		return ""
	}
}
