package model

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi/adminhttp"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
	"github.com/ThankCat/unio-gateway/internal/service/admin/modelprice"
)

// ModelPriceService 定义 adminapi 操作模型基准售价（model_prices）所需的最小能力（DEC-026）。
// 客户最终售价 = 模型基准价 × 线路倍率；此处只管基准售价（无成本，成本在渠道侧）。
type ModelPriceService interface {
	List(ctx context.Context, modelID int64) ([]modelprice.ModelPrice, error)
	Create(ctx context.Context, in modelprice.CreateInput) (modelprice.ModelPrice, error)
	Update(ctx context.Context, in modelprice.UpdateInput) (modelprice.ModelPrice, error)
}

// modelPriceDTO 是模型基准售价的 admin API 响应体。金额用十进制字符串承载，避免 JSON number 精度丢失。
// uncached_input_price/output_price 必填恒有值；其余可空（*string，未配置时为 null）。
// model_external_id / model_display_name 仅列表场景有值；单条写入返回为空。
type modelPriceDTO struct {
	ID                          int64                  `json:"id"`
	ModelID                     int64                  `json:"model_id"`
	ModelExternalID             string                 `json:"model_external_id"`
	ModelDisplayName            string                 `json:"model_display_name"`
	Currency                    string                 `json:"currency"`
	PricingUnit                 string                 `json:"pricing_unit"`
	UncachedInputPrice          string                 `json:"uncached_input_price"`
	CacheReadInputPrice         *string                `json:"cache_read_input_price"`
	CacheWrite5mInputPrice      *string                `json:"cache_write_5m_input_price"`
	CacheWrite1hInputPrice      *string                `json:"cache_write_1h_input_price"`
	CacheWrite30mInputPrice     *string                `json:"cache_write_30m_input_price"`
	OutputPrice                 string                 `json:"output_price"`
	ReasoningOutputPrice        *string                `json:"reasoning_output_price"`
	LongContextEnabled          bool                   `json:"long_context_enabled"`
	LongContextThreshold        *int64                 `json:"long_context_threshold"`
	LongContextInputMultiplier  *string                `json:"long_context_input_multiplier"`
	LongContextOutputMultiplier *string                `json:"long_context_output_multiplier"`
	FastPriceStatus             string                 `json:"fast_price_status"`
	FastPrices                  *fastPriceDTO          `json:"fast_prices"`
	FastPriceReference          *fastPriceReferenceDTO `json:"fast_price_reference"`
	Status                      string                 `json:"status"`
	EffectiveFrom               string                 `json:"effective_from"`
	EffectiveTo                 *string                `json:"effective_to"`
	CreatedAt                   string                 `json:"created_at"`
	UpdatedAt                   string                 `json:"updated_at"`
}

type fastPriceDTO struct {
	ServiceTierID           int64   `json:"service_tier_id"`
	UncachedInputPrice      string  `json:"uncached_input_price"`
	CacheReadInputPrice     *string `json:"cache_read_input_price"`
	CacheWrite5mInputPrice  *string `json:"cache_write_5m_input_price"`
	CacheWrite1hInputPrice  *string `json:"cache_write_1h_input_price"`
	CacheWrite30mInputPrice *string `json:"cache_write_30m_input_price"`
	OutputPrice             string  `json:"output_price"`
	ReasoningOutputPrice    *string `json:"reasoning_output_price"`
	ReferenceSource         *string `json:"reference_source"`
	ReferenceCheckedAt      *string `json:"reference_checked_at"`
}

type fastPriceReferenceDTO struct {
	Currency                string  `json:"currency"`
	PricingUnit             string  `json:"pricing_unit"`
	UncachedInputPrice      string  `json:"uncached_input_price"`
	CacheReadInputPrice     *string `json:"cache_read_input_price"`
	CacheWrite5mInputPrice  *string `json:"cache_write_5m_input_price"`
	CacheWrite1hInputPrice  *string `json:"cache_write_1h_input_price"`
	CacheWrite30mInputPrice *string `json:"cache_write_30m_input_price"`
	OutputPrice             string  `json:"output_price"`
	ReasoningOutputPrice    *string `json:"reasoning_output_price"`
	Source                  string  `json:"source"`
	CheckedAt               string  `json:"checked_at"`
}

