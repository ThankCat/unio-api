// Package providerendpoint 暴露 admin 管理端 ProviderEndpoint 的 HTTP 表面（P4 §8.1、§8.5）。
package providerendpoint

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi/adminhttp"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
	"github.com/ThankCat/unio-gateway/internal/service/admin/providerendpoint"
)

// BreakerRuntime 暴露 admin 只读运行态与显式复位（§8.4/§8.5）；由 *breakerstore.Store 实现。
type BreakerRuntime interface {
	Snapshot(ctx context.Context, scope breakerstore.Scope, id int64) (breakerstore.ScopeSnapshot, error)
	Reset(ctx context.Context, scope breakerstore.Scope, id int64) (int64, error)
}

// breakerSnapshotDTO 是作用域 breaker 运行态的 admin 只读视图（§8.4：客观事实，不含健康分桶）。
type breakerSnapshotDTO struct {
	Scope                  string  `json:"scope"`
	ID                     int64   `json:"id"`
	Exists                 bool    `json:"exists"`
	State                  string  `json:"state"`
	OpenRemainingMs        int64   `json:"open_remaining_ms"`
	OpenLevel              int     `json:"open_level"`
	EligibleSuccesses      int64   `json:"eligible_successes"`
	EligibleFailures       int64   `json:"eligible_failures"`
	ConsecutiveFailures    int64   `json:"consecutive_failures"`
	ErrorRate              float64 `json:"error_rate"`
	SampleCount            int64   `json:"sample_count"`
	TTFTEWMAMs             float64 `json:"ttft_ewma_ms"`
	TTFTSamples            int64   `json:"ttft_samples"`
	TTFTSampleSource       string  `json:"ttft_sample_source"`
	ActiveBaseURLRevision  int64   `json:"active_base_url_revision"`
	PendingBaseURLRevision int64   `json:"pending_base_url_revision"`
	ActiveStatusRevision   int64   `json:"active_status_revision"`
	PendingStatusRevision  int64   `json:"pending_status_revision"`
	EffectiveStatus        string  `json:"effective_status"`
}

func toBreakerSnapshotDTO(s breakerstore.ScopeSnapshot) breakerSnapshotDTO {
	return breakerSnapshotDTO{
		Scope:                  string(s.Scope),
		ID:                     s.ID,
		Exists:                 s.Exists,
		State:                  string(s.State),
		OpenRemainingMs:        s.OpenRemainingMs,
		OpenLevel:              s.OpenLevel,
		EligibleSuccesses:      s.EligibleSuccesses,
		EligibleFailures:       s.EligibleFailures,
		ConsecutiveFailures:    s.ConsecutiveFailures,
		ErrorRate:              s.ErrorRate,
		SampleCount:            s.SampleCount,
		TTFTEWMAMs:             s.TTFTEWMAMs,
		TTFTSamples:            s.TTFTSamples,
		TTFTSampleSource:       "stream_only",
		ActiveBaseURLRevision:  s.BaseURLRevision,
		PendingBaseURLRevision: s.PendingBaseURLRevision,
		ActiveStatusRevision:   s.StatusRevision,
		PendingStatusRevision:  s.PendingStatusRevision,
		EffectiveStatus:        s.EffectiveStatus,
	}
}

// ProviderEndpointService 定义 adminapi 操作 ProviderEndpoint 所需的最小能力。
type ProviderEndpointService interface {
	List(ctx context.Context, params providerendpoint.ListParams) (providerendpoint.ListResult, error)
	Get(ctx context.Context, id int64) (providerendpoint.ProviderEndpoint, error)
	Create(ctx context.Context, in providerendpoint.CreateInput) (providerendpoint.ProviderEndpoint, error)
	UpdateName(ctx context.Context, id int64, name string) (providerendpoint.ProviderEndpoint, error)
	UpdateStatus(ctx context.Context, id int64, status string) (providerendpoint.ProviderEndpoint, error)
	UpdateBaseURL(ctx context.Context, id int64, baseURL string) (providerendpoint.ProviderEndpoint, error)
	UpdateRouting(ctx context.Context, id int64, baseURL, status string) (providerendpoint.ProviderEndpoint, error)
}

