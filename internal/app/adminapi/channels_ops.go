package adminapi

import (
	"context"
	"net/http"
	"time"

	"github.com/ThankCat/unio-api/internal/service/admin/channelops"
	"github.com/ThankCat/unio-api/internal/service/admin/opsutil"
)

// ChannelOpsService 定义渠道作战台（§3.3）只读运维聚合所需能力。
type ChannelOpsService interface {
	Summary(ctx context.Context, from, to time.Time) (channelops.Summary, error)
	Table(ctx context.Context, p channelops.TableParams) ([]channelops.Row, int64, error)
	Detail(ctx context.Context, channelID int64, from, to time.Time) (channelops.Detail, error)
	PerformanceTimeseries(ctx context.Context, channelID int64, interval string, from, to time.Time) ([]channelops.PerfPoint, error)
	SuccessBuckets(ctx context.Context, channelID int64, from, to time.Time) ([]channelops.SuccessBucket, error)
	Errors(ctx context.Context, channelID int64, from, to time.Time, limit, offset int32) ([]channelops.ErrorRow, int64, error)
	Models(ctx context.Context, channelID int64, from, to time.Time) ([]channelops.ModelRow, error)
	Routes(ctx context.Context, channelID int64) ([]channelops.RouteRow, error)
}

type channelOpsHandler struct {
	service ChannelOpsService
}

type channelHealthCountsDTO struct {
	Healthy   int64 `json:"healthy"`
	Degraded  int64 `json:"degraded"`
	Unhealthy int64 `json:"unhealthy"`
	NoData    int64 `json:"no_data"`
}

type channelsOpsSummaryDTO struct {
	Total            int64                  `json:"total"`
	Enabled          int64                  `json:"enabled"`
	Disabled         int64                  `json:"disabled"`
	Health           channelHealthCountsDTO `json:"health"`
	AttemptTotal     int64                  `json:"attempt_total"`
	AttemptSucceeded int64                  `json:"attempt_succeeded"`
	SuccessRate      float64                `json:"success_rate"`
	TimeoutTotal     int64                  `json:"timeout_total"`
	Latency          latencyStatsDTO        `json:"latency"`
	TPS              float64                `json:"tps"`
	RecentErrorCode  string                 `json:"recent_error_code"`
	RecentErrorName  string                 `json:"recent_error_channel"`
	RecentErrorAt    *string                `json:"recent_error_at"`
	PriceTotal       int64                  `json:"price_total"`
	PriceWithPrice   int64                  `json:"price_with_price"`
	PriceWithCost    int64                  `json:"price_with_cost"`
}

type channelOpsRowDTO struct {
	ID               int64           `json:"id"`
	Name             string          `json:"name"`
	Status           string          `json:"status"`
	CreatedAt        string          `json:"created_at"`
	Protocol         string          `json:"protocol"`
	AdapterKey       string          `json:"adapter_key"`
	BaseURL          string          `json:"base_url"`
	Priority         int32           `json:"priority"`
	ProviderName     string          `json:"provider_name"`
	AttemptTotal     int64           `json:"attempt_total"`
	AttemptSucceeded int64           `json:"attempt_succeeded"`
	SuccessRate      float64         `json:"success_rate"`
	TimeoutTotal     int64           `json:"timeout_total"`
	Latency          latencyStatsDTO `json:"latency"`
	Health           string          `json:"health"`
	LastSuccessAt    *string         `json:"last_success_at"`
	BoundModels      int64           `json:"bound_models"`
	RecentErrorCode  string          `json:"recent_error_code"`
}

type channelOpsDetailDTO struct {
	AttemptTotal     int64           `json:"attempt_total"`
	AttemptSucceeded int64           `json:"attempt_succeeded"`
	SuccessRate      float64         `json:"success_rate"`
	TimeoutTotal     int64           `json:"timeout_total"`
	Latency          latencyStatsDTO `json:"latency"`
	LastSuccessAt    *string         `json:"last_success_at"`
	LastFailureAt    *string         `json:"last_failure_at"`
}

type channelOpsPerfPointDTO struct {
	Bucket           string  `json:"bucket"`
	AttemptTotal     int64   `json:"attempt_total"`
	AttemptSucceeded int64   `json:"attempt_succeeded"`
	LatencyAvg       float64 `json:"latency_avg"`
}

