package provider

import (
	"context"
	"net/http"
	"time"

	"github.com/ThankCat/unio-api/internal/app/adminapi/adminhttp"

	"github.com/ThankCat/unio-api/internal/service/admin/providerops"
)

// ProviderOpsService 定义服务商聚合视图（§3.2）只读运维聚合所需能力。
type ProviderOpsService interface {
	Table(ctx context.Context, p providerops.TableParams) ([]providerops.Row, int64, error)
	Detail(ctx context.Context, providerID int64, from, to time.Time) (providerops.Detail, error)
	ChannelCatalog(ctx context.Context, providerID int64) ([]providerops.ChannelCatalogRow, error)
	ModelCatalog(ctx context.Context, providerID int64) ([]providerops.ModelCatalogRow, error)
	RouteCatalog(ctx context.Context, providerID int64) ([]providerops.RouteCatalogRow, error)
	Channels(ctx context.Context, providerID int64, from, to time.Time) ([]providerops.ChannelRow, error)
	PerformanceTimeseries(ctx context.Context, providerID int64, interval string, from, to time.Time) ([]providerops.PerfPoint, error)
	Errors(ctx context.Context, providerID int64, from, to time.Time, limit, offset int32) ([]providerops.ErrorRow, int64, error)
}

type providerOpsHandler struct {
	service ProviderOpsService
}

type providerOpsRowDTO struct {
	ID           int64  `json:"id"`
	Slug         string `json:"slug"`
	Name         string `json:"name"`
	Status       string `json:"status"`
	CreatedAt    string `json:"created_at"`
	ChannelTotal int64  `json:"channel_total"`
	ModelsCount  int64  `json:"models_count"`
	RoutesCount  int64  `json:"routes_count"`
}

type providerOpsDetailDTO struct {
	ChannelTotal     int64                     `json:"channel_total"`
	ChannelEnabled   int64                     `json:"channel_enabled"`
	AttemptTotal     int64                     `json:"attempt_total"`
	AttemptSucceeded int64                     `json:"attempt_succeeded"`
	SuccessRate      float64                   `json:"success_rate"`
	TimeoutTotal     int64                     `json:"timeout_total"`
	Latency          adminhttp.LatencyStatsDTO `json:"latency"`
	Health           string                    `json:"health"`
	Tokens           int64                     `json:"tokens"`
	RevenueUSD       string                    `json:"revenue_usd"`
	CostUSD          string                    `json:"cost_usd"`
	MarginUSD        string                    `json:"margin_usd"`
	AvgTPS           float64                   `json:"avg_tps"`
}

type providerOpsChannelCatalogDTO struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

type providerOpsModelCatalogDTO struct {
	ModelID     string `json:"model_id"`
	DisplayName string `json:"display_name"`
}

type providerOpsRouteCatalogDTO struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Mode   string `json:"mode"`
}

type providerOpsChannelDTO struct {
	ID               int64                     `json:"id"`
	Name             string                    `json:"name"`
	BaseURL          string                    `json:"base_url"`
	Status           string                    `json:"status"`
	AttemptTotal     int64                     `json:"attempt_total"`
	AttemptSucceeded int64                     `json:"attempt_succeeded"`
	SuccessRate      float64                   `json:"success_rate"`
	Latency          adminhttp.LatencyStatsDTO `json:"latency"`
	Health           string                    `json:"health"`
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
	page := adminhttp.ParsePage(r)
	sort, err := adminhttp.ParseListSort(r, map[string]struct{}{
		"name":       {},
		"channels":   {},
		"models":     {},
		"routes":     {},
		"created_at": {},
		"status":     {},
	}, "name", false)
	if err != nil {
		adminhttp.WriteSortError(w, err)
		return
	}
	field, desc := sort.SQLParams()
	rows, total, err := h.service.Table(r.Context(), providerops.TableParams{
		Status:    adminhttp.ListStatus(r),
		Search:    adminhttp.QueryString(r, "search"),
		SortField: field,
		SortDesc:  desc,
		Limit:     page.Limit(),
		Offset:    page.Offset(),
	})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	out := make([]providerOpsRowDTO, 0, len(rows))
	for _, row := range rows {
		out = append(out, providerOpsRowDTO{
			ID:           row.ID,
			Slug:         row.Slug,
			Name:         row.Name,
			Status:       row.Status,
			CreatedAt:    adminhttp.RFC3339(row.CreatedAt),
			ChannelTotal: row.ChannelTotal,
			ModelsCount:  row.ModelsCount,
			RoutesCount:  row.RoutesCount,
		})
	}
	adminhttp.WriteList(w, http.StatusOK, out, page, total)
}

