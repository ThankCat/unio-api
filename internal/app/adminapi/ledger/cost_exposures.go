package ledger

import (
	"context"
	"net/http"
	"time"

	"github.com/ThankCat/unio-api/internal/app/adminapi/adminhttp"

	"github.com/ThankCat/unio-api/internal/service/admin/query"
)

// CostExposureQueryService 定义 adminapi 查询渠道成本敞口所需的最小能力（DESIGN-bill-on-cancel 阶段一）。
type CostExposureQueryService interface {
	Summarize(ctx context.Context, from, to time.Time) ([]query.CostExposureSummary, error)
	List(ctx context.Context, params query.CostExposureListParams) ([]query.CostExposureItem, int64, error)
}

// costExposureSummaryDTO 是渠道成本敞口聚合响应体；金额为十进制字符串（保守上界估算）。
type costExposureSummaryDTO struct {
	ChannelID          int64  `json:"channel_id"`
	ChannelName        string `json:"channel_name"`
	ProviderID         int64  `json:"provider_id"`
	Currency           string `json:"currency"`
	Exposures          int64  `json:"exposures"`
	TotalEstimatedCost string `json:"total_estimated_cost"`
}

// costExposureItemDTO 是成本敞口明细响应体。
type costExposureItemDTO struct {
	ID                   int64  `json:"id"`
	RequestRecordID      int64  `json:"request_record_id"`
	RequestID            string `json:"request_id"`
	AttemptID            int64  `json:"attempt_id"`
	ChannelID            int64  `json:"channel_id"`
	ProviderID           int64  `json:"provider_id"`
	Reason               string `json:"reason"`
	EstimatedInputTokens int64  `json:"estimated_input_tokens"`
	AssumedOutputTokens  int64  `json:"assumed_output_tokens"`
	EstimatedCostAmount  string `json:"estimated_cost_amount"`
	Currency             string `json:"currency"`
	CreatedAt            string `json:"created_at"`
}

type costExposuresHandler struct {
	service CostExposureQueryService
}

// exposureTimeRange 解析 from/to 查询参数；缺省为最近 7 天。
func exposureTimeRange(r *http.Request) (time.Time, time.Time, error) {
	now := time.Now().UTC()
	from := now.AddDate(0, 0, -7)
	to := now

	if parsed, err := adminhttp.OptionalTimeQuery(r, "from"); err != nil {
		return time.Time{}, time.Time{}, err
	} else if parsed != nil {
		from = *parsed
	}
	if parsed, err := adminhttp.OptionalTimeQuery(r, "to"); err != nil {
		return time.Time{}, time.Time{}, err
	} else if parsed != nil {
		to = *parsed
	}
	return from, to, nil
}

func (h *costExposuresHandler) summary(w http.ResponseWriter, r *http.Request) {
	from, to, err := exposureTimeRange(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	rows, err := h.service.Summarize(r.Context(), from, to)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	out := make([]costExposureSummaryDTO, 0, len(rows))
	for _, row := range rows {
		out = append(out, costExposureSummaryDTO{
			ChannelID:          row.ChannelID,
			ChannelName:        row.ChannelName,
			ProviderID:         row.ProviderID,
			Currency:           row.Currency,
			Exposures:          row.Exposures,
			TotalEstimatedCost: row.TotalEstimatedCost,
		})
	}
	adminhttp.WriteData(w, http.StatusOK, out)
}

func (h *costExposuresHandler) list(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	from, to, err := exposureTimeRange(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	page := adminhttp.ParsePage(r)
	items, total, err := h.service.List(r.Context(), query.CostExposureListParams{
		ChannelID: id,
		From:      from,
		To:        to,
		Limit:     page.Limit(),
		Offset:    page.Offset(),
	})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	out := make([]costExposureItemDTO, 0, len(items))
	for _, item := range items {
		out = append(out, costExposureItemDTO{
			ID:                   item.ID,
			RequestRecordID:      item.RequestRecordID,
			RequestID:            item.RequestID,
			AttemptID:            item.AttemptID,
			ChannelID:            item.ChannelID,
			ProviderID:           item.ProviderID,
			Reason:               item.Reason,
			EstimatedInputTokens: item.EstimatedInputTokens,
			AssumedOutputTokens:  item.AssumedOutputTokens,
			EstimatedCostAmount:  item.EstimatedCostAmount,
			Currency:             item.Currency,
			CreatedAt:            item.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	adminhttp.WriteList(w, http.StatusOK, out, page, total)
}
