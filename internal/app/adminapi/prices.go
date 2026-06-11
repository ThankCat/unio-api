package adminapi

import (
	"context"
	"net/http"
	"time"

	"github.com/ThankCat/unio-api/internal/platform/httpx"
	"github.com/ThankCat/unio-api/internal/service/admin/price"
)

// PriceService 定义 adminapi 操作客户售价所需的最小能力。
type PriceService interface {
	List(ctx context.Context, modelID int64) ([]price.Price, error)
	Create(ctx context.Context, in price.CreateInput) (price.Price, error)
	Update(ctx context.Context, in price.UpdateInput) (price.Price, error)
}

// priceDTO 是客户售价的 admin API 响应体。金额用十进制字符串承载，避免 JSON number 精度丢失。
type priceDTO struct {
	ID                     int64   `json:"id"`
	ModelID                int64   `json:"model_id"`
	Currency               string  `json:"currency"`
	PricingUnit            string  `json:"pricing_unit"`
	UncachedInputPrice     string  `json:"uncached_input_price"`
	CacheReadInputPrice    *string `json:"cache_read_input_price"`
	CacheWrite5mInputPrice *string `json:"cache_write_5m_input_price"`
	CacheWrite1hInputPrice *string `json:"cache_write_1h_input_price"`
	OutputPrice            string  `json:"output_price"`
	ReasoningOutputPrice   *string `json:"reasoning_output_price"`
	Status                 string  `json:"status"`
	EffectiveFrom          string  `json:"effective_from"`
	EffectiveTo            *string `json:"effective_to"`
	CreatedAt              string  `json:"created_at"`
	UpdatedAt              string  `json:"updated_at"`
}

type createPriceRequest struct {
	Currency               string  `json:"currency"`
	PricingUnit            string  `json:"pricing_unit"`
	UncachedInputPrice     string  `json:"uncached_input_price"`
	CacheReadInputPrice    *string `json:"cache_read_input_price"`
	CacheWrite5mInputPrice *string `json:"cache_write_5m_input_price"`
	CacheWrite1hInputPrice *string `json:"cache_write_1h_input_price"`
	OutputPrice            string  `json:"output_price"`
	ReasoningOutputPrice   *string `json:"reasoning_output_price"`
	Status                 string  `json:"status"`
	EffectiveFrom          string  `json:"effective_from"`
	EffectiveTo            *string `json:"effective_to"`
}

type updatePriceRequest struct {
	Status      string  `json:"status"`
	EffectiveTo *string `json:"effective_to"`
}

type pricesHandler struct {
	service PriceService
}

func (h *pricesHandler) list(w http.ResponseWriter, r *http.Request) {
	modelID, err := pathID(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	prices, err := h.service.List(r.Context(), modelID)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	dtos := make([]priceDTO, 0, len(prices))
	for _, p := range prices {
		dtos = append(dtos, toPriceDTO(p))
	}

	writeData(w, http.StatusOK, dtos)
}

func (h *pricesHandler) create(w http.ResponseWriter, r *http.Request) {
	modelID, err := pathID(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	var req createPriceRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		writeServiceError(w, err)
		return
	}

	from, err := parseRFC3339("effective_from", req.EffectiveFrom)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	to, err := parseOptionalRFC3339("effective_to", req.EffectiveTo)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	p, err := h.service.Create(r.Context(), price.CreateInput{
		ModelID:                modelID,
		Currency:               req.Currency,
		PricingUnit:            req.PricingUnit,
		UncachedInputPrice:     req.UncachedInputPrice,
		CacheReadInputPrice:    req.CacheReadInputPrice,
		CacheWrite5mInputPrice: req.CacheWrite5mInputPrice,
		CacheWrite1hInputPrice: req.CacheWrite1hInputPrice,
		OutputPrice:            req.OutputPrice,
		ReasoningOutputPrice:   req.ReasoningOutputPrice,
		Status:                 req.Status,
		EffectiveFrom:          from,
		EffectiveTo:            to,
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}

	writeData(w, http.StatusCreated, toPriceDTO(p))
}

func (h *pricesHandler) update(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	var req updatePriceRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		writeServiceError(w, err)
		return
	}

	to, err := parseOptionalRFC3339("effective_to", req.EffectiveTo)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	p, err := h.service.Update(r.Context(), price.UpdateInput{
		ID:          id,
		Status:      req.Status,
		EffectiveTo: to,
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}

	writeData(w, http.StatusOK, toPriceDTO(p))
}

func toPriceDTO(p price.Price) priceDTO {
	dto := priceDTO{
		ID:                     p.ID,
		ModelID:                p.ModelID,
		Currency:               p.Currency,
		PricingUnit:            p.PricingUnit,
		UncachedInputPrice:     p.UncachedInputPrice,
		CacheReadInputPrice:    p.CacheReadInputPrice,
		CacheWrite5mInputPrice: p.CacheWrite5mInputPrice,
		CacheWrite1hInputPrice: p.CacheWrite1hInputPrice,
		OutputPrice:            p.OutputPrice,
		ReasoningOutputPrice:   p.ReasoningOutputPrice,
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
