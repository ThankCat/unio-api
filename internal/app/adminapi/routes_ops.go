package adminapi

import (
	"context"
	"net/http"
	"time"

	"github.com/ThankCat/unio-api/internal/service/admin/routeops"
)

// RouteOpsService 定义线路路由作战台（§3.5）只读运维聚合所需能力。
type RouteOpsService interface {
	Summary(ctx context.Context, from, to time.Time) (routeops.Summary, error)
	Table(ctx context.Context, p routeops.TableParams) ([]routeops.Row, int64, error)
	Detail(ctx context.Context, routeID int64, from, to time.Time) (routeops.Detail, error)
	ChannelPool(ctx context.Context, routeID int64) ([]routeops.ChannelPoolRow, error)
	Bindings(ctx context.Context, routeID int64) ([]routeops.BoundUser, []routeops.BoundKey, error)
	PerformanceTimeseries(ctx context.Context, routeID int64, interval string, from, to time.Time) ([]routeops.PerfPoint, error)
	Models(ctx context.Context, routeID int64, from, to time.Time) ([]routeops.ModelRow, error)
	ReachableModels(ctx context.Context, routeID int64) ([]routeops.ReachableModel, error)
	Requests(ctx context.Context, routeID int64, from, to time.Time, limit, offset int32) ([]routeops.RequestRow, int64, error)
}

type routeOpsHandler struct {
	service RouteOpsService
}

type routesOpsSummaryDTO struct {
	Total         int64   `json:"total"`
	Enabled       int64   `json:"enabled"`
	Disabled      int64   `json:"disabled"`
	RequestTotal  int64   `json:"request_total"`
	Succeeded     int64   `json:"succeeded"`
	SuccessRate   float64 `json:"success_rate"`
	FallbackTotal int64   `json:"fallback_total"`
	FallbackRate  float64 `json:"fallback_rate"`
	NoChannel     int64   `json:"no_channel"`
	LatencyP95    float64 `json:"latency_p95"`
}

type routeOpsRowDTO struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Mode         string `json:"mode"`
	PoolKind     string `json:"pool_kind"`
	Status       string `json:"status"`
	Description  string `json:"description"`
	PriceRatio   string `json:"price_ratio"`
	RpmLimit     *int32 `json:"rpm_limit"`
	TpmLimit     *int32 `json:"tpm_limit"`
	RpdLimit     *int32 `json:"rpd_limit"`
	CreatedAt    string `json:"created_at"`
	BoundKeys    int64  `json:"bound_keys"`
	PoolChannels int64  `json:"pool_channels"`
	ModelsCount  int64  `json:"models_count"`
}

type routeOpsDetailDTO struct {
	RequestTotal     int64   `json:"request_total"`
	RequestSucceeded int64   `json:"request_succeeded"`
	SuccessRate      float64 `json:"success_rate"`
	FallbackTotal    int64   `json:"fallback_total"`
	FallbackRate     float64 `json:"fallback_rate"`
	NoChannelTotal   int64   `json:"no_channel_total"`
	LatencyP50       float64 `json:"latency_p50"`
	LatencyP95       float64 `json:"latency_p95"`
	Serviceable      bool    `json:"serviceable"`
	Abnormal         bool    `json:"abnormal"`
	RouteStatus      string  `json:"route_status"`
}

type routeOpsReachableModelDTO struct {
	ModelID     string `json:"model_id"`
	DisplayName string `json:"display_name"`
}

type routeOpsChannelPoolDTO struct {
	ChannelID     int64  `json:"channel_id"`
	ChannelName   string `json:"channel_name"`
	ChannelStatus string `json:"channel_status"`
	Priority      int32  `json:"priority"`
	ProviderName  string `json:"provider_name"`
}

