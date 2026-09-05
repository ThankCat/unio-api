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
	"github.com/ThankCat/unio-gateway/internal/service/admin/supply"
)

// ModelPriceService 定义 adminapi 操作模型定价（model_prices）所需的最小能力（DEC-026）。
// 创建必须带 intent（base / sale_discount / sale_absolute）；草稿行可以只有基准价。
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
	CacheCreation5mInputPrice   *string                `json:"cache_creation_5m_input_price"`
	CacheCreation1hInputPrice   *string                `json:"cache_creation_1h_input_price"`
	CacheCreation30mInputPrice  *string                `json:"cache_creation_30m_input_price"`
	OutputPrice                 string                 `json:"output_price"`
	ReasoningOutputPrice        *string                `json:"reasoning_output_price"`
	SaleDiscount              *string                `json:"sale_discount"`
	SalePrices                  *salePriceVectorDTO    `json:"sale_prices"`
	SaleConfigured              bool                   `json:"sale_configured"`
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
	// Warnings 是不阻断写入、但管理员应当知道的后果，例如窗口到期后无接续售价。
	Warnings []string `json:"warnings,omitempty"`
}

// salePriceVectorDTO 是一组对外绝对售价：整组给齐或整组留空，非空时优先于售价折扣。
type salePriceVectorDTO struct {
	UncachedInputPrice         string  `json:"uncached_input_price"`
	CacheReadInputPrice        *string `json:"cache_read_input_price"`
	CacheCreation5mInputPrice  *string `json:"cache_creation_5m_input_price"`
	CacheCreation1hInputPrice  *string `json:"cache_creation_1h_input_price"`
	CacheCreation30mInputPrice *string `json:"cache_creation_30m_input_price"`
	OutputPrice                string  `json:"output_price"`
	ReasoningOutputPrice       *string `json:"reasoning_output_price"`
}

type fastPriceDTO struct {
	ServiceTierID              int64               `json:"service_tier_id"`
	UncachedInputPrice         string              `json:"uncached_input_price"`
	CacheReadInputPrice        *string             `json:"cache_read_input_price"`
	CacheCreation5mInputPrice  *string             `json:"cache_creation_5m_input_price"`
	CacheCreation1hInputPrice  *string             `json:"cache_creation_1h_input_price"`
	CacheCreation30mInputPrice *string             `json:"cache_creation_30m_input_price"`
	OutputPrice                string              `json:"output_price"`
	ReasoningOutputPrice       *string             `json:"reasoning_output_price"`
	SalePrices                 *salePriceVectorDTO `json:"sale_prices"`
	ReferenceSource            *string             `json:"reference_source"`
	ReferenceCheckedAt         *string             `json:"reference_checked_at"`
}

type fastPriceReferenceDTO struct {
	Currency                   string  `json:"currency"`
	PricingUnit                string  `json:"pricing_unit"`
	UncachedInputPrice         string  `json:"uncached_input_price"`
	CacheReadInputPrice        *string `json:"cache_read_input_price"`
	CacheCreation5mInputPrice  *string `json:"cache_creation_5m_input_price"`
	CacheCreation1hInputPrice  *string `json:"cache_creation_1h_input_price"`
	CacheCreation30mInputPrice *string `json:"cache_creation_30m_input_price"`
	OutputPrice                string  `json:"output_price"`
	ReasoningOutputPrice       *string `json:"reasoning_output_price"`
	Source                     string  `json:"source"`
	CheckedAt                  string  `json:"checked_at"`
}

type fastPriceRequest struct {
	UncachedInputPrice         string              `json:"uncached_input_price"`
	CacheReadInputPrice        *string             `json:"cache_read_input_price"`
	CacheCreation5mInputPrice  *string             `json:"cache_creation_5m_input_price"`
	CacheCreation1hInputPrice  *string             `json:"cache_creation_1h_input_price"`
	CacheCreation30mInputPrice *string             `json:"cache_creation_30m_input_price"`
	OutputPrice                string              `json:"output_price"`
	ReasoningOutputPrice       *string             `json:"reasoning_output_price"`
	SalePrices                 *salePriceVectorDTO `json:"sale_prices"`
	ReferenceSource            *string             `json:"reference_source"`
	ReferenceCheckedAt         *string             `json:"reference_checked_at"`
}

