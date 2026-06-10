package adminapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/ThankCat/unio-api/internal/platform/httpx"
	"github.com/ThankCat/unio-api/internal/service/admin/provider"
)

// ProviderService 定义 adminapi 操作 provider 所需的最小能力。
type ProviderService interface {
	List(ctx context.Context, params provider.ListParams) (provider.ListResult, error)
	Get(ctx context.Context, id int64) (provider.Provider, error)
	Create(ctx context.Context, in provider.CreateInput) (provider.Provider, error)
	Update(ctx context.Context, in provider.UpdateInput) (provider.Provider, error)
}

// providerDTO 是 provider 的 admin API 响应体。
type providerDTO struct {
	ID        int64  `json:"id"`
	Slug      string `json:"slug"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
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
	page := parsePage(r)

	result, err := h.service.List(r.Context(), provider.ListParams{
		Status: listStatus(r),
		Query:  strings.TrimSpace(r.URL.Query().Get("q")),
		Limit:  page.Limit(),
		Offset: page.Offset(),
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}

	dtos := make([]providerDTO, 0, len(result.Items))
	for _, p := range result.Items {
		dtos = append(dtos, toProviderDTO(p))
	}

	writeList(w, http.StatusOK, dtos, page, result.Total)
}

func (h *providersHandler) get(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	p, err := h.service.Get(r.Context(), id)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	writeData(w, http.StatusOK, toProviderDTO(p))
}

func (h *providersHandler) create(w http.ResponseWriter, r *http.Request) {
	var req createProviderRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		writeServiceError(w, err)
		return
	}

	p, err := h.service.Create(r.Context(), provider.CreateInput{
		Slug:   req.Slug,
		Name:   req.Name,
		Status: req.Status,
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}

	writeData(w, http.StatusCreated, toProviderDTO(p))
}

func (h *providersHandler) update(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	var req updateProviderRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		writeServiceError(w, err)
		return
	}

	p, err := h.service.Update(r.Context(), provider.UpdateInput{
		ID:     id,
		Name:   req.Name,
		Status: req.Status,
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}

	writeData(w, http.StatusOK, toProviderDTO(p))
}

func toProviderDTO(p provider.Provider) providerDTO {
	return providerDTO{
		ID:        p.ID,
		Slug:      p.Slug,
		Name:      p.Name,
		Status:    p.Status,
		CreatedAt: p.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt: p.UpdatedAt.UTC().Format(time.RFC3339),
	}
}