type routeOpsBoundUserDTO struct {
	ID          int64  `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
}

type routeOpsBoundKeyDTO struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	UserID int64  `json:"user_id"`
	Status string `json:"status"`
}

type routeOpsBindingsDTO struct {
	Users []routeOpsBoundUserDTO `json:"users"`
	Keys  []routeOpsBoundKeyDTO  `json:"keys"`
}

type routeOpsPerfPointDTO struct {
	Bucket           string  `json:"bucket"`
	RequestTotal     int64   `json:"request_total"`
	RequestSucceeded int64   `json:"request_succeeded"`
	LatencyP95       float64 `json:"latency_p95"`
}

type routeOpsModelDTO struct {
	ModelID          string  `json:"model_id"`
	RequestTotal     int64   `json:"request_total"`
	RequestSucceeded int64   `json:"request_succeeded"`
	SuccessRate      float64 `json:"success_rate"`
}

type routeOpsRequestDTO struct {
	RequestID      string   `json:"request_id"`
	At             string   `json:"at"`
	Status         string   `json:"status"`
	ModelID        string   `json:"model_id"`
	FinalChannelID *int64   `json:"final_channel_id"`
	LatencyMs      *float64 `json:"latency_ms"`
}

func (h *routeOpsHandler) summary(w http.ResponseWriter, r *http.Request) {
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
	writeData(w, http.StatusOK, routesOpsSummaryDTO{
		Total:         s.Total,
		Enabled:       s.Enabled,
		Disabled:      s.Disabled,
		RequestTotal:  s.RequestTotal,
		Succeeded:     s.Succeeded,
		SuccessRate:   s.SuccessRate,
		FallbackTotal: s.FallbackTotal,
		FallbackRate:  s.FallbackRate,
		NoChannel:     s.NoChannel,
		LatencyP95:    s.LatencyP95,
	})
}

func (h *routeOpsHandler) table(w http.ResponseWriter, r *http.Request) {
	page := parsePage(r)
	sort, err := parseListSort(r, map[string]struct{}{
		"name":          {},
		"created_at":    {},
		"bindings":      {},
		"pool_channels": {},
		"models":        {},
	}, "name", false)
	if err != nil {
		writeSortError(w, err)
		return
	}
	field, desc := sort.SQLParams()
	rows, total, err := h.service.Table(r.Context(), routeops.TableParams{
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
	out := make([]routeOpsRowDTO, 0, len(rows))
	for _, row := range rows {
		out = append(out, routeOpsRowDTO{
			ID:           row.ID,
			Name:         row.Name,
			Mode:         row.Mode,
			PoolKind:     row.PoolKind,
			Status:       row.Status,
			Description:  row.Description,
			PriceRatio:   row.PriceRatio,
			RpmLimit:     row.RpmLimit,
			TpmLimit:     row.TpmLimit,
			RpdLimit:     row.RpdLimit,
			CreatedAt:    rfc3339(row.CreatedAt),
			BoundKeys:    row.BoundKeys,
			PoolChannels: row.PoolChannels,
			ModelsCount:  row.ModelsCount,
		})
	}
	writeList(w, http.StatusOK, out, page, total)
}

func (h *routeOpsHandler) detail(w http.ResponseWriter, r *http.Request) {
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
	writeData(w, http.StatusOK, routeOpsDetailDTO{
		RequestTotal:     d.RequestTotal,
		RequestSucceeded: d.RequestSucceeded,
		SuccessRate:      d.SuccessRate,
		FallbackTotal:    d.FallbackTotal,
		FallbackRate:     d.FallbackRate,
		NoChannelTotal:   d.NoChannelTotal,
		LatencyP50:       d.LatencyP50,
		LatencyP95:       d.LatencyP95,
		Serviceable:      d.Serviceable,
		Abnormal:         d.Abnormal,
		RouteStatus:      d.RouteStatus,
	})
}

func (h *routeOpsHandler) reachableModels(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	rows, err := h.service.ReachableModels(r.Context(), id)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	out := make([]routeOpsReachableModelDTO, 0, len(rows))
	for _, m := range rows {
		out = append(out, routeOpsReachableModelDTO{ModelID: m.ModelID, DisplayName: m.DisplayName})
	}
	writeData(w, http.StatusOK, out)
}

func (h *routeOpsHandler) channelPool(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	rows, err := h.service.ChannelPool(r.Context(), id)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	out := make([]routeOpsChannelPoolDTO, 0, len(rows))
	for _, c := range rows {
		out = append(out, routeOpsChannelPoolDTO{ChannelID: c.ChannelID, ChannelName: c.ChannelName, ChannelStatus: c.ChannelStatus, Priority: c.Priority, ProviderName: c.ProviderName})
	}
	writeData(w, http.StatusOK, out)
}

func (h *routeOpsHandler) bindings(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	users, keys, err := h.service.Bindings(r.Context(), id)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	dto := routeOpsBindingsDTO{
		Users: make([]routeOpsBoundUserDTO, 0, len(users)),
		Keys:  make([]routeOpsBoundKeyDTO, 0, len(keys)),
	}
	for _, u := range users {
		dto.Users = append(dto.Users, routeOpsBoundUserDTO{ID: u.ID, Email: u.Email, DisplayName: u.DisplayName})
	}
	for _, k := range keys {
		dto.Keys = append(dto.Keys, routeOpsBoundKeyDTO{ID: k.ID, Name: k.Name, UserID: k.UserID, Status: k.Status})
	}
	writeData(w, http.StatusOK, dto)
}

func (h *routeOpsHandler) performance(w http.ResponseWriter, r *http.Request) {
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
	out := make([]routeOpsPerfPointDTO, 0, len(points))
	for _, p := range points {
		out = append(out, routeOpsPerfPointDTO{Bucket: rfc3339(p.Bucket), RequestTotal: p.RequestTotal, RequestSucceeded: p.RequestSucceeded, LatencyP95: p.LatencyP95})
	}
	writeData(w, http.StatusOK, out)
}

func (h *routeOpsHandler) models(w http.ResponseWriter, r *http.Request) {
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
	out := make([]routeOpsModelDTO, 0, len(rows))
	for _, m := range rows {
		out = append(out, routeOpsModelDTO{ModelID: m.ModelID, RequestTotal: m.RequestTotal, RequestSucceeded: m.RequestSucceeded, SuccessRate: m.SuccessRate})
	}
	writeData(w, http.StatusOK, out)
}

func (h *routeOpsHandler) requests(w http.ResponseWriter, r *http.Request) {
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
	out := make([]routeOpsRequestDTO, 0, len(rows))
	for _, rr := range rows {
		out = append(out, routeOpsRequestDTO{
			RequestID:      rr.RequestID,
			At:             rfc3339(rr.At),
			Status:         rr.Status,
			ModelID:        rr.ModelID,
			FinalChannelID: rr.FinalChannelID,
			LatencyMs:      rr.LatencyMs,
		})
	}
	writeList(w, http.StatusOK, out, page, total)
}
