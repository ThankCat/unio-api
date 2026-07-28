package route

import (
	"context"
	"net/http"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi/adminhttp"
	"github.com/ThankCat/unio-gateway/internal/service/admin/routeruntime"
)

type RuntimeService interface {
	Get(context.Context, routeruntime.Params) (routeruntime.Runtime, error)
}

type runtimeHandler struct {
	service RuntimeService
}

type runtimeSourceDTO struct {
	Name       string  `json:"name"`
	Available  bool    `json:"available"`
	ObservedAt *string `json:"observed_at"`
	Stale      bool    `json:"stale"`
}

type runtimeChannelDTO struct {
	ChannelID                       int64    `json:"channel_id"`
	ChannelName                     string   `json:"channel_name"`
	ChannelStatus                   string   `json:"channel_status"`
	ProviderID                      int64    `json:"provider_id"`
	ProviderName                    string   `json:"provider_name"`
	ProviderStatus                  string   `json:"provider_status"`
	OriginRevision                  int64    `json:"origin_revision"`
	ProviderStatusRevision          int64    `json:"provider_status_revision"`
	RuntimeOriginRevision           int64    `json:"runtime_origin_revision"`
	RuntimeProviderStatusRevision   int64    `json:"runtime_provider_status_revision"`
	PendingOriginRevision           *int64   `json:"pending_origin_revision"`
	PendingProviderStatusRevision   *int64   `json:"pending_provider_status_revision"`
	OriginRevisionCurrent           bool     `json:"origin_revision_current"`
	ProviderStatusRevisionCurrent   bool     `json:"provider_status_revision_current"`
	ProviderStateGeneration         int64    `json:"provider_state_generation"`
	OriginFenceGeneration           int64    `json:"origin_fence_generation"`
	StatusFenceGeneration           int64    `json:"status_fence_generation"`
	ChannelConfigRevision           int64    `json:"channel_config_revision"`
	RuntimeChannelConfigRevision    *int64   `json:"runtime_channel_config_revision"`
	ChannelConfigRevisionCurrent    bool     `json:"channel_config_revision_current"`
	ChannelAdmissionLimitsRevision  int64    `json:"channel_admission_limits_revision"`
	RuntimeChannelAdmissionRevision int64    `json:"runtime_channel_admission_limits_revision"`
	ChannelAdmissionRevisionCurrent bool     `json:"channel_admission_limits_revision_current"`
	RouteRateLimitsRevision         int64    `json:"route_rate_limits_revision"`
	ChannelRateLimitsRevision       int64    `json:"channel_rate_limits_revision"`
	GlobalConcurrencyRevision       int64    `json:"global_concurrency_revision"`
	CircuitBreakerRevision          int64    `json:"circuit_breaker_revision"`
	RoutingBalanceRevision          int64    `json:"routing_balance_revision"`
	RuntimeControlState             string   `json:"runtime_control_state"`
	RuntimeRevisionCurrent          bool     `json:"runtime_revision_current"`
	Protocol                        string   `json:"protocol"`
	AdapterKey                      string   `json:"adapter_key"`
	Priority                        int32    `json:"priority"`
	Eligible                        bool     `json:"eligible"`
	ExcludedReason                  string   `json:"excluded_reason,omitempty"`
	ConcurrencyUsed                 int64    `json:"concurrency_used"`
	ConcurrencyLimit                int64    `json:"concurrency_limit"`
	ConcurrencyRemaining            *float64 `json:"concurrency_remaining"`
	RPMUsed                         int64    `json:"rpm_used"`
	RPMLimit                        int64    `json:"rpm_limit"`
	RPMRemaining                    *float64 `json:"rpm_remaining"`
	RPDUsed                         int64    `json:"rpd_used"`
	RPDLimit                        int64    `json:"rpd_limit"`
	RPDRemaining                    *float64 `json:"rpd_remaining"`
	TPMUsed                         int64    `json:"tpm_used"`
	TPMLimit                        int64    `json:"tpm_limit"`
	TPMRemaining                    *float64 `json:"tpm_remaining"`
	CapacityScore                   float64  `json:"capacity_score"`
	AlgorithmVersion                string   `json:"algorithm_version"`
	EconomicScore                   float64  `json:"economic_score"`
	HealthScore                     float64  `json:"health_score"`
	PriorityScore                   float64  `json:"priority_score"`
	FinalScore                      float64  `json:"final_score"`
	EconomicWeightPct               int      `json:"economic_weight_pct"`
	HealthWeightPct                 int      `json:"health_weight_pct"`
	CapacityWeightPct               int      `json:"capacity_weight_pct"`
	PriorityWeightPct               int      `json:"priority_weight_pct"`
	CostRatio                       *float64 `json:"cost_ratio"`
	CostWeight                      float64  `json:"cost_weight"`
	CostFactor                      float64  `json:"cost_factor"`
	FinalWeight                     float64  `json:"final_weight"`
	Pressure                        float64  `json:"pressure"`
	CapacityUnknown                 bool     `json:"capacity_unknown"`
	CapacityReadFailed              bool     `json:"capacity_read_failed"`
	ProviderBreakerState            *string  `json:"provider_breaker_state"`
	ProviderOpenRemainingMs         *int64   `json:"provider_open_remaining_ms"`
	ChannelBreakerState             *string  `json:"channel_breaker_state"`
	ChannelOpenRemainingMs          *int64   `json:"channel_open_remaining_ms"`
	ErrorRate                       *float64 `json:"error_rate"`
	ErrorSamples                    int64    `json:"error_samples"`
	TTFTEWMAMs                      *float64 `json:"ttft_ewma_ms"`
	TTFTSamples                     int64    `json:"ttft_samples"`
	TTFTSampleSource                string   `json:"ttft_sample_source"`
	CooldownRemainingMs             int64    `json:"cooldown_remaining_ms"`
	ModelPermissionPaused           bool     `json:"model_permission_paused"`
	ModelPermissionRecheckState     string   `json:"model_permission_recheck_state"`
	RuntimeSyncState                string   `json:"runtime_sync_state"`
	BreakerStoreAdmission           string   `json:"breaker_store_admission"`
	CurrentOrder                    int      `json:"current_order"`
	Selected1m                      int64    `json:"selected_1m"`
	Selected5m                      int64    `json:"selected_5m"`
	SelectedShare1m                 float64  `json:"selected_share_1m"`
	SelectedShare5m                 float64  `json:"selected_share_5m"`
	Fallback1m                      int64    `json:"fallback_1m"`
	MarginStatus                    string   `json:"margin_status"`
}

