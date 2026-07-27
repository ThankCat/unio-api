package provider

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi/adminhttp"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
	adminprovider "github.com/ThankCat/unio-gateway/internal/service/admin/provider"
)

type ProviderService interface {
	List(context.Context, adminprovider.ListParams) (adminprovider.ListResult, error)
	Get(context.Context, int64) (adminprovider.Provider, error)
	Create(context.Context, adminprovider.CreateInput) (adminprovider.Provider, error)
	Update(context.Context, adminprovider.UpdateInput) (adminprovider.Provider, error)
	UpdateOrigin(context.Context, adminprovider.UpdateOriginInput) (adminprovider.Provider, error)
	UpdateStatus(context.Context, adminprovider.UpdateStatusInput) (adminprovider.Provider, error)
	Delete(context.Context, int64) error
	Archive(context.Context, int64) (adminprovider.StatusChangeResult, error)
	Restore(context.Context, int64) (adminprovider.StatusChangeResult, error)
}

type BreakerRuntime interface {
	Snapshot(context.Context, breakerstore.Scope, int64) (breakerstore.ScopeSnapshot, error)
	Reset(context.Context, breakerstore.Scope, int64) (int64, error)
}

type providerDTO struct {
	ID                 int64   `json:"id"`
	Slug               string  `json:"slug"`
	Name               string  `json:"name"`
	Origin             string  `json:"origin"`
	OriginRevision     int64   `json:"origin_revision"`
	Status             string  `json:"status"`
	StatusRevision     int64   `json:"status_revision"`
	CreatedAt          string  `json:"created_at"`
	UpdatedAt          string  `json:"updated_at"`
	ArchivedAt         *string `json:"archived_at"`
	RuntimeSyncPending bool    `json:"runtime_sync_pending"`
}

type statusChangeDTO struct {
	RuntimeSyncPending bool `json:"runtime_sync_pending"`
}

type createProviderRequest struct {
	Slug   string `json:"slug"`
	Name   string `json:"name"`
	Origin string `json:"origin"`
	Status string `json:"status"`
}

type updateProviderRequest struct {
	Name string `json:"name"`
}

type updateOriginRequest struct {
	Origin                 string `json:"origin"`
	ExpectedOriginRevision int64  `json:"expected_origin_revision"`
	ConfirmEnabledChannels bool   `json:"confirm_enabled_channels"`
}

type updateStatusRequest struct {
	Status                 string `json:"status"`
	ExpectedStatusRevision int64  `json:"expected_status_revision"`
}

type providersHandler struct{ service ProviderService }

func (handler *providersHandler) list(w http.ResponseWriter, request *http.Request) {
	page := adminhttp.ParsePage(request)
	result, err := handler.service.List(request.Context(), adminprovider.ListParams{
		Status: adminhttp.ListStatus(request), Query: strings.TrimSpace(request.URL.Query().Get("q")),
		Limit: page.Limit(), Offset: page.Offset(),
	})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	items := make([]providerDTO, 0, len(result.Items))
	for _, item := range result.Items {
		items = append(items, toProviderDTO(item))
	}
	adminhttp.WriteList(w, http.StatusOK, items, page, result.Total)
}

func (handler *providersHandler) get(w http.ResponseWriter, request *http.Request) {
	id, err := adminhttp.PathID(request)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	result, err := handler.service.Get(request.Context(), id)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, toProviderDTO(result))
}

func (handler *providersHandler) create(w http.ResponseWriter, request *http.Request) {
	var body createProviderRequest
	if err := httpx.DecodeJSON(w, request, &body); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	result, err := handler.service.Create(request.Context(), adminprovider.CreateInput{Slug: body.Slug, Name: body.Name, Origin: body.Origin, Status: body.Status})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusCreated, toProviderDTO(result))
}

func (handler *providersHandler) update(w http.ResponseWriter, request *http.Request) {
	id, err := adminhttp.PathID(request)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	var body updateProviderRequest
	if err := httpx.DecodeJSON(w, request, &body); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	result, err := handler.service.Update(request.Context(), adminprovider.UpdateInput{ID: id, Name: body.Name})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, toProviderDTO(result))
}

func (handler *providersHandler) updateOrigin(w http.ResponseWriter, request *http.Request) {
	id, err := adminhttp.PathID(request)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	var body updateOriginRequest
	if err := httpx.DecodeJSON(w, request, &body); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	result, err := handler.service.UpdateOrigin(request.Context(), adminprovider.UpdateOriginInput{
		ID: id, Origin: body.Origin, ExpectedOriginRevision: body.ExpectedOriginRevision,
		ConfirmEnabledChannels: body.ConfirmEnabledChannels,
	})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, toProviderDTO(result))
}