type fastPriceRequest struct {
	UncachedInputPrice      string  `json:"uncached_input_price"`
	CacheReadInputPrice     *string `json:"cache_read_input_price"`
	CacheWrite5mInputPrice  *string `json:"cache_write_5m_input_price"`
	CacheWrite1hInputPrice  *string `json:"cache_write_1h_input_price"`
	CacheWrite30mInputPrice *string `json:"cache_write_30m_input_price"`
	OutputPrice             string  `json:"output_price"`
	ReasoningOutputPrice    *string `json:"reasoning_output_price"`
	ReferenceSource         *string `json:"reference_source"`
	ReferenceCheckedAt      *string `json:"reference_checked_at"`
}

type createModelPriceRequest struct {
	Currency                    string            `json:"currency"`
	PricingUnit                 string            `json:"pricing_unit"`
	UncachedInputPrice          string            `json:"uncached_input_price"`
	CacheReadInputPrice         *string           `json:"cache_read_input_price"`
	CacheWrite5mInputPrice      *string           `json:"cache_write_5m_input_price"`
	CacheWrite1hInputPrice      *string           `json:"cache_write_1h_input_price"`
	CacheWrite30mInputPrice     *string           `json:"cache_write_30m_input_price"`
	OutputPrice                 string            `json:"output_price"`
	ReasoningOutputPrice        *string           `json:"reasoning_output_price"`
	LongContextEnabled          bool              `json:"long_context_enabled"`
	LongContextThreshold        *int64            `json:"long_context_threshold"`
	LongContextInputMultiplier  *string           `json:"long_context_input_multiplier"`
	LongContextOutputMultiplier *string           `json:"long_context_output_multiplier"`
	FastPrices                  *fastPriceRequest `json:"fast_prices"`
	Status                      string            `json:"status"`
	EffectiveFrom               string            `json:"effective_from"`
	EffectiveTo                 *string           `json:"effective_to"`
}

type updateModelPriceRequest struct {
	Status      string  `json:"status"`
	EffectiveTo *string `json:"effective_to"`
}

type modelPricesHandler struct {
	service ModelPriceService
}

func (h *modelPricesHandler) list(w http.ResponseWriter, r *http.Request) {
	modelID, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	prices, err := h.service.List(r.Context(), modelID)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	dtos := make([]modelPriceDTO, 0, len(prices))
	for _, p := range prices {
		dtos = append(dtos, toModelPriceDTO(p))
	}

	adminhttp.WriteData(w, http.StatusOK, dtos)
}

func (h *modelPricesHandler) create(w http.ResponseWriter, r *http.Request) {
	modelID, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	var req createModelPriceRequest
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
	fastPrices, err := parseFastPriceRequest(req.FastPrices)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	p, err := h.service.Create(r.Context(), modelprice.CreateInput{
		ModelID:                     modelID,
		Currency:                    req.Currency,
		PricingUnit:                 req.PricingUnit,
		UncachedInputPrice:          req.UncachedInputPrice,
		CacheReadInputPrice:         req.CacheReadInputPrice,
		CacheWrite5mInputPrice:      req.CacheWrite5mInputPrice,
		CacheWrite1hInputPrice:      req.CacheWrite1hInputPrice,
		CacheWrite30mInputPrice:     req.CacheWrite30mInputPrice,
		OutputPrice:                 req.OutputPrice,
		ReasoningOutputPrice:        req.ReasoningOutputPrice,
		LongContextEnabled:          req.LongContextEnabled,
		LongContextThreshold:        req.LongContextThreshold,
		LongContextInputMultiplier:  req.LongContextInputMultiplier,
		LongContextOutputMultiplier: req.LongContextOutputMultiplier,
		FastPrices:                  fastPrices,
		Status:                      req.Status,
		EffectiveFrom:               from,
		EffectiveTo:                 to,
	})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	adminhttp.WriteData(w, http.StatusCreated, toModelPriceDTO(p))
}

func (h *modelPricesHandler) update(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	var req updateModelPriceRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	to, err := adminhttp.ParseOptionalRFC3339("effective_to", req.EffectiveTo)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	p, err := h.service.Update(r.Context(), modelprice.UpdateInput{
		ID:          id,
		Status:      req.Status,
		EffectiveTo: to,
	})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	adminhttp.WriteData(w, http.StatusOK, toModelPriceDTO(p))
}

