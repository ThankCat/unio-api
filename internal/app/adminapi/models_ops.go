package adminapi

import (
	"context"
	"net/http"
	"time"

	"github.com/ThankCat/unio-api/internal/service/admin/modelops"
)

// ModelOpsService 定义模型商品控制台（§3.4）只读运维聚合所需能力。
type ModelOpsService interface {
	Summary(ctx context.Context, from, to time.Time) (modelops.Summary, error)
	Table(ctx context.Context, p modelops.TableParams) ([]modelops.Row, int64, error)
	Detail(ctx context.Context, modelID int64, from, to time.Time) (modelops.Detail, error)
	Channels(ctx context.Context, modelID int64, from, to time.Time) ([]modelops.ChannelRow, error)
	PerformanceTimeseries(ctx context.Context, modelID int64, interval string, from, to time.Time) ([]modelops.PerfPoint, error)
	Requests(ctx context.Context, modelID int64, from, to time.Time, limit, offset int32) ([]modelops.RequestRow, int64, error)
}

type modelOpsHandler struct {
	service ModelOpsService
}

type modelsOpsSummaryDTO struct {
	Total          int64   `json:"total"`
	Enabled        int64   `json:"enabled"`
	Disabled       int64   `json:"disabled"`
	Sellable       int64   `json:"sellable"`
	NoChannel      int64   `json:"no_channel"`
	PriceTotal     int64   `json:"price_total"`
	PriceWithPrice int64   `json:"price_with_price"`
	RequestTotal   int64   `json:"request_total"`
	Succeeded      int64   `json:"succeeded"`
	SuccessRate    float64 `json:"success_rate"`
	RevenueUSD     string  `json:"revenue_usd"`
	CostUSD        string  `json:"cost_usd"`
	MarginUSD      string  `json:"margin_usd"`
	MarginRate     float64 `json:"margin_rate"`
}

type modelOpsRowDTO struct {
	ID                int64           `json:"id"`
	ModelID           string          `json:"model_id"`
	DisplayName       string          `json:"display_name"`
	OwnedBy           string          `json:"owned_by"`
	Status            string          `json:"status"`
	CreatedAt         string          `json:"created_at"`
	BindingsTotal     int64           `json:"bindings_total"`
	BindingsAvailable int64           `json:"bindings_available"`
	HasPrice          bool            `json:"has_price"`
	Sellable          bool            `json:"sellable"`
	RequestTotal      int64           `json:"request_total"`
	RequestSucceeded  int64           `json:"request_succeeded"`
	SuccessRate       float64         `json:"success_rate"`
	Latency           latencyStatsDTO `json:"latency"`
	RevenueUSD        string          `json:"revenue_usd"`
	MarginUSD         string          `json:"margin_usd"`
	MarginRate        float64         `json:"margin_rate"`
	// 基准售价（DEC-026 model_prices，每 1M tokens；无基准价时为 null）。
	BaseCurrency               *string `json:"base_currency"`
	BaseUncachedInputPrice     *string `json:"base_uncached_input_price"`
	BaseCacheReadInputPrice    *string `json:"base_cache_read_input_price"`
	BaseCacheWrite5mInputPrice *string `json:"base_cache_write_5m_input_price"`
	BaseCacheWrite1hInputPrice *string `json:"base_cache_write_1h_input_price"`
	BaseOutputPrice            *string `json:"base_output_price"`
	BaseReasoningOutputPrice   *string `json:"base_reasoning_output_price"`
}

type modelOpsDetailDTO struct {
	RequestTotal     int64   `json:"request_total"`
	RequestSucceeded int64   `json:"request_succeeded"`
	SuccessRate      float64 `json:"success_rate"`
	LatencyP50       float64 `json:"latency_p50"`
	LatencyP95       float64 `json:"latency_p95"`
	OutputTokens     int64   `json:"output_tokens"`
	InputTokens      int64   `json:"input_tokens"`
	CacheReadRate    float64 `json:"cache_read_rate"`
	TPS              float64 `json:"tps"`
}

type modelOpsChannelDTO struct {
	ChannelID        int64   `json:"channel_id"`
	ChannelName      string  `json:"channel_name"`
	ChannelStatus    string  `json:"channel_status"`
	BindingStatus    string  `json:"binding_status"`
	UpstreamModel    string  `json:"upstream_model"`
	Priority         int32   `json:"priority"`
	AttemptTotal     int64   `json:"attempt_total"`
	AttemptSucceeded int64   `json:"attempt_succeeded"`
	SuccessRate      float64 `json:"success_rate"`
	LatencyP95       float64 `json:"latency_p95"`
	HasPrice         bool    `json:"has_price"`
}

type modelOpsPerfPointDTO struct {
	Bucket           string  `json:"bucket"`
	RequestTotal     int64   `json:"request_total"`
	RequestSucceeded int64   `json:"request_succeeded"`
	LatencyP95       float64 `json:"latency_p95"`
}

type modelOpsRequestDTO struct {
	RequestID      string   `json:"request_id"`
	At             string   `json:"at"`
	Status         string   `json:"status"`
	ErrorCode      string   `json:"error_code"`
	FinalChannelID *int64   `json:"final_channel_id"`
	LatencyMs      *float64 `json:"latency_ms"`
}

