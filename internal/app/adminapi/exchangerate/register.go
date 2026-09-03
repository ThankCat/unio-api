// Package exchangerate 提供 Admin 汇率管理 HTTP 端点：最新/历史查询、手工录入、API Key 验证。
package exchangerate

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi/adminhttp"
	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
	adminexchangerate "github.com/ThankCat/unio-gateway/internal/service/admin/exchangerate"
)

// ExchangeRateService 定义汇率管理台所需能力。
type ExchangeRateService interface {
	Latest(context.Context) ([]adminexchangerate.LatestRate, error)
	List(context.Context, adminexchangerate.ListParams) ([]adminexchangerate.Rate, int64, error)
	CreateManual(context.Context, adminexchangerate.CreateManualParams) (adminexchangerate.Rate, error)
	ValidateKey(context.Context, string) (adminexchangerate.ValidateKeyResult, error)
}

// Deps 是汇率模块的路由依赖。
type Deps struct {
	Service ExchangeRateService
}

// Register 注册汇率管理路由（静态段在前，纯可读性习惯）。
func Register(r chi.Router, d Deps) {
	if d.Service == nil {
		return
	}
	h := &ratesHandler{service: d.Service}
	r.Get("/exchange-rates/latest", h.latest)
	r.Post("/exchange-rates/validate-key", h.validateKey)
	r.Get("/exchange-rates", h.list)
	r.Post("/exchange-rates", h.createManual)
}

type rateDTO struct {
	ID            int64  `json:"id"`
	BaseCurrency  string `json:"base_currency"`
	QuoteCurrency string `json:"quote_currency"`
	Rate          string `json:"rate"`
	RateDate      string `json:"rate_date"`
	Source        string `json:"source"`
	FetchedAt     string `json:"fetched_at"`
}

type latestRateDTO struct {
	rateDTO
	Found    bool    `json:"found"`
	AgeHours float64 `json:"age_hours"`
}

type createManualRateRequest struct {
	QuoteCurrency string `json:"quote_currency"`
	Rate          string `json:"rate"`
	RateDate      string `json:"rate_date"`
}

type validateKeyRequest struct {
	APIKey string `json:"api_key"`
}

type validateKeyResultDTO struct {
	QuoteCurrency string `json:"quote_currency"`
	Rate          string `json:"rate"`
	RateDate      string `json:"rate_date"`
	Source        string `json:"source"`
}

type ratesHandler struct {
	service ExchangeRateService
}

func (h *ratesHandler) latest(w http.ResponseWriter, r *http.Request) {
	items, err := h.service.Latest(r.Context())
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	out := make([]latestRateDTO, 0, len(items))
	for _, item := range items {
		out = append(out, latestRateDTO{rateDTO: rateDTOFrom(item.Rate), Found: item.Found, AgeHours: item.AgeHours})
	}
	adminhttp.WriteData(w, http.StatusOK, out)
}

func (h *ratesHandler) list(w http.ResponseWriter, r *http.Request) {
	page := adminhttp.ParsePage(r)
	items, total, err := h.service.List(r.Context(), adminexchangerate.ListParams{
		Quote:  adminhttp.QueryString(r, "quote"),
		Limit:  page.Limit(),
		Offset: page.Offset(),
	})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	out := make([]rateDTO, 0, len(items))
	for _, item := range items {
		out = append(out, rateDTOFrom(item))
	}
	adminhttp.WriteList(w, http.StatusOK, out, page, total)
}

func (h *ratesHandler) createManual(w http.ResponseWriter, r *http.Request) {
	var body createManualRateRequest
	if err := httpx.DecodeJSON(w, r, &body); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	params := adminexchangerate.CreateManualParams{Quote: body.QuoteCurrency, Rate: body.Rate}
	if body.RateDate != "" {
		rateDate, err := time.Parse("2006-01-02", body.RateDate)
		if err != nil {
			adminhttp.WriteServiceError(w, adminhttp.InvalidRequestField("rate_date", "rate_date must be YYYY-MM-DD"))
			return
		}
		params.RateDate = rateDate
	}
	result, err := h.service.CreateManual(r.Context(), params)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusCreated, rateDTOFrom(result))
}

func (h *ratesHandler) validateKey(w http.ResponseWriter, r *http.Request) {
	var body validateKeyRequest
	if err := httpx.DecodeJSON(w, r, &body); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	result, err := h.service.ValidateKey(r.Context(), body.APIKey)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, validateKeyResultDTO{
		QuoteCurrency: result.Quote,
		Rate:          result.Rate,
		RateDate:      result.RateDate.UTC().Format("2006-01-02"),
		Source:        result.Source,
	})
}

func rateDTOFrom(rate adminexchangerate.Rate) rateDTO {
	return rateDTO{
		ID:            rate.ID,
		BaseCurrency:  rate.BaseCurrency,
		QuoteCurrency: rate.QuoteCurrency,
		Rate:          rate.Rate,
		RateDate:      rate.RateDate.UTC().Format("2006-01-02"),
		Source:        rate.Source,
		FetchedAt:     rate.FetchedAt.UTC().Format(time.RFC3339),
	}
}
