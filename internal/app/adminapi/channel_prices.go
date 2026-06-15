package adminapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/httpx"
	"github.com/ThankCat/unio-api/internal/service/admin/channelprice"
)

// ChannelPriceService 定义 adminapi 操作渠道-模型价（售价 + 成本合并）所需的最小能力（阶段 15）。
type ChannelPriceService interface {
	List(ctx context.Context, channelID int64) ([]channelprice.ChannelPrice, error)
	Create(ctx context.Context, in channelprice.CreateInput) (channelprice.ChannelPrice, error)
	Update(ctx context.Context, in channelprice.UpdateInput) (channelprice.ChannelPrice, error)
}

// channelPriceDTO 是渠道-模型价的 admin API 响应体。金额用十进制字符串承载，避免 JSON number 精度丢失。
// 售价必填（uncached_input_price/output_price 恒有值）；成本可空（*string，未配置时为 null）。
// model_external_id / model_display_name 仅列表场景有值；单条写入返回为空。
type channelPriceDTO struct {
	ID                     int64   `json:"id"`
	ChannelID              int64   `json:"channel_id"`
	ModelID                int64   `json:"model_id"`
	ModelExternalID        string  `json:"model_external_id"`
	ModelDisplayName       string  `json:"model_display_name"`
	Currency               string  `json:"currency"`
	PricingUnit            string  `json:"pricing_unit"`
	UncachedInputPrice     string  `json:"uncached_input_price"`
	CacheReadInputPrice    *string `json:"cache_read_input_price"`
	CacheWrite5mInputPrice *string `json:"cache_write_5m_input_price"`
	CacheWrite1hInputPrice *string `json:"cache_write_1h_input_price"`
	OutputPrice            string  `json:"output_price"`
	ReasoningOutputPrice   *string `json:"reasoning_output_price"`
	UncachedInputCost      *string `json:"uncached_input_cost"`
	CacheReadInputCost     *string `json:"cache_read_input_cost"`
	CacheWrite5mInputCost  *string `json:"cache_write_5m_input_cost"`
	CacheWrite1hInputCost  *string `json:"cache_write_1h_input_cost"`
	OutputCost             *string `json:"output_cost"`
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
	UncachedInputPrice     string  `json:"uncached_input_price"`
	CacheReadInputPrice    *string `json:"cache_read_input_price"`
	CacheWrite5mInputPrice *string `json:"cache_write_5m_input_price"`
	CacheWrite1hInputPrice *string `json:"cache_write_1h_input_price"`
	OutputPrice            string  `json:"output_price"`
	ReasoningOutputPrice   *string `json:"reasoning_output_price"`
	UncachedInputCost      *string `json:"uncached_input_cost"`
	CacheReadInputCost     *string `json:"cache_read_input_cost"`
	CacheWrite5mInputCost  *string `json:"cache_write_5m_input_cost"`
	CacheWrite1hInputCost  *string `json:"cache_write_1h_input_cost"`
	OutputCost             *string `json:"output_cost"`
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
	channelID, err := pathID(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	prices, err := h.service.List(r.Context(), channelID)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	dtos := make([]channelPriceDTO, 0, len(prices))
	for _, p := range prices {
		dtos = append(dtos, toChannelPriceDTO(p))
	}

	writeData(w, http.StatusOK, dtos)
}

func (h *channelPricesHandler) create(w http.ResponseWriter, r *http.Request) {
	channelID, err := pathID(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	modelID, err := pathModelID(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	var req createChannelPriceRequest
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

	p, err := h.service.Create(r.Context(), channelprice.CreateInput{
		ChannelID:              channelID,
		ModelID:                modelID,
		Currency:               req.Currency,
		PricingUnit:            req.PricingUnit,
		UncachedInputPrice:     req.UncachedInputPrice,
		CacheReadInputPrice:    req.CacheReadInputPrice,
		CacheWrite5mInputPrice: req.CacheWrite5mInputPrice,
		CacheWrite1hInputPrice: req.CacheWrite1hInputPrice,
		OutputPrice:            req.OutputPrice,
		ReasoningOutputPrice:   req.ReasoningOutputPrice,
		UncachedInputCost:      req.UncachedInputCost,
		CacheReadInputCost:     req.CacheReadInputCost,
		CacheWrite5mInputCost:  req.CacheWrite5mInputCost,
		CacheWrite1hInputCost:  req.CacheWrite1hInputCost,
		OutputCost:             req.OutputCost,
		ReasoningOutputCost:    req.ReasoningOutputCost,
		Status:                 req.Status,
		EffectiveFrom:          from,
		EffectiveTo:            to,
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}

	writeData(w, http.StatusCreated, toChannelPriceDTO(p))
}

func (h *channelPricesHandler) update(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	var req updateChannelPriceRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		writeServiceError(w, err)
		return
	}

	to, err := parseOptionalRFC3339("effective_to", req.EffectiveTo)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	p, err := h.service.Update(r.Context(), channelprice.UpdateInput{
		ID:          id,
		Status:      req.Status,
		EffectiveTo: to,
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}

	writeData(w, http.StatusOK, toChannelPriceDTO(p))
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
		UncachedInputPrice:     p.UncachedInputPrice,
		CacheReadInputPrice:    p.CacheReadInputPrice,
		CacheWrite5mInputPrice: p.CacheWrite5mInputPrice,
		CacheWrite1hInputPrice: p.CacheWrite1hInputPrice,
		OutputPrice:            p.OutputPrice,
		ReasoningOutputPrice:   p.ReasoningOutputPrice,
		UncachedInputCost:      p.UncachedInputCost,
		CacheReadInputCost:     p.CacheReadInputCost,
		CacheWrite5mInputCost:  p.CacheWrite5mInputCost,
		CacheWrite1hInputCost:  p.CacheWrite1hInputCost,
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

// parseRFC3339 解析必填 RFC3339 时间，非法时返回 admin_invalid_argument。
// 与价格/线路/API Key 等多个 handler 共用，集中放在本文件。
func parseRFC3339(field, raw string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(raw))
	if err != nil {
		return time.Time{}, failure.New(
			failure.CodeAdminInvalidArgument,
			failure.WithMessage(field+" must be an RFC3339 timestamp"),
			failure.WithField("field", field),
		)
	}
	return t, nil
}

// parseOptionalRFC3339 解析可选 RFC3339 时间：nil/空串 → nil。
func parseOptionalRFC3339(field string, raw *string) (*time.Time, error) {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return nil, nil
	}
	t, err := parseRFC3339(field, *raw)
	if err != nil {
		return nil, err
	}
	return &t, nil
}
