package channel

import (
	"context"
	"net/http"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi/adminhttp"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	adminchannel "github.com/ThankCat/unio-gateway/internal/service/admin/channel"
)

// BreakerRuntime 暴露 Channel breaker 只读运行态与显式复位（§8.4/§8.5）；由 *breakerstore.Store 实现。
type BreakerRuntime interface {
	Snapshot(ctx context.Context, scope breakerstore.Scope, id int64) (breakerstore.ScopeSnapshot, error)
	Reset(ctx context.Context, scope breakerstore.Scope, id int64) (int64, error)
	ChannelCapacityControl(channelID int64) breakerstore.ControlTarget
	ReadControl(ctx context.Context, target breakerstore.ControlTarget, expectedRevision int64) (breakerstore.ControlSnapshot, error)
}

type ChannelRuntimeService interface {
	Get(ctx context.Context, id int64) (adminchannel.Channel, error)
}

type channelBreakerSnapshotDTO struct {
	Scope               string  `json:"scope"`
	ID                  int64   `json:"id"`
	Exists              bool    `json:"exists"`
	State               string  `json:"state"`
	OpenRemainingMs     int64   `json:"open_remaining_ms"`
	OpenLevel           int     `json:"open_level"`
	EligibleSuccesses   int64   `json:"eligible_successes"`
	EligibleFailures    int64   `json:"eligible_failures"`
	ConsecutiveFailures int64   `json:"consecutive_failures"`
	ErrorRate           float64 `json:"error_rate"`
	SampleCount         int64   `json:"sample_count"`
}

type channelRuntimeDTO struct {
	ID                             int64                      `json:"id"`
	ProviderID                     int64                      `json:"provider_id"`
	OriginRevision                 int64                      `json:"origin_revision"`
	ProviderStatusRevision         int64                      `json:"provider_status_revision"`
	ConfigRevision                 int64                      `json:"config_revision"`
	CapacityRevision               int64                      `json:"capacity_revision"`
	RuntimeSyncState               string                     `json:"runtime_sync_state"`
	RuntimeProviderID              *int64                     `json:"runtime_provider_id"`
	RuntimeOriginRevision          *int64                     `json:"runtime_origin_revision"`
	RuntimeProviderStatusRevision  *int64                     `json:"runtime_provider_status_revision"`
	RuntimeConfigRevision          *int64                     `json:"runtime_config_revision"`
	RuntimeCapacityActiveRevision  *int64                     `json:"runtime_capacity_active_revision"`
	RuntimeCapacityPendingRevision *int64                     `json:"runtime_capacity_pending_revision"`
	CapacityPayloadMatches         bool                       `json:"capacity_payload_matches"`
	Breaker                        *channelBreakerSnapshotDTO `json:"breaker"`
}

func toChannelBreakerDTO(s breakerstore.ScopeSnapshot) channelBreakerSnapshotDTO {
	return channelBreakerSnapshotDTO{
		Scope: string(s.Scope), ID: s.ID, Exists: s.Exists, State: string(s.State),
		OpenRemainingMs: s.OpenRemainingMs, OpenLevel: s.OpenLevel,
		EligibleSuccesses: s.EligibleSuccesses, EligibleFailures: s.EligibleFailures,
		ConsecutiveFailures: s.ConsecutiveFailures, ErrorRate: s.ErrorRate, SampleCount: s.SampleCount,
	}
}

type channelBreakerHandler struct {
	service ChannelRuntimeService
	breaker BreakerRuntime
}

func (h *channelBreakerHandler) runtime(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	dto, err := h.loadRuntime(r.Context(), id)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, dto)
}

func (h *channelBreakerHandler) reset(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	if h.breaker == nil || h.service == nil {
		adminhttp.WriteServiceError(w, failure.New(failure.CodeGatewayBreakerStoreUnavailable, failure.WithMessage("breaker runtime data source unavailable")))
		return
	}
	if _, err := h.service.Get(r.Context(), id); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	if _, err := h.breaker.Reset(r.Context(), breakerstore.ScopeChannel, id); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	dto, err := h.loadRuntime(r.Context(), id)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, dto)
}

func (h *channelBreakerHandler) loadRuntime(ctx context.Context, id int64) (channelRuntimeDTO, error) {
	if h.breaker == nil || h.service == nil {
		return channelRuntimeDTO{}, failure.New(failure.CodeGatewayBreakerStoreUnavailable, failure.WithMessage("breaker runtime data source unavailable"))
	}
	ch, err := h.service.Get(ctx, id)
	if err != nil {
		return channelRuntimeDTO{}, err
	}
	snapshot, err := h.breaker.Snapshot(ctx, breakerstore.ScopeChannel, id)
	if err != nil {
		return channelRuntimeDTO{}, err
	}
	control, err := h.breaker.ReadControl(ctx, h.breaker.ChannelCapacityControl(id), ch.CapacityRevision)
	if err != nil {
		return channelRuntimeDTO{}, err
	}
	payload, err := adminchannel.CanonicalCapacityPayload(adminchannel.ChannelCapacity{Concurrency: ch.ConcurrencyLimit})
	if err != nil {
		return channelRuntimeDTO{}, err
	}

	dto := channelRuntimeDTO{
		ID:                             ch.ID,
		ProviderID:                     ch.ProviderID,
		OriginRevision:                 ch.OriginRevision,
		ProviderStatusRevision:         ch.ProviderStatusRevision,
		ConfigRevision:                 ch.ConfigRevision,
		CapacityRevision:               ch.CapacityRevision,
		RuntimeProviderID:              positiveRuntimeInt64(snapshot.ProviderID),
		RuntimeOriginRevision:          positiveRuntimeInt64(snapshot.OriginRevision),
		RuntimeProviderStatusRevision:  positiveRuntimeInt64(snapshot.StatusRevision),
		RuntimeConfigRevision:          positiveRuntimeInt64(snapshot.ChannelConfigRevision),
		RuntimeCapacityActiveRevision:  positiveRuntimeInt64(control.ActiveRevision),
		RuntimeCapacityPendingRevision: positiveRuntimeInt64(control.PendingRevision),
		CapacityPayloadMatches:         control.ActivePayload == payload,
	}
	dto.RuntimeSyncState = classifyChannelRuntimeSync(ch, snapshot, control, dto.CapacityPayloadMatches)
	if dto.RuntimeSyncState == "active" {
		breakerDTO := toChannelBreakerDTO(snapshot)
		dto.Breaker = &breakerDTO
	}
	return dto, nil
}

func classifyChannelRuntimeSync(ch adminchannel.Channel, snapshot breakerstore.ScopeSnapshot, control breakerstore.ControlSnapshot, payloadMatches bool) string {
	switch control.SyncState {
	case "absent":
		return "runtime_sync_required"
	case "pending":
		return "runtime_sync_pending"
	case "active":
		if !payloadMatches {
			return "stale"
		}
	default:
		return "stale"
	}
	if !snapshot.Exists {
		return "active"
	}
	if snapshot.ProviderID != ch.ProviderID ||
		snapshot.OriginRevision != ch.OriginRevision ||
		snapshot.StatusRevision != ch.ProviderStatusRevision ||
		snapshot.ChannelConfigRevision != ch.ConfigRevision {
		return "stale"
	}
	return "active"
}

func positiveRuntimeInt64(value int64) *int64 {
	if value <= 0 {
		return nil
	}
	return &value
}
