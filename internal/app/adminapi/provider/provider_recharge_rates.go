package provider

import (
	"context"
	"net/http"
	"time"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi/adminhttp"
	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
	"github.com/ThankCat/unio-gateway/internal/service/admin/providerrechargerate"
)

// ProviderRechargeRateService 定义 adminapi 操作服务商充值汇率（provider_recharge_rates）所需的最小能力。
type ProviderRechargeRateService interface {
	List(ctx context.Context, providerID int64) ([]providerrechargerate.ProviderRechargeRate, error)
	Create(ctx context.Context, in providerrechargerate.CreateInput) (providerrechargerate.ProviderRechargeRate, error)
	Update(ctx context.Context, in providerrechargerate.UpdateInput) (providerrechargerate.ProviderRechargeRate, error)
}

// providerRechargeRateDTO 是服务商充值汇率的 admin API 响应体。
// rate = 实际支付的服务商币种金额 ÷ 到账的 USD 名义额度（provider_currency / USD）。
type providerRechargeRateDTO struct {
	ID               int64   `json:"id"`
	ProviderID       int64   `json:"provider_id"`
	ProviderCurrency string  `json:"provider_currency"`
	NominalCurrency  string  `json:"nominal_currency"`
	Rate             string  `json:"rate"`
	Status           string  `json:"status"`
	Source           string  `json:"source"`
	Reason           string  `json:"reason"`
	CreatedBy        string  `json:"created_by"`
	EffectiveFrom    string  `json:"effective_from"`
	EffectiveTo      *string `json:"effective_to"`
	CreatedAt        string  `json:"created_at"`
	UpdatedAt        string  `json:"updated_at"`
}

type createProviderRechargeRateRequest struct {
	Rate          string  `json:"rate"`
	Status        string  `json:"status"`
	Source        string  `json:"source"`
	Reason        string  `json:"reason"`
	EffectiveFrom string  `json:"effective_from"`
	EffectiveTo   *string `json:"effective_to"`
}

type updateProviderRechargeRateRequest struct {
	Status      string  `json:"status"`
	EffectiveTo *string `json:"effective_to"`
}

type providerRechargeRatesHandler struct {
	service ProviderRechargeRateService
}

func (h *providerRechargeRatesHandler) list(w http.ResponseWriter, r *http.Request) {
	providerID, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	items, err := h.service.List(r.Context(), providerID)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	dtos := make([]providerRechargeRateDTO, 0, len(items))
	for _, item := range items {
		dtos = append(dtos, toProviderRechargeRateDTO(item))
	}

	adminhttp.WriteData(w, http.StatusOK, dtos)
}

func (h *providerRechargeRatesHandler) create(w http.ResponseWriter, r *http.Request) {
	providerID, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	var req createProviderRechargeRateRequest
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

	rate, err := h.service.Create(r.Context(), providerrechargerate.CreateInput{
		ProviderID:    providerID,
		Rate:          req.Rate,
		Status:        req.Status,
		Source:        req.Source,
		Reason:        req.Reason,
		EffectiveFrom: from,
		EffectiveTo:   to,
	})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	adminhttp.WriteData(w, http.StatusCreated, toProviderRechargeRateDTO(rate))
}

func (h *providerRechargeRatesHandler) update(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	var req updateProviderRechargeRateRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	to, err := adminhttp.ParseOptionalRFC3339("effective_to", req.EffectiveTo)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	rate, err := h.service.Update(r.Context(), providerrechargerate.UpdateInput{
		ID:          id,
		Status:      req.Status,
		EffectiveTo: to,
	})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	adminhttp.WriteData(w, http.StatusOK, toProviderRechargeRateDTO(rate))
}

func toProviderRechargeRateDTO(r providerrechargerate.ProviderRechargeRate) providerRechargeRateDTO {
	dto := providerRechargeRateDTO{
		ID:               r.ID,
		ProviderID:       r.ProviderID,
		ProviderCurrency: r.ProviderCurrency,
		NominalCurrency:  r.NominalCurrency,
		Rate:             r.Rate,
		Status:           r.Status,
		Source:           r.Source,
		Reason:           r.Reason,
		CreatedBy:        r.CreatedBy,
		EffectiveFrom:    r.EffectiveFrom.UTC().Format(time.RFC3339),
		CreatedAt:        r.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:        r.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if r.EffectiveTo != nil {
		s := r.EffectiveTo.UTC().Format(time.RFC3339)
		dto.EffectiveTo = &s
	}
	return dto
}
