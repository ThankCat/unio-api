package channel

import (
	"context"
	"net/http"
	"time"

	"github.com/ThankCat/unio-api/internal/app/adminapi/adminhttp"

	"github.com/ThankCat/unio-api/internal/platform/httpx"
	"github.com/ThankCat/unio-api/internal/service/admin/channelrechargefactor"
)

// ChannelRechargeFactorService 定义 adminapi 操作渠道充值倍率（channel_recharge_factors）所需的最小能力（DEC-027）。
type ChannelRechargeFactorService interface {
	List(ctx context.Context, channelID int64) ([]channelrechargefactor.ChannelRechargeFactor, error)
	Create(ctx context.Context, in channelrechargefactor.CreateInput) (channelrechargefactor.ChannelRechargeFactor, error)
	Update(ctx context.Context, in channelrechargefactor.UpdateInput) (channelrechargefactor.ChannelRechargeFactor, error)
}

// channelRechargeFactorDTO 是渠道充值倍率的 admin API 响应体。factor = 每 1 单位上游名义额度折合多少结算币种真实钱。
type channelRechargeFactorDTO struct {
	ID            int64   `json:"id"`
	ChannelID     int64   `json:"channel_id"`
	Factor        string  `json:"factor"`
	Status        string  `json:"status"`
	EffectiveFrom string  `json:"effective_from"`
	EffectiveTo   *string `json:"effective_to"`
	CreatedAt     string  `json:"created_at"`
	UpdatedAt     string  `json:"updated_at"`
}

type createChannelRechargeFactorRequest struct {
	Factor        string  `json:"factor"`
	Status        string  `json:"status"`
	EffectiveFrom string  `json:"effective_from"`
	EffectiveTo   *string `json:"effective_to"`
}

type updateChannelRechargeFactorRequest struct {
	Status      string  `json:"status"`
	EffectiveTo *string `json:"effective_to"`
}

type channelRechargeFactorsHandler struct {
	service ChannelRechargeFactorService
}

func (h *channelRechargeFactorsHandler) list(w http.ResponseWriter, r *http.Request) {
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

	dtos := make([]channelRechargeFactorDTO, 0, len(items))
	for _, f := range items {
		dtos = append(dtos, toChannelRechargeFactorDTO(f))
	}

	adminhttp.WriteData(w, http.StatusOK, dtos)
}

func (h *channelRechargeFactorsHandler) create(w http.ResponseWriter, r *http.Request) {
	channelID, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	var req createChannelRechargeFactorRequest
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

	f, err := h.service.Create(r.Context(), channelrechargefactor.CreateInput{
		ChannelID:     channelID,
		Factor:        req.Factor,
		Status:        req.Status,
		EffectiveFrom: from,
		EffectiveTo:   to,
	})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	adminhttp.WriteData(w, http.StatusCreated, toChannelRechargeFactorDTO(f))
}

func (h *channelRechargeFactorsHandler) update(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	var req updateChannelRechargeFactorRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	to, err := adminhttp.ParseOptionalRFC3339("effective_to", req.EffectiveTo)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	f, err := h.service.Update(r.Context(), channelrechargefactor.UpdateInput{
		ID:          id,
		Status:      req.Status,
		EffectiveTo: to,
	})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	adminhttp.WriteData(w, http.StatusOK, toChannelRechargeFactorDTO(f))
}

func toChannelRechargeFactorDTO(f channelrechargefactor.ChannelRechargeFactor) channelRechargeFactorDTO {
	dto := channelRechargeFactorDTO{
		ID:            f.ID,
		ChannelID:     f.ChannelID,
		Factor:        f.Factor,
		Status:        f.Status,
		EffectiveFrom: f.EffectiveFrom.UTC().Format(time.RFC3339),
		CreatedAt:     f.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:     f.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if f.EffectiveTo != nil {
		s := f.EffectiveTo.UTC().Format(time.RFC3339)
		dto.EffectiveTo = &s
	}
	return dto
}
