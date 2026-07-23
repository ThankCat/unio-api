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
	ProviderEndpointID              int64    `json:"provider_endpoint_id"`
	ProviderEndpointName            string   `json:"provider_endpoint_name"`
	ProviderEndpointStatus          string   `json:"provider_endpoint_status"`
	EndpointBaseURLRevision         int64    `json:"endpoint_base_url_revision"`
	EndpointStatusRevision          int64    `json:"endpoint_status_revision"`
	RuntimeEndpointBaseURLRevision  int64    `json:"runtime_endpoint_base_url_revision"`
	RuntimeEndpointStatusRevision   int64    `json:"runtime_endpoint_status_revision"`
	PendingEndpointBaseURLRevision  *int64   `json:"pending_endpoint_base_url_revision"`
	PendingEndpointStatusRevision   *int64   `json:"pending_endpoint_status_revision"`
	EndpointBaseURLRevisionCurrent  bool     `json:"endpoint_base_url_revision_current"`
	EndpointStatusRevisionCurrent   bool     `json:"endpoint_status_revision_current"`
	EndpointStateGeneration         int64    `json:"endpoint_state_generation"`
	EndpointBaseURLFenceGeneration  int64    `json:"endpoint_base_url_fence_generation"`
	EndpointStatusFenceGeneration   int64    `json:"endpoint_status_fence_generation"`
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
	CostRatio                       *float64 `json:"cost_ratio"`
	CostWeight                      float64  `json:"cost_weight"`
	CostFactor                      float64  `json:"cost_factor"`
	FinalWeight                     float64  `json:"final_weight"`
	Pressure                        float64  `json:"pressure"`
	CapacityUnknown                 bool     `json:"capacity_unknown"`
	CapacityReadFailed              bool     `json:"capacity_read_failed"`
	EndpointBreakerState            *string  `json:"endpoint_breaker_state"`
	EndpointOpenRemainingMs         *int64   `json:"endpoint_open_remaining_ms"`
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
	Sources               []runtimeSourceDTO  `json:"sources"`
	Channels              []runtimeChannelDTO `json:"channels"`
}

func (h *runtimeHandler) get(w http.ResponseWriter, r *http.Request) {
	routeID, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	runtime, err := h.service.Get(r.Context(), routeruntime.Params{
		RouteID: routeID, ModelID: adminhttp.QueryString(r, "model_id"), Protocol: adminhttp.QueryString(r, "protocol"),
	})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
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
			ProviderEndpointID: channel.ProviderEndpointID, ProviderEndpointName: channel.ProviderEndpointName,
			ProviderEndpointStatus:          channel.ProviderEndpointStatus,
			EndpointBaseURLRevision:         channel.EndpointBaseURLRevision,
			EndpointStatusRevision:          channel.EndpointStatusRevision,
			RuntimeEndpointBaseURLRevision:  channel.RuntimeEndpointBaseURLRevision,
			RuntimeEndpointStatusRevision:   channel.RuntimeEndpointStatusRevision,
			PendingEndpointBaseURLRevision:  channel.PendingEndpointBaseURLRevision,
			PendingEndpointStatusRevision:   channel.PendingEndpointStatusRevision,
			EndpointBaseURLRevisionCurrent:  channel.EndpointBaseURLRevisionCurrent,
			EndpointStatusRevisionCurrent:   channel.EndpointStatusRevisionCurrent,
			EndpointStateGeneration:         channel.EndpointStateGeneration,
			EndpointBaseURLFenceGeneration:  channel.EndpointBaseURLFenceGeneration,
			EndpointStatusFenceGeneration:   channel.EndpointStatusFenceGeneration,
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
			CostWeight: channel.CostWeight, CostFactor: channel.CostFactor, FinalWeight: channel.FinalWeight,
			Pressure: channel.Pressure, CapacityUnknown: channel.CapacityUnknown, CapacityReadFailed: channel.CapacityReadFailed,
			EndpointBreakerState: channel.EndpointBreakerState, EndpointOpenRemainingMs: channel.EndpointOpenRemainingMs,
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