type channelOpsSuccessBucketDTO struct {
	Bucket      string  `json:"bucket"`
	Terminal    int64   `json:"terminal"`
	Succeeded   int64   `json:"succeeded"`
	SuccessRate float64 `json:"success_rate"`
}

type channelOpsErrorDTO struct {
	At                 string `json:"at"`
	UpstreamModel      string `json:"upstream_model"`
	ErrorCode          string `json:"error_code"`
	UpstreamStatusCode *int32 `json:"upstream_status_code"`
	ErrorMessage       string `json:"error_message"`
	RequestID          string `json:"request_id"`
}

type channelOpsModelDTO struct {
	ModelID          int64           `json:"model_id"`
	ModelRef         string          `json:"model_ref"`
	DisplayName      string          `json:"display_name"`
	UpstreamModel    string          `json:"upstream_model"`
	Status           string          `json:"status"`
	AttemptTotal     int64           `json:"attempt_total"`
	AttemptSucceeded int64           `json:"attempt_succeeded"`
	SuccessRate      float64         `json:"success_rate"`
	Latency          latencyStatsDTO `json:"latency"`
	HasPrice         bool            `json:"has_price"`
}

type channelOpsRouteDTO struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Mode     string `json:"mode"`
	PoolKind string `json:"pool_kind"`
	Status   string `json:"status"`
}

func (h *channelOpsHandler) summary(w http.ResponseWriter, r *http.Request) {
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
	writeData(w, http.StatusOK, channelsOpsSummaryDTO{
		Total:            s.Total,
		Enabled:          s.Enabled,
		Disabled:         s.Disabled,
		Health:           channelHealthCountsDTO{Healthy: s.Health.Healthy, Degraded: s.Health.Degraded, Unhealthy: s.Health.Unhealthy, NoData: s.Health.NoData},
		AttemptTotal:     s.AttemptTotal,
		AttemptSucceeded: s.AttemptSucceeded,
		SuccessRate:      s.SuccessRate,
		TimeoutTotal:     s.TimeoutTotal,
		Latency:          latencyStatsFrom(s.Latency),
		TPS:              s.TPS,
		RecentErrorCode:  s.RecentError.Code,
		RecentErrorName:  s.RecentError.ChannelName,
		RecentErrorAt:    rfc3339Ptr(s.RecentError.At),
		PriceTotal:       s.PriceTotal,
		PriceWithPrice:   s.PriceWithPrice,
		PriceWithCost:    s.PriceWithCost,
	})
}

