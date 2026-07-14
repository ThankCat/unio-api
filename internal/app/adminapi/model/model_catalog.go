package model

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/ThankCat/unio-api/internal/app/adminapi/adminhttp"

	"github.com/go-chi/chi/v5"

	"github.com/ThankCat/unio-api/internal/platform/httpx"
	modelcatalogadmin "github.com/ThankCat/unio-api/internal/service/admin/modelcatalog"
)

// CatalogService 定义 adminapi 操作 models.dev 目录与采纳/刷新/提醒所需的最小能力。
type CatalogService interface {
	List(ctx context.Context, params modelcatalogadmin.ListParams) (modelcatalogadmin.ListResult, error)
	Get(ctx context.Context, canonicalID string) (modelcatalogadmin.EntryDetail, error)
	Adopt(ctx context.Context, in modelcatalogadmin.AdoptInput) (int64, error)
	Refresh(ctx context.Context, modelID int64) error
	SetReminder(ctx context.Context, modelID int64, action modelcatalogadmin.ReminderAction, snoozeUntil *time.Time) error
}

type catalogEntryDTO struct {
	CanonicalID              string  `json:"canonical_id"`
	Lab                      string  `json:"lab"`
	DisplayName              string  `json:"display_name"`
	ContextWindowTokens      *int64  `json:"context_window_tokens"`
	MaxOutputTokens          *int64  `json:"max_output_tokens"`
	InputPriceUSDPerMTokens  *string `json:"input_price_usd_per_million_tokens"`
	OutputPriceUSDPerMTokens *string `json:"output_price_usd_per_million_tokens"`
	ReleaseDate              *string `json:"release_date"`
	RemovedUpstream          bool    `json:"removed_upstream"`
	Fingerprint              string  `json:"fingerprint"`
	CapabilityCount          int64   `json:"capability_count"`
	AdoptedCount             int64   `json:"adopted_count"`
}

type catalogCapabilityHintDTO struct {
	CapabilityKey string          `json:"capability_key"`
	SupportLevel  string          `json:"support_level"`
	Limits        json.RawMessage `json:"limits"`
}

type catalogEntryDetailDTO struct {
	catalogEntryDTO
	Capabilities []catalogCapabilityHintDTO `json:"capabilities"`
}

type adoptFromCatalogRequest struct {
	CanonicalID string `json:"canonical_id"`
	ModelID     string `json:"model_id"`
	DisplayName string `json:"display_name"`
	OwnedBy     string `json:"owned_by"`
	Status      string `json:"status"`
	modelMetadataRequest
	Capabilities []catalogCapabilityHintDTO `json:"capabilities"`
}

type catalogReminderRequest struct {
	Action      string  `json:"action"`
	SnoozeUntil *string `json:"snooze_until"`
}

type catalogHandler struct {
	catalog CatalogService
	models  ModelService
}

func (h *catalogHandler) list(w http.ResponseWriter, r *http.Request) {
	page := adminhttp.ParsePage(r)

	result, err := h.catalog.List(r.Context(), modelcatalogadmin.ListParams{
		Query:  strings.TrimSpace(r.URL.Query().Get("q")),
		Lab:    strings.TrimSpace(r.URL.Query().Get("lab")),
		Limit:  page.Limit(),
		Offset: page.Offset(),
	})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	dtos := make([]catalogEntryDTO, 0, len(result.Items))
	for _, e := range result.Items {
		dtos = append(dtos, toCatalogEntryDTO(e))
	}

	adminhttp.WriteList(w, http.StatusOK, dtos, page, result.Total)
}

func (h *catalogHandler) get(w http.ResponseWriter, r *http.Request) {
	canonicalID := strings.TrimSpace(chi.URLParam(r, "*"))
	if canonicalID == "" {
		adminhttp.WriteServiceError(w, adminhttp.InvalidRequestField("canonical_id", "canonical_id is required"))
		return
	}

	detail, err := h.catalog.Get(r.Context(), canonicalID)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	dto := catalogEntryDetailDTO{catalogEntryDTO: toCatalogEntryDTO(detail.Entry)}
	dto.Capabilities = make([]catalogCapabilityHintDTO, 0, len(detail.Capabilities))
	for _, c := range detail.Capabilities {
		dto.Capabilities = append(dto.Capabilities, catalogCapabilityHintDTO{
			CapabilityKey: c.Key,
			SupportLevel:  c.SupportLevel,
			Limits:        c.Limits,
		})
	}

	adminhttp.WriteData(w, http.StatusOK, dto)
}

func (h *catalogHandler) adopt(w http.ResponseWriter, r *http.Request) {
	var req adoptFromCatalogRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	meta, err := req.modelMetadataRequest.toMetadata()
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	caps := make([]modelcatalogadmin.CapabilityHint, 0, len(req.Capabilities))
	for _, c := range req.Capabilities {
		caps = append(caps, modelcatalogadmin.CapabilityHint{
			Key:          c.CapabilityKey,
			SupportLevel: c.SupportLevel,
			Limits:       c.Limits,
		})
	}

	id, err := h.catalog.Adopt(r.Context(), modelcatalogadmin.AdoptInput{
		CanonicalID:              req.CanonicalID,
		ModelID:                  req.ModelID,
		DisplayName:              req.DisplayName,
		OwnedBy:                  req.OwnedBy,
		Status:                   req.Status,
		ContextWindowTokens:      meta.ContextWindowTokens,
		MaxOutputTokens:          meta.MaxOutputTokens,
		InputPriceUSDPerMTokens:  meta.InputPriceUSDPerMTokens,
		OutputPriceUSDPerMTokens: meta.OutputPriceUSDPerMTokens,
		ReleaseDate:              meta.ReleaseDate,
		Capabilities:             caps,
	})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	h.writeModel(w, r, id, http.StatusCreated)
}

// writeModel 回读模型完整视图（含 catalog 追更状态）并写出，供采纳后返回最新态。
func (h *catalogHandler) writeModel(w http.ResponseWriter, r *http.Request, id int64, status int) {
	m, err := h.models.Get(r.Context(), id)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, status, toModelDTO(m))
}

func toCatalogEntryDTO(e modelcatalogadmin.Entry) catalogEntryDTO {
	dto := catalogEntryDTO{
		CanonicalID:              e.CanonicalID,
		Lab:                      e.Lab,
		DisplayName:              e.DisplayName,
		ContextWindowTokens:      e.ContextWindowTokens,
		MaxOutputTokens:          e.MaxOutputTokens,
		InputPriceUSDPerMTokens:  e.InputPriceUSDPerMTokens,
		OutputPriceUSDPerMTokens: e.OutputPriceUSDPerMTokens,
		RemovedUpstream:          e.RemovedUpstream,
		Fingerprint:              e.Fingerprint,
		CapabilityCount:          e.CapabilityCount,
		AdoptedCount:             e.AdoptedCount,
	}
	if e.ReleaseDate != nil {
		formatted := e.ReleaseDate.Format("2006-01-02")
		dto.ReleaseDate = &formatted
	}
	return dto
}