type providerEndpointDTO struct {
	ID                            int64   `json:"id"`
	ProviderID                    int64   `json:"provider_id"`
	ProviderName                  string  `json:"provider_name"`
	Name                          string  `json:"name"`
	BaseURL                       string  `json:"base_url"`
	BaseURLRevision               int64   `json:"base_url_revision"`
	Status                        string  `json:"status"`
	StatusRevision                int64   `json:"status_revision"`
	ChannelCount                  int64   `json:"channel_count"`
	RuntimeSyncPending            bool    `json:"runtime_sync_pending"`
	RuntimeSyncState              string  `json:"runtime_sync_state"`
	RuntimeActiveBaseURLRevision  *int64  `json:"runtime_active_base_url_revision"`
	RuntimePendingBaseURLRevision *int64  `json:"runtime_pending_base_url_revision"`
	RuntimeActiveStatusRevision   *int64  `json:"runtime_active_status_revision"`
	RuntimePendingStatusRevision  *int64  `json:"runtime_pending_status_revision"`
	RuntimeEffectiveStatus        *string `json:"runtime_effective_status"`
	ArchivedAt                    *string `json:"archived_at"`
	CreatedAt                     string  `json:"created_at"`
	UpdatedAt                     string  `json:"updated_at"`
}

type createRequest struct {
	ProviderID int64  `json:"provider_id"`
	Name       string `json:"name"`
	BaseURL    string `json:"base_url"`
	Status     string `json:"status"`
}

type updateRequest struct {
	Name string `json:"name"`
}

type updateStatusRequest struct {
	Status string `json:"status"`
}

type updateBaseURLRequest struct {
	BaseURL string `json:"base_url"`
}

type updateRoutingRequest struct {
	BaseURL string `json:"base_url"`
	Status  string `json:"status"`
}

type handler struct {
	service ProviderEndpointService
	breaker BreakerRuntime // 可空：Redis 缺失时运行态/复位不可用
}

func (h *handler) runtime(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	if h.breaker == nil {
		adminhttp.WriteServiceError(w, failure.New(failure.CodeGatewayBreakerStoreUnavailable, failure.WithMessage("breaker runtime data source unavailable")))
		return
	}
	snap, err := h.breaker.Snapshot(r.Context(), breakerstore.ScopeEndpoint, id)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, toBreakerSnapshotDTO(snap))
}

func (h *handler) resetBreaker(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	if h.breaker == nil {
		adminhttp.WriteServiceError(w, failure.New(failure.CodeGatewayBreakerStoreUnavailable, failure.WithMessage("breaker runtime data source unavailable")))
		return
	}
	if _, err := h.breaker.Reset(r.Context(), breakerstore.ScopeEndpoint, id); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	snap, err := h.breaker.Snapshot(r.Context(), breakerstore.ScopeEndpoint, id)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, toBreakerSnapshotDTO(snap))
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	providerID := int64(0)
	if raw := strings.TrimSpace(r.URL.Query().Get("provider_id")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed <= 0 {
			adminhttp.WriteServiceError(w, failure.New(
				failure.CodeAdminInvalidArgument,
				failure.WithMessage("provider_id query must be a positive integer"),
				failure.WithField("field", "provider_id"),
			))
			return
		}
		providerID = parsed
	}
	page := adminhttp.ParsePage(r)
	result, err := h.service.List(r.Context(), providerendpoint.ListParams{
		ProviderID: providerID,
		Status:     adminhttp.ListStatus(r),
		Query:      strings.TrimSpace(r.URL.Query().Get("q")),
		Limit:      page.Limit(),
		Offset:     page.Offset(),
	})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	dtos := make([]providerEndpointDTO, 0, len(result.Items))
	for _, ep := range result.Items {
		dtos = append(dtos, h.toDTOWithRuntime(r.Context(), ep))
	}
	adminhttp.WriteList(w, http.StatusOK, dtos, page, result.Total)
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	ep, err := h.service.Get(r.Context(), id)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, h.toDTOWithRuntime(r.Context(), ep))
}

