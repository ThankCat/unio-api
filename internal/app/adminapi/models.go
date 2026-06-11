package adminapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/ThankCat/unio-api/internal/platform/httpx"
	"github.com/ThankCat/unio-api/internal/service/admin/model"
)

// ModelService 定义 adminapi 操作 model 所需的最小能力。
type ModelService interface {
	List(ctx context.Context, params model.ListParams) (model.ListResult, error)
	Get(ctx context.Context, id int64) (model.Model, error)
	Create(ctx context.Context, in model.CreateInput) (model.Model, error)
	Update(ctx context.Context, in model.UpdateInput) (model.Model, error)
}

// modelDTO 是 model 的 admin API 响应体。
// Source 标明来源（manual / seed_models_dev / import），前端据此提示 seed 行的元数据会被同步覆盖。
type modelDTO struct {
	ID              int64  `json:"id"`
	ModelID         string `json:"model_id"`
	DisplayName     string `json:"display_name"`
	OwnedBy         string `json:"owned_by"`
	Status          string `json:"status"`
	Lab             string `json:"lab"`
	MaxOutputTokens *int64 `json:"max_output_tokens"`
	Source          string `json:"source"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
}

type createModelRequest struct {
	ModelID         string `json:"model_id"`
	DisplayName     string `json:"display_name"`
	OwnedBy         string `json:"owned_by"`
	Status          string `json:"status"`
	Lab             string `json:"lab"`
	MaxOutputTokens *int64 `json:"max_output_tokens"`
}

type updateModelRequest struct {
	DisplayName     string `json:"display_name"`
	OwnedBy         string `json:"owned_by"`
	Status          string `json:"status"`
	Lab             string `json:"lab"`
	MaxOutputTokens *int64 `json:"max_output_tokens"`
}

type modelsHandler struct {
	service ModelService
}

func (h *modelsHandler) list(w http.ResponseWriter, r *http.Request) {
	page := parsePage(r)

	result, err := h.service.List(r.Context(), model.ListParams{
		Status: listStatus(r),
		Query:  strings.TrimSpace(r.URL.Query().Get("q")),
		Limit:  page.Limit(),
		Offset: page.Offset(),
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}

	dtos := make([]modelDTO, 0, len(result.Items))
	for _, m := range result.Items {
		dtos = append(dtos, toModelDTO(m))
	}

	writeList(w, http.StatusOK, dtos, page, result.Total)
}

func (h *modelsHandler) get(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	m, err := h.service.Get(r.Context(), id)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	writeData(w, http.StatusOK, toModelDTO(m))
}

func (h *modelsHandler) create(w http.ResponseWriter, r *http.Request) {
	var req createModelRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		writeServiceError(w, err)
		return
	}

	m, err := h.service.Create(r.Context(), model.CreateInput{
		ModelID:         req.ModelID,
		DisplayName:     req.DisplayName,
		OwnedBy:         req.OwnedBy,
		Status:          req.Status,
		Lab:             optionalString(req.Lab),
		MaxOutputTokens: req.MaxOutputTokens,
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}

	writeData(w, http.StatusCreated, toModelDTO(m))
}

func (h *modelsHandler) update(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	var req updateModelRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		writeServiceError(w, err)
		return
	}

	m, err := h.service.Update(r.Context(), model.UpdateInput{
		ID:              id,
		DisplayName:     req.DisplayName,
		OwnedBy:         req.OwnedBy,
		Status:          req.Status,
		Lab:             optionalString(req.Lab),
		MaxOutputTokens: req.MaxOutputTokens,
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}

	writeData(w, http.StatusOK, toModelDTO(m))
}

func toModelDTO(m model.Model) modelDTO {
	dto := modelDTO{
		ID:              m.ID,
		ModelID:         m.ModelID,
		DisplayName:     m.DisplayName,
		OwnedBy:         m.OwnedBy,
		Status:          m.Status,
		MaxOutputTokens: m.MaxOutputTokens,
		Source:          m.Source,
		CreatedAt:       m.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:       m.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if m.Lab != nil {
		dto.Lab = *m.Lab
	}
	return dto
}

// optionalString 把请求里的字符串转成可选指针：空串视为未设置（nil）。
func optionalString(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}