func toModelPriceDTO(p modelprice.ModelPrice) modelPriceDTO {
	dto := modelPriceDTO{
		ID:                          p.ID,
		ModelID:                     p.ModelID,
		ModelExternalID:             p.ModelExternalID,
		ModelDisplayName:            p.ModelDisplayName,
		Currency:                    p.Currency,
		PricingUnit:                 p.PricingUnit,
		UncachedInputPrice:          p.UncachedInputPrice,
		CacheReadInputPrice:         p.CacheReadInputPrice,
		CacheWrite5mInputPrice:      p.CacheWrite5mInputPrice,
		CacheWrite1hInputPrice:      p.CacheWrite1hInputPrice,
		CacheWrite30mInputPrice:     p.CacheWrite30mInputPrice,
		OutputPrice:                 p.OutputPrice,
		ReasoningOutputPrice:        p.ReasoningOutputPrice,
		LongContextEnabled:          p.LongContextEnabled,
		LongContextThreshold:        p.LongContextThreshold,
		LongContextInputMultiplier:  p.LongContextInputMultiplier,
		LongContextOutputMultiplier: p.LongContextOutputMultiplier,
		FastPriceStatus:             p.FastPriceStatus,
		Status:                      p.Status,
		EffectiveFrom:               p.EffectiveFrom.UTC().Format(time.RFC3339),
		CreatedAt:                   p.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:                   p.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if p.FastPrices != nil {
		fast := fastPriceDTO{
			ServiceTierID:           p.FastPrices.ServiceTierID,
			UncachedInputPrice:      p.FastPrices.UncachedInputPrice,
			CacheReadInputPrice:     p.FastPrices.CacheReadInputPrice,
			CacheWrite5mInputPrice:  p.FastPrices.CacheWrite5mInputPrice,
			CacheWrite1hInputPrice:  p.FastPrices.CacheWrite1hInputPrice,
			CacheWrite30mInputPrice: p.FastPrices.CacheWrite30mInputPrice,
			OutputPrice:             p.FastPrices.OutputPrice,
			ReasoningOutputPrice:    p.FastPrices.ReasoningOutputPrice,
			ReferenceSource:         p.FastPrices.ReferenceSource,
		}
		if p.FastPrices.ReferenceCheckedAt != nil {
			value := p.FastPrices.ReferenceCheckedAt.UTC().Format(time.DateOnly)
			fast.ReferenceCheckedAt = &value
		}
		dto.FastPrices = &fast
	}
	if p.FastPriceReference != nil {
		dto.FastPriceReference = &fastPriceReferenceDTO{
			Currency:                p.FastPriceReference.Currency,
			PricingUnit:             p.FastPriceReference.PricingUnit,
			UncachedInputPrice:      p.FastPriceReference.UncachedInputPrice,
			CacheReadInputPrice:     p.FastPriceReference.CacheReadInputPrice,
			CacheWrite5mInputPrice:  p.FastPriceReference.CacheWrite5mInputPrice,
			CacheWrite1hInputPrice:  p.FastPriceReference.CacheWrite1hInputPrice,
			CacheWrite30mInputPrice: p.FastPriceReference.CacheWrite30mInputPrice,
			OutputPrice:             p.FastPriceReference.OutputPrice,
			ReasoningOutputPrice:    p.FastPriceReference.ReasoningOutputPrice,
			Source:                  p.FastPriceReference.Source,
			CheckedAt:               p.FastPriceReference.CheckedAt.UTC().Format(time.DateOnly),
		}
	}
	if p.EffectiveTo != nil {
		s := p.EffectiveTo.UTC().Format(time.RFC3339)
		dto.EffectiveTo = &s
	}
	return dto
}

func parseFastPriceRequest(req *fastPriceRequest) (*modelprice.FastPriceInput, error) {
	if req == nil {
		return nil, nil
	}
	var checkedAt *time.Time
	if req.ReferenceCheckedAt != nil && strings.TrimSpace(*req.ReferenceCheckedAt) != "" {
		parsed, err := time.Parse(time.DateOnly, strings.TrimSpace(*req.ReferenceCheckedAt))
		if err != nil {
			return nil, failure.New(
				failure.CodeAdminInvalidArgument,
				failure.WithMessage("must be YYYY-MM-DD"),
				failure.WithField("field", "fast_prices.reference_checked_at"),
			)
		}
		checkedAt = &parsed
	}
	return &modelprice.FastPriceInput{
		UncachedInputPrice:      req.UncachedInputPrice,
		CacheReadInputPrice:     req.CacheReadInputPrice,
		CacheWrite5mInputPrice:  req.CacheWrite5mInputPrice,
		CacheWrite1hInputPrice:  req.CacheWrite1hInputPrice,
		CacheWrite30mInputPrice: req.CacheWrite30mInputPrice,
		OutputPrice:             req.OutputPrice,
		ReasoningOutputPrice:    req.ReasoningOutputPrice,
		ReferenceSource:         req.ReferenceSource,
		ReferenceCheckedAt:      checkedAt,
	}, nil
}