func (h *channelOpsHandler) table(w http.ResponseWriter, r *http.Request) {
	from, to, _, err := rangeWindow(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	page := parsePage(r)
	providerID, err := optionalInt64Query(r, "provider_id")
	if err != nil {
		writeServiceError(w, err)
		return
	}
	sort, err := parseListSort(r, map[string]struct{}{
		"name":         {},
		"requests":     {},
		"success_rate": {},
		"status":       {},
		"created_at":   {},
	}, "success_rate", false)
	if err != nil {
		writeSortError(w, err)
		return
	}
	field, desc := sort.SQLParams()
	rows, total, err := h.service.Table(r.Context(), channelops.TableParams{
		From:       from,
		To:         to,
		Status:     listStatus(r),
		ProviderID: providerID,
		Search:     queryString(r, "search"),
		SortField:  field,
		SortDesc:   desc,
		Limit:      page.Limit(),
		Offset:     page.Offset(),
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	dtos := make([]channelOpsRowDTO, 0, len(rows))
	for _, row := range rows {
		dtos = append(dtos, channelOpsRowDTO{
			ID:               row.ID,
			Name:             row.Name,
			Status:           row.Status,
			CreatedAt:        rfc3339(row.CreatedAt),
			Protocol:         row.Protocol,
			AdapterKey:       row.AdapterKey,
			BaseURL:          row.BaseURL,
			Priority:         row.Priority,
			ProviderName:     row.ProviderName,
			AttemptTotal:     row.AttemptTotal,
			AttemptSucceeded: row.AttemptSucceeded,
			SuccessRate:      row.SuccessRate,
			TimeoutTotal:     row.TimeoutTotal,
			Latency:          latencyStatsFrom(row.Latency),
			Health:           row.HealthBucket,
			LastSuccessAt:    rfc3339Ptr(row.LastSuccessAt),
			BoundModels:      row.BoundModels,
			RecentErrorCode:  row.RecentErrorCode,
		})
	}
	writeList(w, http.StatusOK, dtos, page, total)
}

func (h *channelOpsHandler) detail(w http.ResponseWriter, r *http.Request) {
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
	writeData(w, http.StatusOK, channelOpsDetailDTO{
		AttemptTotal:     d.AttemptTotal,
		AttemptSucceeded: d.AttemptSucceeded,
		SuccessRate:      d.SuccessRate,
		TimeoutTotal:     d.TimeoutTotal,
		Latency:          latencyStatsFrom(d.Latency),
		LastSuccessAt:    rfc3339Ptr(d.LastSuccessAt),
		LastFailureAt:    rfc3339Ptr(d.LastFailureAt),
	})
}

func (h *channelOpsHandler) successBuckets(w http.ResponseWriter, r *http.Request) {
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
	buckets, err := h.service.SuccessBuckets(r.Context(), id, from, to)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	out := make([]channelOpsSuccessBucketDTO, 0, len(buckets))
	for _, b := range buckets {
		out = append(out, channelOpsSuccessBucketDTO{
			Bucket:      rfc3339(b.Bucket),
			Terminal:    b.Terminal,
			Succeeded:   b.Succeeded,
			SuccessRate: b.SuccessRate,
		})
	}
	writeData(w, http.StatusOK, out)
}

func (h *channelOpsHandler) performance(w http.ResponseWriter, r *http.Request) {
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
	out := make([]channelOpsPerfPointDTO, 0, len(points))
	for _, p := range points {
		out = append(out, channelOpsPerfPointDTO{Bucket: rfc3339(p.Bucket), AttemptTotal: p.AttemptTotal, AttemptSucceeded: p.AttemptSucceeded, LatencyAvg: p.LatencyAvg})
	}
	writeData(w, http.StatusOK, out)
}

func (h *channelOpsHandler) errors(w http.ResponseWriter, r *http.Request) {
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
	out := make([]channelOpsErrorDTO, 0, len(rows))
	for _, e := range rows {
		out = append(out, channelOpsErrorDTO{
			At:                 rfc3339(e.At),
			UpstreamModel:      e.UpstreamModel,
			ErrorCode:          e.ErrorCode,
			UpstreamStatusCode: e.UpstreamStatusCode,
			ErrorMessage:       e.ErrorMessage,
			RequestID:          e.RequestID,
		})
	}
	writeList(w, http.StatusOK, out, page, total)
}

func (h *channelOpsHandler) models(w http.ResponseWriter, r *http.Request) {
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
	rows, err := h.service.Models(r.Context(), id, from, to)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	out := make([]channelOpsModelDTO, 0, len(rows))
	for _, m := range rows {
		out = append(out, channelOpsModelDTO{
			ModelID:          m.ModelID,
			ModelRef:         m.ModelRef,
			DisplayName:      m.DisplayName,
			UpstreamModel:    m.UpstreamModel,
			Status:           m.Status,
			AttemptTotal:     m.AttemptTotal,
			AttemptSucceeded: m.AttemptSucceeded,
			SuccessRate:      m.SuccessRate,
			Latency:          latencyStatsFrom(m.Latency),
			HasPrice:         m.HasPrice,
		})
	}
	writeData(w, http.StatusOK, out)
}

func (h *channelOpsHandler) routes(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	rows, err := h.service.Routes(r.Context(), id)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	out := make([]channelOpsRouteDTO, 0, len(rows))
	for _, rt := range rows {
		out = append(out, channelOpsRouteDTO{ID: rt.ID, Name: rt.Name, Mode: rt.Mode, PoolKind: rt.PoolKind, Status: rt.Status})
	}
	writeData(w, http.StatusOK, out)
}

func latencyStatsFrom(s opsutil.LatencyStats) latencyStatsDTO {
	return latencyStatsDTO{
		Avg:      s.Avg,
		P50:      s.P50,
		P90:      s.P90,
		P95:      s.P95,
		P99:      s.P99,
		Sample:   s.Sample,
		Coverage: s.Coverage,
	}
}