func (h *modelOpsHandler) summary(w http.ResponseWriter, r *http.Request) {
	from, to, _, err := rangeWindow(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	s, err := h.service.Summary(r.Context(), from, to)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeData(w, http.StatusOK, modelsOpsSummaryDTO{
		Total:          s.Total,
		Enabled:        s.Enabled,
		Disabled:       s.Disabled,
		Sellable:       s.Sellable,
		NoChannel:      s.NoChannel,
		PriceTotal:     s.PriceTotal,
		PriceWithPrice: s.PriceWithPrice,
		RequestTotal:   s.RequestTotal,
		Succeeded:      s.Succeeded,
		SuccessRate:    s.SuccessRate,
		RevenueUSD:     s.RevenueUSD,
		CostUSD:        s.CostUSD,
		MarginUSD:      s.MarginUSD,
		MarginRate:     s.MarginRate,
	})
}

func (h *modelOpsHandler) table(w http.ResponseWriter, r *http.Request) {
	from, to, _, err := rangeWindow(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	page := parsePage(r)
	sort, err := parseListSort(r, map[string]struct{}{
		"name":         {},
		"requests":     {},
		"success_rate": {},
		"margin":       {},
		"created_at":   {},
	}, "success_rate", false)
	if err != nil {
		writeSortError(w, err)
		return
	}
	field, desc := sort.SQLParams()
	rows, total, err := h.service.Table(r.Context(), modelops.TableParams{
		From:      from,
		To:        to,
		Status:    listStatus(r),
		Search:    queryString(r, "search"),
		SortField: field,
		SortDesc:  desc,
		Limit:     page.Limit(),
		Offset:    page.Offset(),
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	out := make([]modelOpsRowDTO, 0, len(rows))
	for _, row := range rows {
		out = append(out, modelOpsRowDTO{
			ID:                         row.ID,
			ModelID:                    row.ModelID,
			DisplayName:                row.DisplayName,
			OwnedBy:                    row.OwnedBy,
			Status:                     row.Status,
			CreatedAt:                  rfc3339(row.CreatedAt),
			BindingsTotal:              row.BindingsTotal,
			BindingsAvailable:          row.BindingsAvailable,
			HasPrice:                   row.HasPrice,
			Sellable:                   row.Sellable,
			RequestTotal:               row.RequestTotal,
			RequestSucceeded:           row.RequestSucceeded,
			SuccessRate:                row.SuccessRate,
			Latency:                    latencyStatsFrom(row.Latency),
			RevenueUSD:                 row.RevenueUSD,
			MarginUSD:                  row.MarginUSD,
			MarginRate:                 row.MarginRate,
			BaseCurrency:               row.BaseCurrency,
			BaseUncachedInputPrice:     row.BaseUncachedInputPrice,
			BaseCacheReadInputPrice:    row.BaseCacheReadInputPrice,
			BaseCacheWrite5mInputPrice: row.BaseCacheWrite5mInputPrice,
			BaseCacheWrite1hInputPrice: row.BaseCacheWrite1hInputPrice,
			BaseOutputPrice:            row.BaseOutputPrice,
			BaseReasoningOutputPrice:   row.BaseReasoningOutputPrice,
		})
	}
	writeList(w, http.StatusOK, out, page, total)
}

func (h *modelOpsHandler) detail(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	from, to, _, err := rangeWindow(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	d, err := h.service.Detail(r.Context(), id, from, to)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeData(w, http.StatusOK, modelOpsDetailDTO{
		RequestTotal:     d.RequestTotal,
		RequestSucceeded: d.RequestSucceeded,
		SuccessRate:      d.SuccessRate,
		LatencyP50:       d.LatencyP50,
		LatencyP95:       d.LatencyP95,
		OutputTokens:     d.OutputTokens,
		InputTokens:      d.InputTokens,
		CacheReadRate:    d.CacheReadRate,
		TPS:              d.TPS,
	})
}

func (h *modelOpsHandler) channels(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	from, to, _, err := rangeWindow(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	rows, err := h.service.Channels(r.Context(), id, from, to)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	out := make([]modelOpsChannelDTO, 0, len(rows))
	for _, c := range rows {
		out = append(out, modelOpsChannelDTO{
			ChannelID:        c.ChannelID,
			ChannelName:      c.ChannelName,
			ChannelStatus:    c.ChannelStatus,
			BindingStatus:    c.BindingStatus,
			UpstreamModel:    c.UpstreamModel,
			Priority:         c.Priority,
			AttemptTotal:     c.AttemptTotal,
			AttemptSucceeded: c.AttemptSucceeded,
			SuccessRate:      c.SuccessRate,
			LatencyP95:       c.LatencyP95,
			HasPrice:         c.HasPrice,
		})
	}
	writeData(w, http.StatusOK, out)
}

func (h *modelOpsHandler) performance(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	from, to, interval, err := rangeWindow(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	if q := queryString(r, "interval"); q != "" {
		interval = q
	}
	points, err := h.service.PerformanceTimeseries(r.Context(), id, interval, from, to)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	out := make([]modelOpsPerfPointDTO, 0, len(points))
	for _, p := range points {
		out = append(out, modelOpsPerfPointDTO{Bucket: rfc3339(p.Bucket), RequestTotal: p.RequestTotal, RequestSucceeded: p.RequestSucceeded, LatencyP95: p.LatencyP95})
	}
	writeData(w, http.StatusOK, out)
}

func (h *modelOpsHandler) requests(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	from, to, _, err := rangeWindow(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	page := parsePage(r)
	rows, total, err := h.service.Requests(r.Context(), id, from, to, page.Limit(), page.Offset())
	if err != nil {
		writeServiceError(w, err)
		return
	}
	out := make([]modelOpsRequestDTO, 0, len(rows))
	for _, rr := range rows {
		out = append(out, modelOpsRequestDTO{
			RequestID:      rr.RequestID,
			At:             rfc3339(rr.At),
			Status:         rr.Status,
			ErrorCode:      rr.ErrorCode,
			FinalChannelID: rr.FinalChannelID,
			LatencyMs:      rr.LatencyMs,
		})
	}
	writeList(w, http.StatusOK, out, page, total)
}
