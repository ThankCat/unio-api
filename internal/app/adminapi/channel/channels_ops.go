package channel

import (
	"context"
	"net/http"
	"time"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi/adminhttp"

	"github.com/ThankCat/unio-gateway/internal/service/admin/channelops"
	"github.com/ThankCat/unio-gateway/internal/service/admin/gatewayruntime"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/lifecycle"
)

// ChannelOpsService 定义渠道作战台（§3.3）只读运维聚合所需能力。
type ChannelOpsService interface {
	Table(ctx context.Context, p channelops.TableParams) ([]channelops.Row, int64, error)
	Detail(ctx context.Context, channelID int64, from, to time.Time) (channelops.Detail, error)
	PerformanceTimeseries(ctx context.Context, channelID int64, interval string, from, to time.Time) ([]channelops.PerfPoint, error)
	Errors(ctx context.Context, channelID int64, from, to time.Time, limit, offset int32) ([]channelops.ErrorRow, int64, error)
	Models(ctx context.Context, channelID int64, from, to time.Time) ([]channelops.ModelRow, error)
	Routes(ctx context.Context, channelID int64) ([]channelops.RouteRow, error)
}

type channelOpsHandler struct {
	service ChannelOpsService
	// breaker 可选：从 gateway 拉取进程内熔断快照并挂到列表行；nil 则不填充。
	breaker *gatewayruntime.Client
}

type channelOpsRowDTO struct {
	ID                      int64                     `json:"id"`
	Name                    string                    `json:"name"`
	Status                  string                    `json:"status"`
	CreatedAt               string                    `json:"created_at"`
	Protocol                string                    `json:"protocol"`
	AdapterKey              string                    `json:"adapter_key"`
	BaseURL                 string                    `json:"base_url"`
	Priority                int32                     `json:"priority"`
	TimeoutMs               *int32                    `json:"timeout_ms"`
	ProviderName            string                    `json:"provider_name"`
	Credential              string                    `json:"credential"`
	AttemptTotal            int64                     `json:"attempt_total"`
	AttemptSucceeded        int64                     `json:"attempt_succeeded"`
	SuccessRate             float64                   `json:"success_rate"`
	TimeoutTotal            int64                     `json:"timeout_total"`
	Latency                 adminhttp.LatencyStatsDTO `json:"latency"`
	Health                  string                    `json:"health"`
	BoundModels             int64                     `json:"bound_models"`
	BoundRoutes             int64                     `json:"bound_routes"`
	RecentErrorCode         string                    `json:"recent_error_code"`
	RpmLimit                *int32                    `json:"rpm_limit"`
	TpmLimit                *int32                    `json:"tpm_limit"`
	RpdLimit                *int32                    `json:"rpd_limit"`
	LastTestedAt            *string                   `json:"last_tested_at"`
	LastTestOK              *bool                     `json:"last_test_ok"`
	LastTestLatencyMs       *int32                    `json:"last_test_latency_ms"`
	LastTestError           string                    `json:"last_test_error"`
	CredentialValid         bool                      `json:"credential_valid"`
	CostMultiplier          *string                   `json:"cost_multiplier"`
	CostMultiplierOverrides int64                     `json:"cost_multiplier_overrides"`
	RechargeFactor          *string                   `json:"recharge_factor"`
	// CircuitBreaker 来自 gateway 熔断快照；无快照时前端按闭合（绿）常驻显示。
	CircuitBreaker *channelCircuitBreakerDTO `json:"circuit_breaker,omitempty"`
}

type channelCircuitBreakerDTO struct {
	State            string                             `json:"state"`
	Failures         int                                `json:"failures"`
	Successes        int                                `json:"successes"`
	WindowStart      *string                            `json:"window_start,omitempty"`
	OpenedAt         *string                            `json:"opened_at,omitempty"`
	OpenRemainingMs  *int64                             `json:"open_remaining_ms,omitempty"`
	HalfOpenInFlight bool                               `json:"half_open_in_flight"`
	HealthScore      float64                            `json:"health_score"`
	ObservedAt       string                             `json:"observed_at"`
	Instances        []channelCircuitBreakerInstanceDTO `json:"instances,omitempty"`
}

type channelCircuitBreakerInstanceDTO struct {
	ID               string `json:"id"`
	State            string `json:"state"`
	OpenRemainingMs  *int64 `json:"open_remaining_ms,omitempty"`
	HalfOpenInFlight bool   `json:"half_open_in_flight"`
	Failures         int    `json:"failures"`
	Successes        int    `json:"successes"`
}