type routeUsageDTO struct {
	Concurrency int64 `json:"concurrency"`
	RPM         int64 `json:"rpm"`
	RPD         int64 `json:"rpd"`
	TPM         int64 `json:"tpm"`
	ActiveUsers int64 `json:"active_users"`
}

type routeRuntimeDTO struct {
	RouteID               int64               `json:"route_id"`
	Mode                  string              `json:"mode"`
	RouteStatus           string              `json:"route_status"`
	ModelID               string              `json:"model_id,omitempty"`
	Protocol              string              `json:"protocol,omitempty"`
	ObservedAt            string              `json:"observed_at"`
	Stale                 bool                `json:"stale"`
	PoolSize              int                 `json:"pool_size"`
	CandidateCount        int                 `json:"candidate_count"`
	NoRedundancy          bool                `json:"no_redundancy"`
	AllCapacityZero       bool                `json:"all_capacity_zero"`
	RuntimeSyncState      string              `json:"runtime_sync_state"`
	BreakerStoreAdmission string              `json:"breaker_store_admission"`
	RouteUsage            *routeUsageDTO      `json:"route_usage"`
	Sources               []runtimeSourceDTO  `json:"sources"`
	Channels              []runtimeChannelDTO `json:"channels"`
}

func (h *runtimeHandler) get(w http.ResponseWriter, r *http.Request) {
	routeID, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	sort, err := adminhttp.ParseListSort(r, map[string]struct{}{
		"order":       {},
		"weight":      {},
		"capacity":    {}, // 兼容：最紧余量
		"concurrency": {},
		"rpm":         {},
		"rpd":         {},
		"tpm":         {},
	}, "order", false)
	if err != nil {
		adminhttp.WriteSortError(w, err)
		return
	}
	runtime, err := h.service.Get(r.Context(), routeruntime.Params{
		RouteID: routeID, ModelID: adminhttp.QueryString(r, "model_id"), Protocol: adminhttp.QueryString(r, "protocol"),
	})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	field, desc := sort.SQLParams()
	routeruntime.SortChannels(runtime.Channels, field, desc)
	adminhttp.WriteData(w, http.StatusOK, toRouteRuntimeDTO(runtime))
}

