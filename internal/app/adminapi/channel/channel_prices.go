package channel

import (
	"context"
	"net/http"
	"time"

	"github.com/ThankCat/unio-api/internal/app/adminapi/adminhttp"

	"github.com/ThankCat/unio-api/internal/platform/httpx"
	"github.com/ThankCat/unio-api/internal/service/admin/channelprice"
)

// ChannelPriceService 定义 adminapi 操作渠道-模型成本价所需的最小能力（DEC-026：渠道只录成本）。
type ChannelPriceService interface {
	List(ctx context.Context, channelID int64) ([]channelprice.ChannelPrice, error)
	Create(ctx context.Context, in channelprice.CreateInput) (channelprice.ChannelPrice, error)
	Update(ctx context.Context, in channelprice.UpdateInput) (channelprice.ChannelPrice, error)
}

// channelPriceDTO 是渠道-模型成本价的 admin API 响应体。金额用十进制字符串承载，避免 JSON number 精度丢失。
// DEC-026：渠道只录成本（客户售价 = 模型基准价 × 线路倍率）。主成本必填（恒有值）；其余分项成本可空（*string）。
// model_external_id / model_display_name 仅列表场景有值；单条写入返回为空。
type channelPriceDTO struct {
	ID                     int64   `json:"id"`
	ChannelID              int64   `json:"channel_id"`
	ModelID                int64   `json:"model_id"`
	ModelExternalID        string  `json:"model_external_id"`
	ModelDisplayName       string  `json:"model_display_name"`
	Currency               string  `json:"currency"`
	PricingUnit            string  `json:"pricing_unit"`
	UncachedInputCost      string  `json:"uncached_input_cost"`
	CacheReadInputCost     *string `json:"cache_read_input_cost"`
	CacheWrite5mInputCost  *string `json:"cache_write_5m_input_cost"`
	CacheWrite1hInputCost  *string `json:"cache_write_1h_input_cost"`
	CacheWrite30mInputCost *string `json:"cache_write_30m_input_cost"`
	OutputCost             string  `json:"output_cost"`
	ReasoningOutputCost    *string `json:"reasoning_output_cost"`
	Status                 string  `json:"status"`
	EffectiveFrom          string  `json:"effective_from"`
	EffectiveTo            *string `json:"effective_to"`
	CreatedAt              string  `json:"created_at"`
	UpdatedAt              string  `json:"updated_at"`
}

type createChannelPriceRequest struct {
	ModelID                int64   `json:"model_id"`
	Currency               string  `json:"currency"`
	PricingUnit            string  `json:"pricing_unit"`
	UncachedInputCost      string  `json:"uncached_input_cost"`
	CacheReadInputCost     *string `json:"cache_read_input_cost"`
	CacheWrite5mInputCost  *string `json:"cache_write_5m_input_cost"`
	CacheWrite1hInputCost  *string `json:"cache_write_1h_input_cost"`
	CacheWrite30mInputCost *string `json:"cache_write_30m_input_cost"`
	OutputCost             string  `json:"output_cost"`
	ReasoningOutputCost    *string `json:"reasoning_output_cost"`
	Status                 string  `json:"status"`
	EffectiveFrom          string  `json:"effective_from"`
	EffectiveTo            *string `json:"effective_to"`
}

type updateChannelPriceRequest struct {
	Status      string  `json:"status"`
	EffectiveTo *string `json:"effective_to"`
}

type channelPricesHandler struct {
	service ChannelPriceService
}

func (h *channelPricesHandler) list(w http.ResponseWriter, r *http.Request) {
	channelID, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	prices, err := h.service.List(r.Context(), channelID)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	dtos := make([]channelPriceDTO, 0, len(prices))
	for _, p := range prices {
		dtos = append(dtos, toChannelPriceDTO(p))
	}

	adminhttp.WriteData(w, http.StatusOK, dtos)
}

func (h *channelPricesHandler) create(w http.ResponseWriter, r *http.Request) {
	channelID, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	modelID, err := pathModelID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	var req createChannelPriceRequest
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

	p, err := h.service.Create(r.Context(), channelprice.CreateInput{
		ChannelID:              channelID,
		ModelID:                modelID,
		Currency:               req.Currency,
		PricingUnit:            req.PricingUnit,
		UncachedInputCost:      req.UncachedInputCost,
		CacheReadInputCost:     req.CacheReadInputCost,
		CacheWrite5mInputCost:  req.CacheWrite5mInputCost,
		CacheWrite1hInputCost:  req.CacheWrite1hInputCost,
		CacheWrite30mInputCost: req.CacheWrite30mInputCost,
		OutputCost:             req.OutputCost,
		ReasoningOutputCost:    req.ReasoningOutputCost,
		Status:                 req.Status,
		EffectiveFrom:          from,
		EffectiveTo:            to,
	})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	adminhttp.WriteData(w, http.StatusCreated, toChannelPriceDTO(p))
}

func (h *channelPricesHandler) update(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	var req updateChannelPriceRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	to, err := adminhttp.ParseOptionalRFC3339("effective_to", req.EffectiveTo)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	p, err := h.service.Update(r.Context(), channelprice.UpdateInput{
		ID:          id,
		Status:      req.Status,
		EffectiveTo: to,
	})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	adminhttp.WriteData(w, http.StatusOK, toChannelPriceDTO(p))
}

func toChannelPriceDTO(p channelprice.ChannelPrice) channelPriceDTO {
	dto := channelPriceDTO{
		ID:                     p.ID,
		ChannelID:              p.ChannelID,
		ModelID:                p.ModelID,
		ModelExternalID:        p.ModelExternalID,
		ModelDisplayName:       p.ModelDisplayName,
		Currency:               p.Currency,
		PricingUnit:            p.PricingUnit,
		UncachedInputCost:      p.UncachedInputCost,
		CacheReadInputCost:     p.CacheReadInputCost,
		CacheWrite5mInputCost:  p.CacheWrite5mInputCost,
		CacheWrite1hInputCost:  p.CacheWrite1hInputCost,
		CacheWrite30mInputCost: p.CacheWrite30mInputCost,
		OutputCost:             p.OutputCost,
		ReasoningOutputCost:    p.ReasoningOutputCost,
		Status:                 p.Status,
		EffectiveFrom:          p.EffectiveFrom.UTC().Format(time.RFC3339),
		CreatedAt:              p.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:              p.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if p.EffectiveTo != nil {
		s := p.EffectiveTo.UTC().Format(time.RFC3339)
		dto.EffectiveTo = &s
	}
	return dto
}