type channelOpsDetailDTO struct {
	AttemptTotal     int64                     `json:"attempt_total"`
	AttemptSucceeded int64                     `json:"attempt_succeeded"`
	SuccessRate      float64                   `json:"success_rate"`
	TimeoutTotal     int64                     `json:"timeout_total"`
	Latency          adminhttp.LatencyStatsDTO `json:"latency"`
	LastSuccessAt    *string                   `json:"last_success_at"`
	LastFailureAt    *string                   `json:"last_failure_at"`
	// CircuitBreaker 来自 gateway 快照；无快照时省略，前端按闭合显示。
	CircuitBreaker *channelCircuitBreakerDTO `json:"circuit_breaker,omitempty"`
}

type channelOpsPerfPointDTO struct {
	Bucket           string  `json:"bucket"`
	AttemptTotal     int64   `json:"attempt_total"`
	AttemptSucceeded int64   `json:"attempt_succeeded"`
	LatencyAvg       float64 `json:"latency_avg"`
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
	ModelID          int64                     `json:"model_id"`
	ModelRef         string                    `json:"model_ref"`
	DisplayName      string                    `json:"display_name"`
	UpstreamModel    string                    `json:"upstream_model"`
	Status           string                    `json:"status"`
	AttemptTotal     int64                     `json:"attempt_total"`
	AttemptSucceeded int64                     `json:"attempt_succeeded"`
	SuccessRate      float64                   `json:"success_rate"`
	Latency          adminhttp.LatencyStatsDTO `json:"latency"`
	HasPrice         bool                      `json:"has_price"`
}

type channelOpsRouteDTO struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Mode       string `json:"mode"`
	Status     string `json:"status"`
	PriceRatio string `json:"price_ratio"`
}