func (h *providerOpsHandler) detail(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	from, to, _, err := adminhttp.RangeWindow(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	d, err := h.service.Detail(r.Context(), id, from, to)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, providerOpsDetailDTO{
		ChannelTotal:     d.ChannelTotal,
		ChannelEnabled:   d.ChannelEnabled,
		AttemptTotal:     d.AttemptTotal,
		AttemptSucceeded: d.AttemptSucceeded,
		SuccessRate:      d.SuccessRate,
		TimeoutTotal:     d.TimeoutTotal,
		Latency:          adminhttp.LatencyStatsFrom(d.Latency),
		Health:           d.HealthBucket,
		Tokens:           d.Tokens,
		RevenueUSD:       d.RevenueUSD,
		CostUSD:          d.CostUSD,
		MarginUSD:        d.MarginUSD,
		AvgTPS:           d.AvgTPS,
	})
}

func (h *providerOpsHandler) channelCatalog(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	rows, err := h.service.ChannelCatalog(r.Context(), id)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	out := make([]providerOpsChannelCatalogDTO, 0, len(rows))
	for _, c := range rows {
		out = append(out, providerOpsChannelCatalogDTO{ID: c.ID, Name: c.Name, Status: c.Status})
	}
	adminhttp.WriteData(w, http.StatusOK, out)
}

func (h *providerOpsHandler) modelCatalog(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	rows, err := h.service.ModelCatalog(r.Context(), id)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	out := make([]providerOpsModelCatalogDTO, 0, len(rows))
	for _, m := range rows {
		out = append(out, providerOpsModelCatalogDTO{ModelID: m.ModelID, DisplayName: m.DisplayName})
	}
	adminhttp.WriteData(w, http.StatusOK, out)
}

func (h *providerOpsHandler) routeCatalog(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	rows, err := h.service.RouteCatalog(r.Context(), id)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	out := make([]providerOpsRouteCatalogDTO, 0, len(rows))
	for _, rt := range rows {
		out = append(out, providerOpsRouteCatalogDTO{ID: rt.ID, Name: rt.Name, Status: rt.Status, Mode: rt.Mode})
	}
	adminhttp.WriteData(w, http.StatusOK, out)
}

func (h *providerOpsHandler) channels(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	from, to, _, err := adminhttp.RangeWindow(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	rows, err := h.service.Channels(r.Context(), id, from, to)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
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
			Latency:          adminhttp.LatencyStatsFrom(c.Latency),
			Health:           c.HealthBucket,
		})
	}
	adminhttp.WriteData(w, http.StatusOK, out)
}

func (h *providerOpsHandler) performance(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	from, to, interval, err := adminhttp.RangeWindow(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	if q := adminhttp.QueryString(r, "interval"); q != "" {
		interval = q
	}
	points, err := h.service.PerformanceTimeseries(r.Context(), id, interval, from, to)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	out := make([]providerOpsPerfPointDTO, 0, len(points))
	for _, p := range points {
		out = append(out, providerOpsPerfPointDTO{Bucket: adminhttp.RFC3339(p.Bucket), AttemptTotal: p.AttemptTotal, AttemptSucceeded: p.AttemptSucceeded, LatencyAvg: p.LatencyAvg})
	}
	adminhttp.WriteData(w, http.StatusOK, out)
}

func (h *providerOpsHandler) errors(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	from, to, _, err := adminhttp.RangeWindow(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	page := adminhttp.ParsePage(r)
	rows, total, err := h.service.Errors(r.Context(), id, from, to, page.Limit(), page.Offset())
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	out := make([]providerOpsErrorDTO, 0, len(rows))
	for _, e := range rows {
		out = append(out, providerOpsErrorDTO{
			At:                 adminhttp.RFC3339(e.At),
			ChannelName:        e.ChannelName,
			UpstreamModel:      e.UpstreamModel,
			ErrorCode:          e.ErrorCode,
			UpstreamStatusCode: e.UpstreamStatusCode,
			RequestID:          e.RequestID,
		})
	}
	adminhttp.WriteList(w, http.StatusOK, out, page, total)
}
