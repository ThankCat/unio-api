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
	// SupplyForm 是渠道供给形态；pool 型的供给单元是账号，凭据判定换成「有无可调度账号」。
	SupplyForm string
	// HasSchedulableAccount 表示池内至少有一个 enabled 账号（credential 型恒 false，不参与判定）。
	HasSchedulableAccount bool
	HasBaseURL            bool
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
	// 池型渠道不持凭据：凭据在账号上，判定换成「池内有无可调度账号」（池空 ≠ 缺凭据 ≠ 熔断）。
	case facts.SupplyForm == "pool" && !facts.HasSchedulableAccount:
		return "account_pool_empty"
	case facts.SupplyForm != "pool" && !facts.HasCredential:
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