// createModelPriceRequest 必须带 intent：base / sale_discount / sale_absolute。
// 没有 intent 的旧请求返回 400，避免静默按「整行手填」走。
type createModelPriceRequest struct {
	Intent                      string              `json:"intent"`
	Currency                    string              `json:"currency"`
	PricingUnit                 string              `json:"pricing_unit"`
	UncachedInputPrice          string              `json:"uncached_input_price"`
	CacheReadInputPrice         *string             `json:"cache_read_input_price"`
	CacheCreation5mInputPrice   *string             `json:"cache_creation_5m_input_price"`
	CacheCreation1hInputPrice   *string             `json:"cache_creation_1h_input_price"`
	CacheCreation30mInputPrice  *string             `json:"cache_creation_30m_input_price"`
	OutputPrice                 string              `json:"output_price"`
	ReasoningOutputPrice        *string             `json:"reasoning_output_price"`
	SaleDiscount              *string             `json:"sale_discount"`
	SalePrices                  *salePriceVectorDTO `json:"sale_prices"`
	LongContextEnabled          bool                `json:"long_context_enabled"`
	LongContextThreshold        *int64              `json:"long_context_threshold"`
	LongContextInputMultiplier  *string             `json:"long_context_input_multiplier"`
	LongContextOutputMultiplier *string             `json:"long_context_output_multiplier"`
	FastPrices                  *fastPriceRequest   `json:"fast_prices"`
	ReplaceOverlappingEnabled   bool                `json:"replace_overlapping_enabled"`
	Status                      string              `json:"status"`
	EffectiveFrom               string              `json:"effective_from"`
	EffectiveTo                 *string             `json:"effective_to"`
	// 替换窗口若让模型失去可解析售价，需携带影响指纹确认；确认后模型一并下架。
	ConfirmSupplyImpact       bool   `json:"confirm_supply_impact"`
	ExpectedImpactFingerprint string `json:"expected_impact_fingerprint"`
}

