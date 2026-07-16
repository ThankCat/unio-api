package provider

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi/adminhttp"

	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
	"github.com/ThankCat/unio-gateway/internal/service/admin/provider"
)

// ProviderService 定义 adminapi 操作 provider 所需的最小能力。
type ProviderService interface {
	List(ctx context.Context, params provider.ListParams) (provider.ListResult, error)
	Get(ctx context.Context, id int64) (provider.Provider, error)
	Create(ctx context.Context, in provider.CreateInput) (provider.Provider, error)
	Update(ctx context.Context, in provider.UpdateInput) (provider.Provider, error)
	Delete(ctx context.Context, id int64) error
	Archive(ctx context.Context, id int64) error
	Restore(ctx context.Context, id int64) error
}

// providerDTO 是 provider 的 admin API 响应体。
type providerDTO struct {
	ID         int64   `json:"id"`
	Slug       string  `json:"slug"`
	Name       string  `json:"name"`
	Status     string  `json:"status"`
	CreatedAt  string  `json:"created_at"`
	UpdatedAt  string  `json:"updated_at"`
	ArchivedAt *string `json:"archived_at"`
}

type createProviderRequest struct {
	Slug   string `json:"slug"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

type updateProviderRequest struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

type providersHandler struct {
	service ProviderService
}

func (h *providersHandler) list(w http.ResponseWriter, r *http.Request) {
	page := adminhttp.ParsePage(r)

	result, err := h.service.List(r.Context(), provider.ListParams{
		Status: adminhttp.ListStatus(r),
		Query:  strings.TrimSpace(r.URL.Query().Get("q")),
		Limit:  page.Limit(),
		Offset: page.Offset(),
	})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	dtos := make([]providerDTO, 0, len(result.Items))
	for _, p := range result.Items {
		dtos = append(dtos, toProviderDTO(p))
	}

	adminhttp.WriteList(w, http.StatusOK, dtos, page, result.Total)
}

func (h *providersHandler) create(w http.ResponseWriter, r *http.Request) {
	var req createProviderRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	p, err := h.service.Create(r.Context(), provider.CreateInput{
		Slug:   req.Slug,
		Name:   req.Name,
		Status: req.Status,
	})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	adminhttp.WriteData(w, http.StatusCreated, toProviderDTO(p))
}

func (h *providersHandler) update(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	var req updateProviderRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	p, err := h.service.Update(r.Context(), provider.UpdateInput{
		ID:     id,
		Name:   req.Name,
		Status: req.Status,
	})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	adminhttp.WriteData(w, http.StatusOK, toProviderDTO(p))
}

func (h *providersHandler) delete(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	if err := h.service.Delete(r.Context(), id); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *providersHandler) archive(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	if err := h.service.Archive(r.Context(), id); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *providersHandler) restore(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	if err := h.service.Restore(r.Context(), id); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func toProviderDTO(p provider.Provider) providerDTO {
	return providerDTO{
		ID:         p.ID,
		Slug:       p.Slug,
		Name:       p.Name,
		Status:     p.Status,
		CreatedAt:  p.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:  p.UpdatedAt.UTC().Format(time.RFC3339),
		ArchivedAt: adminhttp.RFC3339Ptr(p.ArchivedAt),
	}
}
