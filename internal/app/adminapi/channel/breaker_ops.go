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
	ChannelAdmissionControl(channelID int64) breakerstore.ControlTarget
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
	TTFTEWMAMs          float64 `json:"ttft_ewma_ms"`
	TTFTSamples         int64   `json:"ttft_samples"`
	TTFTSampleSource    string  `json:"ttft_sample_source"`
}

type channelRuntimeDTO struct {
	ID                              int64                      `json:"id"`
	ProviderEndpointID              int64                      `json:"provider_endpoint_id"`
	EndpointBaseURLRevision         int64                      `json:"endpoint_base_url_revision"`
	EndpointStatusRevision          int64                      `json:"endpoint_status_revision"`
	ConfigRevision                  int64                      `json:"config_revision"`
	AdmissionLimitsRevision         int64                      `json:"admission_limits_revision"`
	RuntimeSyncState                string                     `json:"runtime_sync_state"`
	RuntimeProviderEndpointID       *int64                     `json:"runtime_provider_endpoint_id"`
	RuntimeEndpointBaseURLRevision  *int64                     `json:"runtime_endpoint_base_url_revision"`
	RuntimeEndpointStatusRevision   *int64                     `json:"runtime_endpoint_status_revision"`
	RuntimeConfigRevision           *int64                     `json:"runtime_config_revision"`
	RuntimeAdmissionActiveRevision  *int64                     `json:"runtime_admission_active_revision"`
	RuntimeAdmissionPendingRevision *int64                     `json:"runtime_admission_pending_revision"`
	AdmissionPayloadMatches         bool                       `json:"admission_payload_matches"`
	Breaker                         *channelBreakerSnapshotDTO `json:"breaker"`
}

func toChannelBreakerDTO(s breakerstore.ScopeSnapshot) channelBreakerSnapshotDTO {
	return channelBreakerSnapshotDTO{
		Scope: string(s.Scope), ID: s.ID, Exists: s.Exists, State: string(s.State),
		OpenRemainingMs: s.OpenRemainingMs, OpenLevel: s.OpenLevel,
		EligibleSuccesses: s.EligibleSuccesses, EligibleFailures: s.EligibleFailures,
		ConsecutiveFailures: s.ConsecutiveFailures, ErrorRate: s.ErrorRate, SampleCount: s.SampleCount,
		TTFTEWMAMs: s.TTFTEWMAMs, TTFTSamples: s.TTFTSamples, TTFTSampleSource: "stream_only",
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
	control, err := h.breaker.ReadControl(ctx, h.breaker.ChannelAdmissionControl(id), ch.AdmissionLimitsRevision)
	if err != nil {
		return channelRuntimeDTO{}, err
	}
	payload, err := adminchannel.CanonicalAdmissionLimitsPayload(adminchannel.AdmissionLimits{
		RPM: ch.RPMLimit, RPD: ch.RPDLimit, TPM: ch.TPMLimit, Concurrency: ch.ConcurrencyLimit,
	})
	if err != nil {
		return channelRuntimeDTO{}, err
	}

	dto := channelRuntimeDTO{
		ID:                              ch.ID,
		ProviderEndpointID:              ch.ProviderEndpointID,
		EndpointBaseURLRevision:         ch.ProviderEndpointBaseURLRevision,
		EndpointStatusRevision:          ch.ProviderEndpointStatusRevision,
		ConfigRevision:                  ch.ConfigRevision,
		AdmissionLimitsRevision:         ch.AdmissionLimitsRevision,
		RuntimeProviderEndpointID:       positiveRuntimeInt64(snapshot.ProviderEndpointID),
		RuntimeEndpointBaseURLRevision:  positiveRuntimeInt64(snapshot.BaseURLRevision),
		RuntimeEndpointStatusRevision:   positiveRuntimeInt64(snapshot.StatusRevision),
		RuntimeConfigRevision:           positiveRuntimeInt64(snapshot.ChannelConfigRevision),
		RuntimeAdmissionActiveRevision:  positiveRuntimeInt64(control.ActiveRevision),
		RuntimeAdmissionPendingRevision: positiveRuntimeInt64(control.PendingRevision),
		AdmissionPayloadMatches:         control.ActivePayload == payload,
	}
	dto.RuntimeSyncState = classifyChannelRuntimeSync(ch, snapshot, control, dto.AdmissionPayloadMatches)
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
	if snapshot.ProviderEndpointID != ch.ProviderEndpointID ||
		snapshot.BaseURLRevision != ch.ProviderEndpointBaseURLRevision ||
		snapshot.StatusRevision != ch.ProviderEndpointStatusRevision ||
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
