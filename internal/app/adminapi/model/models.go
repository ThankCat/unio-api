package model

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/ThankCat/unio-api/internal/app/adminapi/adminhttp"

	"github.com/ThankCat/unio-api/internal/platform/httpx"
	"github.com/ThankCat/unio-api/internal/service/admin/model"
)

// ModelService 定义 adminapi 操作 model 所需的最小能力。
type ModelService interface {
	List(ctx context.Context, params model.ListParams) (model.ListResult, error)
	Get(ctx context.Context, id int64) (model.Model, error)
	Create(ctx context.Context, in model.CreateInput) (model.Model, error)
	Update(ctx context.Context, in model.UpdateInput) (model.Model, error)
	Delete(ctx context.Context, id int64) error
}

// modelDTO 是 model 的 admin API 响应体。
// Source 标明来源（manual=空白手建 / catalog=从 models.dev 目录采纳）；元数据为快照展示、不参与计费。
// Catalog 为采纳目录追更状态（未采纳模型为 null）。
type modelDTO struct {
	ID                       int64                 `json:"id"`
	ModelID                  string                `json:"model_id"`
	DisplayName              string                `json:"display_name"`
	OwnedBy                  string                `json:"owned_by"`
	Status                   string                `json:"status"`
	MaxOutputTokens          *int64                `json:"max_output_tokens"`
	ContextWindowTokens      *int64                `json:"context_window_tokens"`
	InputPriceUSDPerMTokens  *string               `json:"input_price_usd_per_million_tokens"`
	OutputPriceUSDPerMTokens *string               `json:"output_price_usd_per_million_tokens"`
	ReleaseDate              *string               `json:"release_date"`
	Source                   string                `json:"source"`
	Catalog                  *modelCatalogStateDTO `json:"catalog"`
	CreatedAt                string                `json:"created_at"`
	UpdatedAt                string                `json:"updated_at"`
}

// modelCatalogStateDTO 是采纳模型相对 models.dev 目录的追更状态。
type modelCatalogStateDTO struct {
	CanonicalID     string           `json:"canonical_id"`
	UpdateAvailable bool             `json:"update_available"`
	RemovedUpstream bool             `json:"removed_upstream"`
	ShouldRemind    bool             `json:"should_remind"`
	Reminder        modelReminderDTO `json:"reminder"`
}

// modelReminderDTO 是更新提醒的忽略/静音/稍后状态。
type modelReminderDTO struct {
	Muted       bool    `json:"muted"`
	SnoozeUntil *string `json:"snooze_until"`
	Dismissed   bool    `json:"dismissed"`
}

type modelMetadataRequest struct {
	MaxOutputTokens          *int64  `json:"max_output_tokens"`
	ContextWindowTokens      *int64  `json:"context_window_tokens"`
	InputPriceUSDPerMTokens  *string `json:"input_price_usd_per_million_tokens"`
	OutputPriceUSDPerMTokens *string `json:"output_price_usd_per_million_tokens"`
	ReleaseDate              *string `json:"release_date"`
}

type createModelRequest struct {
	ModelID     string `json:"model_id"`
	DisplayName string `json:"display_name"`
	OwnedBy     string `json:"owned_by"`
	Status      string `json:"status"`
	modelMetadataRequest
}

type updateModelRequest struct {
	DisplayName string `json:"display_name"`
	OwnedBy     string `json:"owned_by"`
	Status      string `json:"status"`
	modelMetadataRequest
}

type modelsHandler struct {
	service ModelService
}

func (h *modelsHandler) list(w http.ResponseWriter, r *http.Request) {
	page := adminhttp.ParsePage(r)

	result, err := h.service.List(r.Context(), model.ListParams{
		Status:        adminhttp.ListStatus(r),
		Query:         strings.TrimSpace(r.URL.Query().Get("q")),
		HasUpdateOnly: adminhttp.ParseBoolQuery(r, "has_update"),
		Limit:         page.Limit(),
		Offset:        page.Offset(),
	})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	dtos := make([]modelDTO, 0, len(result.Items))
	for _, m := range result.Items {
		dtos = append(dtos, toModelDTO(m))
	}

	adminhttp.WriteList(w, http.StatusOK, dtos, page, result.Total)
}

