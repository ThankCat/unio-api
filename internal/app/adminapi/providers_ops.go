package adminapi

import (
	"context"
	"net/http"
	"time"

	"github.com/ThankCat/unio-api/internal/service/admin/providerops"
)

// ProviderOpsService 定义服务商聚合视图（§3.2）只读运维聚合所需能力。
type ProviderOpsService interface {
	Table(ctx context.Context, p providerops.TableParams) ([]providerops.Row, int64, error)
	Detail(ctx context.Context, providerID int64, from, to time.Time) (providerops.Detail, error)
	Channels(ctx context.Context, providerID int64, from, to time.Time) ([]providerops.ChannelRow, error)
	PerformanceTimeseries(ctx context.Context, providerID int64, interval string, from, to time.Time) ([]providerops.PerfPoint, error)
	Errors(ctx context.Context, providerID int64, from, to time.Time, limit, offset int32) ([]providerops.ErrorRow, int64, error)
}

type providerOpsHandler struct {
	service ProviderOpsService
}

type providerOpsRowDTO struct {
	ID               int64           `json:"id"`
	Slug             string          `json:"slug"`
	Name             string          `json:"name"`
	Status           string          `json:"status"`
	CreatedAt        string          `json:"created_at"`
	ChannelTotal     int64           `json:"channel_total"`
	ChannelEnabled   int64           `json:"channel_enabled"`
	AttemptTotal     int64           `json:"attempt_total"`
	AttemptSucceeded int64           `json:"attempt_succeeded"`
	SuccessRate      float64         `json:"success_rate"`
	TimeoutTotal     int64           `json:"timeout_total"`
	Latency          latencyStatsDTO `json:"latency"`
	Health           string          `json:"health"`
	LastSuccessAt    *string         `json:"last_success_at"`
	Tokens           int64           `json:"tokens"`
	RevenueUSD       string          `json:"revenue_usd"`
	CostUSD          string          `json:"cost_usd"`
	MarginUSD        string          `json:"margin_usd"`
	AvgTPS           float64         `json:"avg_tps"`
}

type providerOpsDetailDTO struct {
	ChannelTotal     int64           `json:"channel_total"`
	ChannelEnabled   int64           `json:"channel_enabled"`
	AttemptTotal     int64           `json:"attempt_total"`
	AttemptSucceeded int64           `json:"attempt_succeeded"`
	SuccessRate      float64         `json:"success_rate"`
	TimeoutTotal     int64           `json:"timeout_total"`
	Latency          latencyStatsDTO `json:"latency"`
}

type providerOpsChannelDTO struct {
	ID               int64           `json:"id"`
	Name             string          `json:"name"`
	BaseURL          string          `json:"base_url"`
	Status           string          `json:"status"`
	AttemptTotal     int64           `json:"attempt_total"`
	AttemptSucceeded int64           `json:"attempt_succeeded"`
	SuccessRate      float64         `json:"success_rate"`
	Latency          latencyStatsDTO `json:"latency"`
	Health           string          `json:"health"`
}

type providerOpsPerfPointDTO struct {
	Bucket           string  `json:"bucket"`
	AttemptTotal     int64   `json:"attempt_total"`
	AttemptSucceeded int64   `json:"attempt_succeeded"`
	LatencyAvg       float64 `json:"latency_avg"`
}

type providerOpsErrorDTO struct {
	At                 string `json:"at"`
	ChannelName        string `json:"channel_name"`
	UpstreamModel      string `json:"upstream_model"`
	ErrorCode          string `json:"error_code"`
	UpstreamStatusCode *int32 `json:"upstream_status_code"`
	RequestID          string `json:"request_id"`
}

func (h *providerOpsHandler) table(w http.ResponseWriter, r *http.Request) {
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
		"tokens":       {},
		"margin":       {},
		"created_at":   {},
	}, "success_rate", false)
	if err != nil {
		writeSortError(w, err)
		return
	}
	field, desc := sort.SQLParams()
	rows, total, err := h.service.Table(r.Context(), providerops.TableParams{
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
	out := make([]providerOpsRowDTO, 0, len(rows))
	for _, row := range rows {
		out = append(out, providerOpsRowDTO{
			ID:               row.ID,
			Slug:             row.Slug,
			Name:             row.Name,
			Status:           row.Status,
			CreatedAt:        rfc3339(row.CreatedAt),
			ChannelTotal:     row.ChannelTotal,
			ChannelEnabled:   row.ChannelEnabled,
			AttemptTotal:     row.AttemptTotal,
			AttemptSucceeded: row.AttemptSucceeded,
			SuccessRate:      row.SuccessRate,
			TimeoutTotal:     row.TimeoutTotal,
			Latency:          latencyStatsFrom(row.Latency),
			Health:           row.HealthBucket,
			LastSuccessAt:    rfc3339Ptr(row.LastSuccessAt),
			Tokens:           row.Tokens,
			RevenueUSD:       row.RevenueUSD,
			CostUSD:          row.CostUSD,
			MarginUSD:        row.MarginUSD,
			AvgTPS:           row.AvgTPS,
		})
	}
	writeList(w, http.StatusOK, out, page, total)
}

func (h *providerOpsHandler) detail(w http.ResponseWriter, r *http.Request) {
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
	writeData(w, http.StatusOK, providerOpsDetailDTO{
		ChannelTotal:     d.ChannelTotal,
		ChannelEnabled:   d.ChannelEnabled,
		AttemptTotal:     d.AttemptTotal,
		AttemptSucceeded: d.AttemptSucceeded,
		SuccessRate:      d.SuccessRate,
		TimeoutTotal:     d.TimeoutTotal,
		Latency:          latencyStatsFrom(d.Latency),
	})
}

func (h *providerOpsHandler) channels(w http.ResponseWriter, r *http.Request) {
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
	out := make([]providerOpsChannelDTO, 0, len(rows))
	for _, c := range rows {
		out = append(out, providerOpsChannelDTO{
			ID:               c.ID,
			Name:             c.Name,
			BaseURL:          c.BaseURL,
			Status:           c.Status,
			AttemptTotal:     c.AttemptTotal,
			AttemptSucceeded: c.AttemptSucceeded,
			SuccessRate:      c.SuccessRate,
			Latency:          latencyStatsFrom(c.Latency),
			Health:           c.HealthBucket,
		})
	}
	writeData(w, http.StatusOK, out)
}

func (h *providerOpsHandler) performance(w http.ResponseWriter, r *http.Request) {
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
	out := make([]providerOpsPerfPointDTO, 0, len(points))
	for _, p := range points {
		out = append(out, providerOpsPerfPointDTO{Bucket: rfc3339(p.Bucket), AttemptTotal: p.AttemptTotal, AttemptSucceeded: p.AttemptSucceeded, LatencyAvg: p.LatencyAvg})
	}
	writeData(w, http.StatusOK, out)
}

func (h *providerOpsHandler) errors(w http.ResponseWriter, r *http.Request) {
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
	rows, total, err := h.service.Errors(r.Context(), id, from, to, page.Limit(), page.Offset())
	if err != nil {
		writeServiceError(w, err)
		return
	}
	out := make([]providerOpsErrorDTO, 0, len(rows))
	for _, e := range rows {
		out = append(out, providerOpsErrorDTO{
			At:                 rfc3339(e.At),
			ChannelName:        e.ChannelName,
			UpstreamModel:      e.UpstreamModel,
			ErrorCode:          e.ErrorCode,
			UpstreamStatusCode: e.UpstreamStatusCode,
			RequestID:          e.RequestID,
		})
	}
	writeList(w, http.StatusOK, out, page, total)
}