func toRouteRuntimeDTO(value routeruntime.Runtime) routeRuntimeDTO {
	out := routeRuntimeDTO{
		RouteID: value.RouteID, Mode: value.Mode, RouteStatus: value.RouteStatus,
		ModelID: value.ModelID, Protocol: value.Protocol, ObservedAt: adminhttp.RFC3339(value.ObservedAt), Stale: value.Stale,
		PoolSize: value.PoolSize, CandidateCount: value.CandidateCount, NoRedundancy: value.NoRedundancy,
		AllCapacityZero:  value.AllCapacityZero,
		RuntimeSyncState: value.RuntimeSyncState, BreakerStoreAdmission: value.BreakerStoreAdmission,
		Sources:  make([]runtimeSourceDTO, 0, len(value.Sources)),
		Channels: make([]runtimeChannelDTO, 0, len(value.Channels)),
	}
	if value.RouteUsage != nil {
		out.RouteUsage = &routeUsageDTO{
			Concurrency: value.RouteUsage.Concurrency,
			RPM:         value.RouteUsage.RPM,
			RPD:         value.RouteUsage.RPD,
			TPM:         value.RouteUsage.TPM,
			ActiveUsers: value.RouteUsage.ActiveUsers,
		}
	}
	for _, source := range value.Sources {
		var observedAt *string
		if !source.ObservedAt.IsZero() {
			formatted := adminhttp.RFC3339(source.ObservedAt)
			observedAt = &formatted
		}
		out.Sources = append(out.Sources, runtimeSourceDTO{Name: source.Name, Available: source.Available, ObservedAt: observedAt, Stale: source.Stale})
	}
	for _, channel := range value.Channels {
		out.Channels = append(out.Channels, runtimeChannelDTO{
			ChannelID: channel.ChannelID, ChannelName: channel.ChannelName, ChannelStatus: channel.ChannelStatus,
			ProviderID: channel.ProviderID, ProviderName: channel.ProviderName, ProviderStatus: channel.ProviderStatus,
			OriginRevision:                  channel.OriginRevision,
			ProviderStatusRevision:          channel.ProviderStatusRevision,
			RuntimeOriginRevision:           channel.RuntimeOriginRevision,
			RuntimeProviderStatusRevision:   channel.RuntimeProviderStatusRevision,
			PendingOriginRevision:           channel.PendingOriginRevision,
			PendingProviderStatusRevision:   channel.PendingProviderStatusRevision,
			OriginRevisionCurrent:           channel.OriginRevisionCurrent,
			ProviderStatusRevisionCurrent:   channel.ProviderStatusRevisionCurrent,
			ProviderStateGeneration:         channel.ProviderStateGeneration,
			OriginFenceGeneration:           channel.OriginFenceGeneration,
			StatusFenceGeneration:           channel.StatusFenceGeneration,
			ChannelConfigRevision:           channel.ChannelConfigRevision,
			RuntimeChannelConfigRevision:    channel.RuntimeChannelConfigRevision,
			ChannelConfigRevisionCurrent:    channel.ChannelConfigRevisionCurrent,
			ChannelAdmissionLimitsRevision:  channel.ChannelAdmissionLimitsRevision,
			RuntimeChannelAdmissionRevision: channel.RuntimeChannelAdmissionRevision,
			ChannelAdmissionRevisionCurrent: channel.ChannelAdmissionRevisionCurrent,
			RouteRateLimitsRevision:         channel.RouteRateLimitsRevision,
			ChannelRateLimitsRevision:       channel.ChannelRateLimitsRevision,
			GlobalConcurrencyRevision:       channel.GlobalConcurrencyRevision,
			CircuitBreakerRevision:          channel.CircuitBreakerRevision,
			RoutingBalanceRevision:          channel.RoutingBalanceRevision,
			RuntimeControlState:             channel.RuntimeControlState,
			RuntimeRevisionCurrent:          channel.RuntimeRevisionCurrent,
			Protocol:                        channel.Protocol, AdapterKey: channel.AdapterKey, Priority: channel.Priority,
			Eligible: channel.Eligible, ExcludedReason: channel.ExcludedReason,
			ConcurrencyUsed: channel.ConcurrencyUsed, ConcurrencyLimit: channel.ConcurrencyLimit, ConcurrencyRemaining: channel.ConcurrencyRemaining,
			RPMUsed: channel.RPMUsed, RPMLimit: channel.RPMLimit, RPMRemaining: channel.RPMRemaining,
			RPDUsed: channel.RPDUsed, RPDLimit: channel.RPDLimit, RPDRemaining: channel.RPDRemaining,
			TPMUsed: channel.TPMUsed, TPMLimit: channel.TPMLimit, TPMRemaining: channel.TPMRemaining,
			CapacityScore: channel.CapacityScore, CostRatio: channel.CostRatio,
			AlgorithmVersion: channel.AlgorithmVersion,
			EconomicScore:    channel.EconomicScore, HealthScore: channel.HealthScore,
			PriorityScore: channel.PriorityScore, FinalScore: channel.FinalScore,
			EconomicWeightPct: channel.EconomicWeightPct, HealthWeightPct: channel.HealthWeightPct,
			CapacityWeightPct: channel.CapacityWeightPct, PriorityWeightPct: channel.PriorityWeightPct,
			CostWeight: channel.CostWeight, CostFactor: channel.CostFactor, FinalWeight: channel.FinalWeight,
			Pressure: channel.Pressure, CapacityUnknown: channel.CapacityUnknown, CapacityReadFailed: channel.CapacityReadFailed,
			ProviderBreakerState: channel.ProviderBreakerState, ProviderOpenRemainingMs: channel.ProviderOpenRemainingMs,
			ChannelBreakerState: channel.ChannelBreakerState, ChannelOpenRemainingMs: channel.ChannelOpenRemainingMs,
			ErrorRate: channel.ErrorRate, ErrorSamples: channel.ErrorSamples,
			TTFTEWMAMs: channel.TTFTEWMAMs, TTFTSamples: channel.TTFTSamples, TTFTSampleSource: channel.TTFTSampleSource,
			CooldownRemainingMs:         channel.CooldownRemainingMs,
			ModelPermissionPaused:       channel.ModelPermissionPaused,
			ModelPermissionRecheckState: channel.ModelPermissionRecheckState,
			RuntimeSyncState:            channel.RuntimeSyncState, BreakerStoreAdmission: channel.BreakerStoreAdmission,
			CurrentOrder: channel.CurrentOrder, Selected1m: channel.Selected1m, Selected5m: channel.Selected5m,
			SelectedShare1m: channel.SelectedShare1m, SelectedShare5m: channel.SelectedShare5m,
			Fallback1m: channel.Fallback1m, MarginStatus: channel.MarginStatus,
		})
	}
	return out
}