func (h *modelsHandler) get(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	m, err := h.service.Get(r.Context(), id)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	adminhttp.WriteData(w, http.StatusOK, toModelDTO(m))
}

func (h *modelsHandler) create(w http.ResponseWriter, r *http.Request) {
	var req createModelRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	meta, err := req.modelMetadataRequest.toMetadata()
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	m, err := h.service.Create(r.Context(), model.CreateInput{
		ModelID:     req.ModelID,
		DisplayName: req.DisplayName,
		OwnedBy:     req.OwnedBy,
		Status:      req.Status,
		Metadata:    meta,
	})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	adminhttp.WriteData(w, http.StatusCreated, toModelDTO(m))
}

func (h *modelsHandler) update(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	var req updateModelRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	meta, err := req.modelMetadataRequest.toMetadata()
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	m, err := h.service.Update(r.Context(), model.UpdateInput{
		ID:          id,
		DisplayName: req.DisplayName,
		OwnedBy:     req.OwnedBy,
		Status:      req.Status,
		Metadata:    meta,
	})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	adminhttp.WriteData(w, http.StatusOK, toModelDTO(m))
}

func (h *modelsHandler) delete(w http.ResponseWriter, r *http.Request) {
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

// toMetadata 把请求里的可选元数据转成 service 入参；release_date 解析为日期，非法返回 400。
func (m modelMetadataRequest) toMetadata() (model.Metadata, error) {
	meta := model.Metadata{
		MaxOutputTokens:          m.MaxOutputTokens,
		ContextWindowTokens:      m.ContextWindowTokens,
		InputPriceUSDPerMTokens:  trimOptional(m.InputPriceUSDPerMTokens),
		OutputPriceUSDPerMTokens: trimOptional(m.OutputPriceUSDPerMTokens),
	}
	if m.ReleaseDate != nil && strings.TrimSpace(*m.ReleaseDate) != "" {
		parsed, err := time.Parse("2006-01-02", strings.TrimSpace(*m.ReleaseDate))
		if err != nil {
			return model.Metadata{}, adminhttp.InvalidRequestField("release_date", "release_date must be YYYY-MM-DD")
		}
		meta.ReleaseDate = &parsed
	}
	return meta, nil
}

func toModelDTO(m model.Model) modelDTO {
	dto := modelDTO{
		ID:                       m.ID,
		ModelID:                  m.ModelID,
		DisplayName:              m.DisplayName,
		OwnedBy:                  m.OwnedBy,
		Status:                   m.Status,
		MaxOutputTokens:          m.MaxOutputTokens,
		ContextWindowTokens:      m.ContextWindowTokens,
		InputPriceUSDPerMTokens:  m.InputPriceUSDPerMTokens,
		OutputPriceUSDPerMTokens: m.OutputPriceUSDPerMTokens,
		Source:                   m.Source,
		CreatedAt:                m.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:                m.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if m.ReleaseDate != nil {
		formatted := m.ReleaseDate.Format("2006-01-02")
		dto.ReleaseDate = &formatted
	}
	if m.Catalog != nil {
		state := modelCatalogStateDTO{
			CanonicalID:     m.Catalog.CanonicalID,
			UpdateAvailable: m.Catalog.UpdateAvailable,
			RemovedUpstream: m.Catalog.RemovedUpstream,
			ShouldRemind:    m.Catalog.ShouldRemind,
			Reminder: modelReminderDTO{
				Muted:     m.Catalog.ReminderMuted,
				Dismissed: m.Catalog.DismissedSame,
			},
		}
		if m.Catalog.SnoozeUntil != nil {
			formatted := m.Catalog.SnoozeUntil.UTC().Format(time.RFC3339)
			state.Reminder.SnoozeUntil = &formatted
		}
		dto.Catalog = &state
	}
	return dto
}

// trimOptional 去空白；空串视为未设置（nil）。
func trimOptional(s *string) *string {
	if s == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*s)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}