func (handler *providersHandler) updateStatus(w http.ResponseWriter, request *http.Request) {
	id, err := adminhttp.PathID(request)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	var body updateStatusRequest
	if err := httpx.DecodeJSON(w, request, &body); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	result, err := handler.service.UpdateStatus(request.Context(), adminprovider.UpdateStatusInput{ID: id, Status: body.Status, ExpectedStatusRevision: body.ExpectedStatusRevision})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, toProviderDTO(result))
}

func (handler *providersHandler) archive(w http.ResponseWriter, request *http.Request) {
	id, err := adminhttp.PathID(request)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	result, err := handler.service.Archive(request.Context(), id)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, statusChangeDTO{RuntimeSyncPending: result.RuntimeSyncPending})
}

func (handler *providersHandler) restore(w http.ResponseWriter, request *http.Request) {
	id, err := adminhttp.PathID(request)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	result, err := handler.service.Restore(request.Context(), id)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, statusChangeDTO{RuntimeSyncPending: result.RuntimeSyncPending})
}

func (handler *providersHandler) delete(w http.ResponseWriter, request *http.Request) {
	id, err := adminhttp.PathID(request)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	if err := handler.service.Delete(request.Context(), id); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type providerRuntimeDTO struct {
	ID                    int64  `json:"id"`
	OriginRevision        int64  `json:"origin_revision"`
	StatusRevision        int64  `json:"status_revision"`
	EffectiveStatus       string `json:"effective_status"`
	OriginRevisionState   string `json:"origin_revision_state"`
	StatusRevisionState   string `json:"status_revision_state"`
	PendingOriginRevision int64  `json:"pending_origin_revision"`
	PendingStatusRevision int64  `json:"pending_status_revision"`
	State                 string `json:"state"`
	StateGeneration       int64  `json:"state_generation"`
	RuntimeSyncState      string `json:"runtime_sync_state"`
}

type runtimeHandler struct {
	service ProviderService
	breaker BreakerRuntime
}

func (handler *runtimeHandler) runtime(w http.ResponseWriter, request *http.Request) {
	id, err := adminhttp.PathID(request)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	dto, err := handler.load(request.Context(), id)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, dto)
}

func (handler *runtimeHandler) reset(w http.ResponseWriter, request *http.Request) {
	id, err := adminhttp.PathID(request)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	if _, err := handler.service.Get(request.Context(), id); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	if handler.breaker == nil {
		adminhttp.WriteServiceError(w, failure.New(failure.CodeGatewayBreakerStoreUnavailable, failure.WithMessage("breaker runtime data source unavailable")))
		return
	}
	if _, err := handler.breaker.Reset(request.Context(), breakerstore.ScopeProvider, id); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	dto, err := handler.load(request.Context(), id)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, dto)
}

func (handler *runtimeHandler) load(ctx context.Context, id int64) (providerRuntimeDTO, error) {
	provider, err := handler.service.Get(ctx, id)
	if err != nil {
		return providerRuntimeDTO{}, err
	}
	if handler.breaker == nil {
		return providerRuntimeDTO{}, failure.New(failure.CodeGatewayBreakerStoreUnavailable, failure.WithMessage("breaker runtime data source unavailable"))
	}
	snapshot, err := handler.breaker.Snapshot(ctx, breakerstore.ScopeProvider, id)
	if err != nil {
		return providerRuntimeDTO{}, err
	}
	syncState := "active"
	if !snapshot.Exists || !snapshot.ControlPresent {
		syncState = "runtime_sync_required"
	} else if snapshot.OriginRevisionState == "pending" || snapshot.StatusRevisionState == "pending" {
		syncState = "runtime_sync_pending"
	} else if snapshot.OriginRevision != provider.OriginRevision || snapshot.StatusRevision != provider.StatusRevision || snapshot.EffectiveStatus != provider.Status {
		syncState = "stale"
	}
	return providerRuntimeDTO{
		ID: id, OriginRevision: snapshot.OriginRevision, StatusRevision: snapshot.StatusRevision,
		EffectiveStatus: snapshot.EffectiveStatus, OriginRevisionState: snapshot.OriginRevisionState,
		StatusRevisionState: snapshot.StatusRevisionState, PendingOriginRevision: snapshot.PendingOriginRevision,
		PendingStatusRevision: snapshot.PendingStatusRevision, State: string(snapshot.State),
		StateGeneration: snapshot.StateGeneration, RuntimeSyncState: syncState,
	}, nil
}

func toProviderDTO(provider adminprovider.Provider) providerDTO {
	return providerDTO{
		ID: provider.ID, Slug: provider.Slug, Name: provider.Name, Origin: provider.Origin,
		OriginRevision: provider.OriginRevision, Status: provider.Status, StatusRevision: provider.StatusRevision,
		CreatedAt: provider.CreatedAt.UTC().Format(time.RFC3339), UpdatedAt: provider.UpdatedAt.UTC().Format(time.RFC3339),
		ArchivedAt: adminhttp.RFC3339Ptr(provider.ArchivedAt), RuntimeSyncPending: provider.RuntimeSyncPending,
	}
}