func (h *handler) create(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	ep, err := h.service.Create(r.Context(), providerendpoint.CreateInput{
		ProviderID: req.ProviderID,
		Name:       req.Name,
		BaseURL:    req.BaseURL,
		Status:     req.Status,
	})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusCreated, toDTO(ep))
}

func (h *handler) update(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	var req updateRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	ep, err := h.service.UpdateName(r.Context(), id, req.Name)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, toDTO(ep))
}

func (h *handler) updateStatus(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	var req updateStatusRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	ep, err := h.service.UpdateStatus(r.Context(), id, req.Status)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, toDTO(ep))
}

func (h *handler) updateBaseURL(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	var req updateBaseURLRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	ep, err := h.service.UpdateBaseURL(r.Context(), id, req.BaseURL)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, toDTO(ep))
}

func (h *handler) updateRouting(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	var req updateRoutingRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	ep, err := h.service.UpdateRouting(r.Context(), id, req.BaseURL, req.Status)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, toDTO(ep))
}

func toDTO(ep providerendpoint.ProviderEndpoint) providerEndpointDTO {
	runtimeState := "active"
	if ep.RuntimeSyncPending {
		runtimeState = "runtime_sync_pending"
	}
	dto := providerEndpointDTO{
		ID:                 ep.ID,
		ProviderID:         ep.ProviderID,
		ProviderName:       ep.ProviderName,
		Name:               ep.Name,
		BaseURL:            ep.BaseURL,
		BaseURLRevision:    ep.BaseURLRevision,
		Status:             ep.Status,
		StatusRevision:     ep.StatusRevision,
		ChannelCount:       ep.ChannelCount,
		RuntimeSyncPending: ep.RuntimeSyncPending,
		RuntimeSyncState:   runtimeState,
		CreatedAt:          ep.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:          ep.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if ep.ArchivedAt != nil {
		s := ep.ArchivedAt.UTC().Format(time.RFC3339)
		dto.ArchivedAt = &s
	}
	return dto
}

func (h *handler) toDTOWithRuntime(ctx context.Context, ep providerendpoint.ProviderEndpoint) providerEndpointDTO {
	dto := toDTO(ep)
	if h.breaker == nil {
		dto.RuntimeSyncPending = false
		dto.RuntimeSyncState = "store_unavailable"
		return dto
	}
	snapshot, err := h.breaker.Snapshot(ctx, breakerstore.ScopeEndpoint, ep.ID)
	if err != nil {
		dto.RuntimeSyncPending = false
		dto.RuntimeSyncState = "store_unavailable"
		return dto
	}
	dto.RuntimeSyncState = classifyEndpointRuntimeSync(ep, snapshot)
	dto.RuntimeSyncPending = dto.RuntimeSyncState == "runtime_sync_pending"
	dto.RuntimeActiveBaseURLRevision = positiveInt64(snapshot.BaseURLRevision)
	dto.RuntimePendingBaseURLRevision = positiveInt64(snapshot.PendingBaseURLRevision)
	dto.RuntimeActiveStatusRevision = positiveInt64(snapshot.StatusRevision)
	dto.RuntimePendingStatusRevision = positiveInt64(snapshot.PendingStatusRevision)
	if snapshot.EffectiveStatus != "" {
		effectiveStatus := snapshot.EffectiveStatus
		dto.RuntimeEffectiveStatus = &effectiveStatus
	}
	return dto
}

func classifyEndpointRuntimeSync(ep providerendpoint.ProviderEndpoint, snapshot breakerstore.ScopeSnapshot) string {
	if !snapshot.Exists || !snapshot.ControlPresent {
		return "runtime_sync_required"
	}
	if snapshot.BaseURLRevisionState == "pending" || snapshot.StatusRevisionState == "pending" ||
		snapshot.PendingBaseURLRevision > 0 || snapshot.PendingStatusRevision > 0 {
		return "runtime_sync_pending"
	}
	if snapshot.BaseURLRevision != ep.BaseURLRevision || snapshot.StatusRevision != ep.StatusRevision ||
		snapshot.EffectiveStatus != ep.Status {
		return "stale"
	}
	return "active"
}

func positiveInt64(value int64) *int64 {
	if value <= 0 {
		return nil
	}
	return &value
}
