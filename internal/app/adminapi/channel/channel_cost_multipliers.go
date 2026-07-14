package channel

import (
	"context"
	"net/http"
	"time"

	"github.com/ThankCat/unio-api/internal/app/adminapi/adminhttp"

	"github.com/ThankCat/unio-api/internal/platform/httpx"
	"github.com/ThankCat/unio-api/internal/service/admin/channelcostmultiplier"
)

// ChannelCostMultiplierService 定义 adminapi 操作渠道价格倍率（channel_cost_multipliers）所需的最小能力（DEC-027）。
type ChannelCostMultiplierService interface {
	List(ctx context.Context, channelID int64) ([]channelcostmultiplier.ChannelCostMultiplier, error)
	Create(ctx context.Context, in channelcostmultiplier.CreateInput) (channelcostmultiplier.ChannelCostMultiplier, error)
	Update(ctx context.Context, in channelcostmultiplier.UpdateInput) (channelcostmultiplier.ChannelCostMultiplier, error)
}

// channelCostMultiplierDTO 是渠道价格倍率的 admin API 响应体。model_id 为 null 表示渠道默认倍率；
// model_external_id / model_display_name 仅逐模型覆盖行有值。
type channelCostMultiplierDTO struct {
	ID               int64   `json:"id"`
	ChannelID        int64   `json:"channel_id"`
	ModelID          *int64  `json:"model_id"`
	ModelExternalID  *string `json:"model_external_id"`
	ModelDisplayName *string `json:"model_display_name"`
	Multiplier       string  `json:"multiplier"`
	Status           string  `json:"status"`
	EffectiveFrom    string  `json:"effective_from"`
	EffectiveTo      *string `json:"effective_to"`
	CreatedAt        string  `json:"created_at"`
	UpdatedAt        string  `json:"updated_at"`
}

type createChannelCostMultiplierRequest struct {
	// ModelID 为 null 表示渠道默认倍率；非 null 表示对该模型的覆盖。
	ModelID       *int64  `json:"model_id"`
	Multiplier    string  `json:"multiplier"`
	Status        string  `json:"status"`
	EffectiveFrom string  `json:"effective_from"`
	EffectiveTo   *string `json:"effective_to"`
}

type updateChannelCostMultiplierRequest struct {
	Status      string  `json:"status"`
	EffectiveTo *string `json:"effective_to"`
}

type channelCostMultipliersHandler struct {
	service ChannelCostMultiplierService
}

func (h *channelCostMultipliersHandler) list(w http.ResponseWriter, r *http.Request) {
	channelID, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	items, err := h.service.List(r.Context(), channelID)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	dtos := make([]channelCostMultiplierDTO, 0, len(items))
	for _, m := range items {
		dtos = append(dtos, toChannelCostMultiplierDTO(m))
	}

	adminhttp.WriteData(w, http.StatusOK, dtos)
}

func (h *channelCostMultipliersHandler) create(w http.ResponseWriter, r *http.Request) {
	channelID, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	var req createChannelCostMultiplierRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	from, err := adminhttp.ParseRFC3339("effective_from", req.EffectiveFrom)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	to, err := adminhttp.ParseOptionalRFC3339("effective_to", req.EffectiveTo)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	m, err := h.service.Create(r.Context(), channelcostmultiplier.CreateInput{
		ChannelID:     channelID,
		ModelID:       req.ModelID,
		Multiplier:    req.Multiplier,
		Status:        req.Status,
		EffectiveFrom: from,
		EffectiveTo:   to,
	})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	adminhttp.WriteData(w, http.StatusCreated, toChannelCostMultiplierDTO(m))
}

func (h *channelCostMultipliersHandler) update(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	var req updateChannelCostMultiplierRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	to, err := adminhttp.ParseOptionalRFC3339("effective_to", req.EffectiveTo)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	m, err := h.service.Update(r.Context(), channelcostmultiplier.UpdateInput{
		ID:          id,
		Status:      req.Status,
		EffectiveTo: to,
	})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	adminhttp.WriteData(w, http.StatusOK, toChannelCostMultiplierDTO(m))
}

func toChannelCostMultiplierDTO(m channelcostmultiplier.ChannelCostMultiplier) channelCostMultiplierDTO {
	dto := channelCostMultiplierDTO{
		ID:               m.ID,
		ChannelID:        m.ChannelID,
		ModelID:          m.ModelID,
		ModelExternalID:  m.ModelExternalID,
		ModelDisplayName: m.ModelDisplayName,
		Multiplier:       m.Multiplier,
		Status:           m.Status,
		EffectiveFrom:    m.EffectiveFrom.UTC().Format(time.RFC3339),
		CreatedAt:        m.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:        m.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if m.EffectiveTo != nil {
		s := m.EffectiveTo.UTC().Format(time.RFC3339)
		dto.EffectiveTo = &s
	}
	return dto
}