type updateModelPriceRequest struct {
	Status      string  `json:"status"`
	EffectiveTo *string `json:"effective_to"`
	// 撤掉模型最后一条可解析售价时需携带影响指纹确认；确认后模型一并下架。
	// 价格侧不提供「保留模型」选项，所以没有 selected_models。
	ConfirmSupplyImpact       bool   `json:"confirm_supply_impact"`
	ExpectedImpactFingerprint string `json:"expected_impact_fingerprint"`
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
		Intent:                      req.Intent,
		Currency:                    req.Currency,
		PricingUnit:                 req.PricingUnit,
		UncachedInputPrice:          req.UncachedInputPrice,
		CacheReadInputPrice:         req.CacheReadInputPrice,
		CacheCreation5mInputPrice:   req.CacheCreation5mInputPrice,
		CacheCreation1hInputPrice:   req.CacheCreation1hInputPrice,
		CacheCreation30mInputPrice:  req.CacheCreation30mInputPrice,
		OutputPrice:                 req.OutputPrice,
		ReasoningOutputPrice:        req.ReasoningOutputPrice,
		SaleDiscount:              req.SaleDiscount,
		SalePrices:                  saleVectorInput(req.SalePrices),
		LongContextEnabled:          req.LongContextEnabled,
		LongContextThreshold:        req.LongContextThreshold,
		LongContextInputMultiplier:  req.LongContextInputMultiplier,
		LongContextOutputMultiplier: req.LongContextOutputMultiplier,
		FastPrices:                  fastPrices,
		ReplaceOverlappingEnabled:   req.ReplaceOverlappingEnabled,
		Status:                      req.Status,
		EffectiveFrom:               from,
		EffectiveTo:                 to,
		Confirmation: supply.Confirmation{
			Confirm:             req.ConfirmSupplyImpact,
			ExpectedFingerprint: req.ExpectedImpactFingerprint,
		},
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
		Confirmation: supply.Confirmation{
			Confirm:             req.ConfirmSupplyImpact,
			ExpectedFingerprint: req.ExpectedImpactFingerprint,
		},
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
		CacheCreation5mInputPrice:   p.CacheCreation5mInputPrice,
		CacheCreation1hInputPrice:   p.CacheCreation1hInputPrice,
		CacheCreation30mInputPrice:  p.CacheCreation30mInputPrice,
		OutputPrice:                 p.OutputPrice,
		ReasoningOutputPrice:        p.ReasoningOutputPrice,
		SaleDiscount:              p.SaleDiscount,
		SalePrices:                  saleVectorDTO(p.SalePrices),
		SaleConfigured:              p.SaleConfigured,
		LongContextEnabled:          p.LongContextEnabled,
		LongContextThreshold:        p.LongContextThreshold,
		LongContextInputMultiplier:  p.LongContextInputMultiplier,
		LongContextOutputMultiplier: p.LongContextOutputMultiplier,
		FastPriceStatus:             p.FastPriceStatus,
		Status:                      p.Status,
		EffectiveFrom:               p.EffectiveFrom.UTC().Format(time.RFC3339),
		CreatedAt:                   p.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:                   p.UpdatedAt.UTC().Format(time.RFC3339),
		Warnings:                    p.Warnings,
	}
	if p.FastPrices != nil {
		fast := fastPriceDTO{
			ServiceTierID:              p.FastPrices.ServiceTierID,
			UncachedInputPrice:         p.FastPrices.UncachedInputPrice,
			CacheReadInputPrice:        p.FastPrices.CacheReadInputPrice,
			CacheCreation5mInputPrice:  p.FastPrices.CacheCreation5mInputPrice,
			CacheCreation1hInputPrice:  p.FastPrices.CacheCreation1hInputPrice,
			CacheCreation30mInputPrice: p.FastPrices.CacheCreation30mInputPrice,
			OutputPrice:                p.FastPrices.OutputPrice,
			ReasoningOutputPrice:       p.FastPrices.ReasoningOutputPrice,
			SalePrices:                 saleVectorDTO(p.FastPrices.SalePrices),
			ReferenceSource:            p.FastPrices.ReferenceSource,
		}
		if p.FastPrices.ReferenceCheckedAt != nil {
			value := p.FastPrices.ReferenceCheckedAt.UTC().Format(time.DateOnly)
			fast.ReferenceCheckedAt = &value
		}
		dto.FastPrices = &fast
	}
	if p.FastPriceReference != nil {
		dto.FastPriceReference = &fastPriceReferenceDTO{
			Currency:                   p.FastPriceReference.Currency,
			PricingUnit:                p.FastPriceReference.PricingUnit,
			UncachedInputPrice:         p.FastPriceReference.UncachedInputPrice,
			CacheReadInputPrice:        p.FastPriceReference.CacheReadInputPrice,
			CacheCreation5mInputPrice:  p.FastPriceReference.CacheCreation5mInputPrice,
			CacheCreation1hInputPrice:  p.FastPriceReference.CacheCreation1hInputPrice,
			CacheCreation30mInputPrice: p.FastPriceReference.CacheCreation30mInputPrice,
			OutputPrice:                p.FastPriceReference.OutputPrice,
			ReasoningOutputPrice:       p.FastPriceReference.ReasoningOutputPrice,
			Source:                     p.FastPriceReference.Source,
			CheckedAt:                  p.FastPriceReference.CheckedAt.UTC().Format(time.DateOnly),
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
		UncachedInputPrice:         req.UncachedInputPrice,
		CacheReadInputPrice:        req.CacheReadInputPrice,
		CacheCreation5mInputPrice:  req.CacheCreation5mInputPrice,
		CacheCreation1hInputPrice:  req.CacheCreation1hInputPrice,
		CacheCreation30mInputPrice: req.CacheCreation30mInputPrice,
		OutputPrice:                req.OutputPrice,
		ReasoningOutputPrice:       req.ReasoningOutputPrice,
		SalePrices:                 saleVectorInput(req.SalePrices),
		ReferenceSource:            req.ReferenceSource,
		ReferenceCheckedAt:         checkedAt,
	}, nil
}

func saleVectorInput(in *salePriceVectorDTO) *modelprice.SalePriceVector {
	if in == nil {
		return nil
	}
	return &modelprice.SalePriceVector{
		UncachedInputPrice:         in.UncachedInputPrice,
		CacheReadInputPrice:        in.CacheReadInputPrice,
		CacheCreation5mInputPrice:  in.CacheCreation5mInputPrice,
		CacheCreation1hInputPrice:  in.CacheCreation1hInputPrice,
		CacheCreation30mInputPrice: in.CacheCreation30mInputPrice,
		OutputPrice:                in.OutputPrice,
		ReasoningOutputPrice:       in.ReasoningOutputPrice,
	}
}

func saleVectorDTO(in *modelprice.SalePriceVector) *salePriceVectorDTO {
	if in == nil {
		return nil
	}
	return &salePriceVectorDTO{
		UncachedInputPrice:         in.UncachedInputPrice,
		CacheReadInputPrice:        in.CacheReadInputPrice,
		CacheCreation5mInputPrice:  in.CacheCreation5mInputPrice,
		CacheCreation1hInputPrice:  in.CacheCreation1hInputPrice,
		CacheCreation30mInputPrice: in.CacheCreation30mInputPrice,
		OutputPrice:                in.OutputPrice,
		ReasoningOutputPrice:       in.ReasoningOutputPrice,
	}
}
