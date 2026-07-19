package route

import (
	"context"
	"net/http"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi/adminhttp"
	"github.com/ThankCat/unio-gateway/internal/service/admin/gatewayruntime"
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
	ChannelID            int64                           `json:"channel_id"`
	ChannelName          string                          `json:"channel_name"`
	ChannelStatus        string                          `json:"channel_status"`
	ProviderID           int64                           `json:"provider_id"`
	ProviderName         string                          `json:"provider_name"`
	ProviderStatus       string                          `json:"provider_status"`
	Protocol             string                          `json:"protocol"`
	AdapterKey           string                          `json:"adapter_key"`
	Priority             int32                           `json:"priority"`
	Eligible             bool                            `json:"eligible"`
	ExcludedReason       string                          `json:"excluded_reason,omitempty"`
	ConcurrencyUsed      int64                           `json:"concurrency_used"`
	ConcurrencyLimit     int64                           `json:"concurrency_limit"`
	ConcurrencyRemaining *float64                        `json:"concurrency_remaining"`
	TPMUsed              int64                           `json:"tpm_used"`
	TPMLimit             int64                           `json:"tpm_limit"`
	TPMRemaining         *float64                        `json:"tpm_remaining"`
	CapacityScore        float64                         `json:"capacity_score"`
	HealthFactor         float64                         `json:"health_factor"`
	FinalWeight          float64                         `json:"final_weight"`
	Pressure             float64                         `json:"pressure"`
	CapacityUnknown      bool                            `json:"capacity_unknown"`
	CapacityReadFailed   bool                            `json:"capacity_read_failed"`
	BreakerState         string                          `json:"breaker_state"`
	ErrorRate            float64                         `json:"error_rate"`
	LatencyEWMAMs        float64                         `json:"latency_ewma_ms"`
	CurrentOrder         int                             `json:"current_order"`
	Selected1m           int64                           `json:"selected_1m"`
	Selected5m           int64                           `json:"selected_5m"`
	SelectedShare1m      float64                         `json:"selected_share_1m"`
	SelectedShare5m      float64                         `json:"selected_share_5m"`
	Fallback1m           int64                           `json:"fallback_1m"`
	MarginStatus         string                          `json:"margin_status"`
	InstanceSnapshots    []gatewayruntime.InstanceStatus `json:"instance_snapshots"`
}

type routeRuntimeDTO struct {
	RouteID          int64                         `json:"route_id"`
	Mode             string                        `json:"mode"`
	RouteStatus      string                        `json:"route_status"`
	ModelID          string                        `json:"model_id,omitempty"`
	Protocol         string                        `json:"protocol,omitempty"`
	ObservedAt       string                        `json:"observed_at"`
	Stale            bool                          `json:"stale"`
	PoolSize         int                           `json:"pool_size"`
	CandidateCount   int                           `json:"candidate_count"`
	NoRedundancy     bool                          `json:"no_redundancy"`
	AllCapacityZero  bool                          `json:"all_capacity_zero"`
	CapacityDegraded bool                          `json:"capacity_degraded"`
	Sources          []runtimeSourceDTO            `json:"sources"`
	GatewaySources   []gatewayruntime.SourceStatus `json:"gateway_sources"`
	Channels         []runtimeChannelDTO           `json:"channels"`
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
		AllCapacityZero: value.AllCapacityZero, CapacityDegraded: value.CapacityDegraded,
		Sources: make([]runtimeSourceDTO, 0, len(value.Sources)), GatewaySources: value.GatewaySources,
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
			Protocol: channel.Protocol, AdapterKey: channel.AdapterKey, Priority: channel.Priority,
			Eligible: channel.Eligible, ExcludedReason: channel.ExcludedReason,
			ConcurrencyUsed: channel.ConcurrencyUsed, ConcurrencyLimit: channel.ConcurrencyLimit, ConcurrencyRemaining: channel.ConcurrencyRemaining,
			TPMUsed: channel.TPMUsed, TPMLimit: channel.TPMLimit, TPMRemaining: channel.TPMRemaining,
			CapacityScore: channel.CapacityScore, HealthFactor: channel.HealthFactor, FinalWeight: channel.FinalWeight,
			Pressure: channel.Pressure, CapacityUnknown: channel.CapacityUnknown, CapacityReadFailed: channel.CapacityReadFailed,
			BreakerState: channel.BreakerState, ErrorRate: channel.ErrorRate, LatencyEWMAMs: channel.LatencyEWMAMs,
			CurrentOrder: channel.CurrentOrder, Selected1m: channel.Selected1m, Selected5m: channel.Selected5m,
			SelectedShare1m: channel.SelectedShare1m, SelectedShare5m: channel.SelectedShare5m,
			Fallback1m: channel.Fallback1m, MarginStatus: channel.MarginStatus, InstanceSnapshots: channel.InstanceSnapshots,
		})
	}
	return out
}