func (h *channelOpsHandler) table(w http.ResponseWriter, r *http.Request) {
	from, to, _, err := adminhttp.RangeWindow(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	page := adminhttp.ParsePage(r)
	providerID, err := adminhttp.OptionalInt64Query(r, "provider_id")
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	sort, err := adminhttp.ParseListSort(r, map[string]struct{}{
		"name":             {},
		"requests":         {},
		"success_rate":     {},
		"latency":          {},
		"timeout":          {},
		"bound_models":     {},
		"status":           {},
		"credential_valid": {},
		"created_at":       {},
	}, "success_rate", false)
	if err != nil {
		adminhttp.WriteSortError(w, err)
		return
	}
	field, desc := sort.SQLParams()
	rows, total, err := h.service.Table(r.Context(), channelops.TableParams{
		From:       from,
		To:         to,
		Status:     adminhttp.ListStatus(r),
		ProviderID: providerID,
		Search:     adminhttp.QueryString(r, "search"),
		SortField:  field,
		SortDesc:   desc,
		Limit:      page.Limit(),
		Offset:     page.Offset(),
	})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	breakerByID := map[int64]gatewayruntime.ChannelStatus{}
	if h.breaker != nil {
		breakerByID = h.breaker.Statuses(r.Context())
	}
	dtos := make([]channelOpsRowDTO, 0, len(rows))
	for _, row := range rows {
		dto := channelOpsRowDTO{
			ID:                      row.ID,
			Name:                    row.Name,
			Status:                  row.Status,
			CreatedAt:               adminhttp.RFC3339(row.CreatedAt),
			Protocol:                row.Protocol,
			AdapterKey:              row.AdapterKey,
			BaseURL:                 row.BaseURL,
			Priority:                row.Priority,
			TimeoutMs:               row.TimeoutMs,
			ProviderName:            row.ProviderName,
			Credential:              row.Credential,
			AttemptTotal:            row.AttemptTotal,
			AttemptSucceeded:        row.AttemptSucceeded,
			SuccessRate:             row.SuccessRate,
			TimeoutTotal:            row.TimeoutTotal,
			Latency:                 adminhttp.LatencyStatsFrom(row.Latency),
			Health:                  row.HealthBucket,
			BoundModels:             row.BoundModels,
			BoundRoutes:             row.BoundRoutes,
			RecentErrorCode:         row.RecentErrorCode,
			RpmLimit:                row.RpmLimit,
			TpmLimit:                row.TpmLimit,
			RpdLimit:                row.RpdLimit,
			LastTestedAt:            adminhttp.RFC3339Ptr(row.LastTestedAt),
			LastTestOK:              row.LastTestOK,
			LastTestLatencyMs:       row.LastTestLatencyMs,
			LastTestError:           row.LastTestError,
			CredentialValid:         row.CredentialValid,
			CostMultiplier:          row.CostMultiplier,
			CostMultiplierOverrides: row.CostMultiplierOverrides,
			RechargeFactor:          row.RechargeFactor,
		}
		if st, ok := breakerByID[row.ID]; ok {
			dto.CircuitBreaker = toCircuitBreakerDTO(st)
		}
		dtos = append(dtos, dto)
	}
	adminhttp.WriteList(w, http.StatusOK, dtos, page, total)
}

func toCircuitBreakerDTO(st gatewayruntime.ChannelStatus) *channelCircuitBreakerDTO {
	dto := &channelCircuitBreakerDTO{
		State:            string(st.State),
		Failures:         st.Failures,
		Successes:        st.Successes,
		WindowStart:      adminhttp.RFC3339Ptr(st.WindowStart),
		OpenedAt:         adminhttp.RFC3339Ptr(st.OpenedAt),
		OpenRemainingMs:  st.OpenRemainingMs,
		HalfOpenInFlight: st.HalfOpenInFlight,
		HealthScore:      st.HealthScore,
		ObservedAt:       adminhttp.RFC3339(st.ObservedAt),
	}
	if st.State == "" {
		dto.State = string(lifecycle.CircuitStateClosed)
	}
	if len(st.Instances) > 0 {
		dto.Instances = make([]channelCircuitBreakerInstanceDTO, 0, len(st.Instances))
		for _, inst := range st.Instances {
			dto.Instances = append(dto.Instances, channelCircuitBreakerInstanceDTO{
				ID:               inst.ID,
				State:            string(inst.State),
				OpenRemainingMs:  inst.OpenRemainingMs,
				HalfOpenInFlight: inst.HalfOpenInFlight,
				Failures:         inst.Failures,
				Successes:        inst.Successes,
			})
		}
	}
	return dto
}

func (h *channelOpsHandler) detail(w http.ResponseWriter, r *http.Request) {
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
	dto := channelOpsDetailDTO{
		AttemptTotal:     d.AttemptTotal,
		AttemptSucceeded: d.AttemptSucceeded,
		SuccessRate:      d.SuccessRate,
		TimeoutTotal:     d.TimeoutTotal,
		Latency:          adminhttp.LatencyStatsFrom(d.Latency),
		LastSuccessAt:    adminhttp.RFC3339Ptr(d.LastSuccessAt),
		LastFailureAt:    adminhttp.RFC3339Ptr(d.LastFailureAt),
	}
	if h.breaker != nil {
		if st, ok := h.breaker.Statuses(r.Context())[id]; ok {
			dto.CircuitBreaker = toCircuitBreakerDTO(st)
		}
	}
	adminhttp.WriteData(w, http.StatusOK, dto)
}

func (h *channelOpsHandler) performance(w http.ResponseWriter, r *http.Request) {
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
	out := make([]channelOpsPerfPointDTO, 0, len(points))
	for _, p := range points {
		out = append(out, channelOpsPerfPointDTO{Bucket: adminhttp.RFC3339(p.Bucket), AttemptTotal: p.AttemptTotal, AttemptSucceeded: p.AttemptSucceeded, LatencyAvg: p.LatencyAvg})
	}
	adminhttp.WriteData(w, http.StatusOK, out)
}

func (h *channelOpsHandler) errors(w http.ResponseWriter, r *http.Request) {
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
	out := make([]channelOpsErrorDTO, 0, len(rows))
	for _, e := range rows {
		out = append(out, channelOpsErrorDTO{
			At:                 adminhttp.RFC3339(e.At),
			UpstreamModel:      e.UpstreamModel,
			ErrorCode:          e.ErrorCode,
			UpstreamStatusCode: e.UpstreamStatusCode,
			ErrorMessage:       e.ErrorMessage,
			RequestID:          e.RequestID,
		})
	}
	adminhttp.WriteList(w, http.StatusOK, out, page, total)
}

func (h *channelOpsHandler) models(w http.ResponseWriter, r *http.Request) {
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
	rows, err := h.service.Models(r.Context(), id, from, to)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
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
			Latency:          adminhttp.LatencyStatsFrom(m.Latency),
			HasPrice:         m.HasPrice,
		})
	}
	adminhttp.WriteData(w, http.StatusOK, out)
}

func (h *channelOpsHandler) routes(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	rows, err := h.service.Routes(r.Context(), id)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	out := make([]channelOpsRouteDTO, 0, len(rows))
	for _, rt := range rows {
		out = append(out, channelOpsRouteDTO{
			ID:         rt.ID,
			Name:       rt.Name,
			Mode:       rt.Mode,
			Status:     rt.Status,
			PriceRatio: rt.PriceRatio,
		})
	}
	adminhttp.WriteData(w, http.StatusOK, out)
}
