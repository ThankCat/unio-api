package overview

import (
	"context"
	"net/http"
	"time"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi/adminhttp"

	"github.com/ThankCat/unio-gateway/internal/service/admin/dashboard"
)

// defaultDashboardWindow 是 from/to 均缺省时的默认回看窗口（近 7 天）。
const defaultDashboardWindow = 7 * 24 * time.Hour

// DashboardService 定义 adminapi 工作台看板（M9 只读聚合）所需的最小能力。
type DashboardService interface {
	Timeseries(ctx context.Context, metric, interval string, from, to time.Time) (dashboard.Series, error)

	// §3.1 概览重构：雷达 / 分组表现 / 性能时序。
	Radar(ctx context.Context, from, to time.Time) (dashboard.RadarReport, error)
	Breakdown(ctx context.Context, dimension string, from, to time.Time) ([]dashboard.BreakdownRow, error)
	PerformanceTimeseries(ctx context.Context, interval string, from, to time.Time) ([]dashboard.PerformancePoint, error)
	TopErrors(ctx context.Context, from, to time.Time) ([]dashboard.ErrorGroup, error)
}

type dashboardTokensDTO struct {
	Input  int64 `json:"input"`
	Output int64 `json:"output"`
	Total  int64 `json:"total"`
}

// dashboardSeriesDTO 是时间序列响应体；points 形状随 metric 而定。
type dashboardSeriesDTO struct {
	Metric   string `json:"metric"`
	Interval string `json:"interval"`
	From     string `json:"from"`
	To       string `json:"to"`
	Points   any    `json:"points"`
}

type requestPointDTO struct {
	Bucket    string `json:"bucket"`
	Total     int64  `json:"total"`
	Succeeded int64  `json:"succeeded"`
}

type tokenPointDTO struct {
	Bucket string `json:"bucket"`
	Input  int64  `json:"input"`
	Output int64  `json:"output"`
}

type spendPointDTO struct {
	Bucket   string `json:"bucket"`
	Currency string `json:"currency"`
	Amount   string `json:"amount"`
}

type dashboardHandler struct {
	service DashboardService
}

func (h *dashboardHandler) timeseries(w http.ResponseWriter, r *http.Request) {
	from, to, err := dashboardRange(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	// metric/interval 合法性由 service 校验（非法返回 admin_invalid_argument → 400）。
	series, err := h.service.Timeseries(r.Context(), adminhttp.QueryString(r, "metric"), adminhttp.QueryString(r, "interval"), from, to)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, toDashboardSeriesDTO(series))
}

// dashboardRange 解析 from/to（RFC3339）；缺省默认近 7 天（to=now，from=to-7d）。
func dashboardRange(r *http.Request) (time.Time, time.Time, error) {
	fromPtr, err := adminhttp.OptionalTimeQuery(r, "from")
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	toPtr, err := adminhttp.OptionalTimeQuery(r, "to")
	if err != nil {
		return time.Time{}, time.Time{}, err
	}

	to := time.Now()
	if toPtr != nil {
		to = *toPtr
	}
	from := to.Add(-defaultDashboardWindow)
	if fromPtr != nil {
		from = *fromPtr
	}
	return from, to, nil
}

func toDashboardSeriesDTO(s dashboard.Series) dashboardSeriesDTO {
	dto := dashboardSeriesDTO{Metric: s.Metric, Interval: s.Interval, From: adminhttp.RFC3339(s.From), To: adminhttp.RFC3339(s.To)}
	switch s.Metric {
	case dashboard.MetricRequests:
		points := make([]requestPointDTO, 0, len(s.RequestPoints))
		for _, p := range s.RequestPoints {
			points = append(points, requestPointDTO{Bucket: adminhttp.RFC3339(p.Bucket), Total: p.Total, Succeeded: p.Succeeded})
		}
		dto.Points = points
	case dashboard.MetricTokens:
		points := make([]tokenPointDTO, 0, len(s.TokenPoints))
		for _, p := range s.TokenPoints {
			points = append(points, tokenPointDTO{Bucket: adminhttp.RFC3339(p.Bucket), Input: p.Input, Output: p.Output})
		}
		dto.Points = points
	case dashboard.MetricSpend:
		points := make([]spendPointDTO, 0, len(s.SpendPoints))
		for _, p := range s.SpendPoints {
			points = append(points, spendPointDTO{Bucket: adminhttp.RFC3339(p.Bucket), Currency: p.Currency, Amount: p.Amount})
		}
		dto.Points = points
	case dashboard.MetricCost:
		points := make([]spendPointDTO, 0, len(s.CostPoints))
		for _, p := range s.CostPoints {
			points = append(points, spendPointDTO{Bucket: adminhttp.RFC3339(p.Bucket), Currency: p.Currency, Amount: p.Amount})
		}
		dto.Points = points
	}
	return dto
}

// ---- §3.1 概览重构：radar / breakdown / performance ----

type rangeWindowDTO struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type ttftStatsDTO struct {
	Avg      float64 `json:"avg"`
	P50      float64 `json:"p50"`
	P90      float64 `json:"p90"`
	P95      float64 `json:"p95"`
	P99      float64 `json:"p99"`
	Sample   int64   `json:"sample"`
	Coverage float64 `json:"coverage"`
	HasData  bool    `json:"has_data"`
}

type cacheStatsDTO struct {
	ReadRate            float64 `json:"read_rate"`
	WriteRate           float64 `json:"write_rate"`
	InputTokens         int64   `json:"input_tokens"`
	UncachedTokens      int64   `json:"uncached_tokens"`
	CacheReadTokens     int64   `json:"cache_read_tokens"`
	CacheWrite5mTokens  int64   `json:"cache_write_5m_tokens"`
	CacheWrite1hTokens  int64   `json:"cache_write_1h_tokens"`
	CacheWrite30mTokens int64   `json:"cache_write_30m_tokens"`
}

type radarRequestsDTO struct {
	Total       int64   `json:"total"`
	Succeeded   int64   `json:"succeeded"`
	Failed      int64   `json:"failed"`
	Canceled    int64   `json:"canceled"`
	SuccessRate float64 `json:"success_rate"`
	ErrorRate   float64 `json:"error_rate"`
	Timeout     int64   `json:"timeout"`
}

type settlementBacklogDTO struct {
	Active int64 `json:"active"`
	Dead   int64 `json:"dead"`
}

type radarBillingExceptionDTO struct {
	Total  int64  `json:"total"`
	Amount string `json:"amount"`
}

type actionItemDTO struct {
	Kind     string `json:"kind"`
	Severity string `json:"severity"`
	Title    string `json:"title"`
	Detail   string `json:"detail"`
	Deeplink string `json:"deeplink"`
}

type badChannelDTO struct {
	ChannelID        int64   `json:"channel_id"`
	Name             string  `json:"name"`
	Status           string  `json:"status"`
	AttemptTotal     int64   `json:"attempt_total"`
	AttemptSucceeded int64   `json:"attempt_succeeded"`
	SuccessRate      float64 `json:"success_rate"`
	Bucket           string  `json:"bucket"`
	RecentErrorCode  string  `json:"recent_error_code"`
}

type radarDTO struct {
	Range             rangeWindowDTO            `json:"range"`
	Requests          radarRequestsDTO          `json:"requests"`
	Latency           adminhttp.LatencyStatsDTO `json:"latency"`
	Ttft              ttftStatsDTO              `json:"ttft"`
	TPS               float64                   `json:"tps"`
	Tokens            dashboardTokensDTO        `json:"tokens"`
	Cache             cacheStatsDTO             `json:"cache"`
	RevenueUSD        string                    `json:"revenue_usd"`
	CostUSD           string                    `json:"cost_usd"`
	MarginUSD         string                    `json:"margin_usd"`
	BillingExceptions radarBillingExceptionDTO  `json:"billing_exceptions"`
	Settlement        settlementBacklogDTO      `json:"settlement_backlog"`
	ActionItems       []actionItemDTO           `json:"action_items"`
	BadChannels       []badChannelDTO           `json:"bad_channels"`
}

type breakdownRowDTO struct {
	Label          string                    `json:"label"`
	RefID          *int64                    `json:"ref_id"`
	Status         string                    `json:"status"`
	Terminal       int64                     `json:"terminal"`
	Succeeded      int64                     `json:"succeeded"`
	Failed         int64                     `json:"failed"`
	SuccessRate    float64                   `json:"success_rate"`
	Tokens         int64                     `json:"tokens"`
	RevenueUSD     string                    `json:"revenue_usd"`
	CostUSD        string                    `json:"cost_usd"`
	MarginUSD      string                    `json:"margin_usd"`
	Latency        adminhttp.LatencyStatsDTO `json:"latency"`
	LatencyP95     float64                   `json:"latency_p95"`
	AvgTPS         float64                   `json:"avg_tps"`
	HealthBucket   string                    `json:"health_bucket"`
	RecentError    string                    `json:"recent_error"`
	ChannelCount   int64                     `json:"channel_count"`
	SuccessBuckets []successBucketDTO        `json:"success_buckets,omitempty"`
}

type successBucketDTO struct {
	Bucket      string  `json:"bucket"`
	Terminal    int64   `json:"terminal"`
	Succeeded   int64   `json:"succeeded"`
	SuccessRate float64 `json:"success_rate"`
}

type breakdownDTO struct {
	Dimension string            `json:"dimension"`
	Rows      []breakdownRowDTO `json:"rows"`
}

type errorGroupDTO struct {
	Code  string  `json:"code"`
	Total int64   `json:"total"`
	Share float64 `json:"share"`
}

type topErrorsDTO struct {
	Errors []errorGroupDTO `json:"errors"`
}

type performancePointDTO struct {
	Bucket     string  `json:"bucket"`
	LatencyP95 float64 `json:"latency_p95"`
	TtftP95    float64 `json:"ttft_p95"`
	TPS        float64 `json:"tps"`
}

type performanceSeriesDTO struct {
	Interval string                `json:"interval"`
	From     string                `json:"from"`
	To       string                `json:"to"`
	Points   []performancePointDTO `json:"points"`
}

func (h *dashboardHandler) radar(w http.ResponseWriter, r *http.Request) {
	from, to, _, err := adminhttp.RangeWindow(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	report, err := h.service.Radar(r.Context(), from, to)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, toRadarDTO(report))
}

func (h *dashboardHandler) breakdown(w http.ResponseWriter, r *http.Request) {
	from, to, _, err := adminhttp.RangeWindow(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	dimension := adminhttp.QueryString(r, "dimension")
	rows, err := h.service.Breakdown(r.Context(), dimension, from, to)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	out := make([]breakdownRowDTO, 0, len(rows))
	for _, row := range rows {
		successBuckets := make([]successBucketDTO, 0, len(row.SuccessBuckets))
		for _, bucket := range row.SuccessBuckets {
			successBuckets = append(successBuckets, successBucketDTO{
				Bucket:      bucket.Bucket.Format(time.RFC3339),
				Terminal:    bucket.Terminal,
				Succeeded:   bucket.Succeeded,
				SuccessRate: bucket.SuccessRate,
			})
		}
		out = append(out, breakdownRowDTO{
			Label:       row.Label,
			RefID:       row.RefID,
			Status:      row.Status,
			Terminal:    row.Terminal,
			Succeeded:   row.Succeeded,
			Failed:      row.Failed,
			SuccessRate: row.SuccessRate,
			Tokens:      row.Tokens,
			RevenueUSD:  row.RevenueUSD,
			CostUSD:     row.CostUSD,
			MarginUSD:   row.MarginUSD,
			Latency: adminhttp.LatencyStatsDTO{
				Avg:      row.Latency.Avg,
				P50:      row.Latency.P50,
				P90:      row.Latency.P90,
				P95:      row.Latency.P95,
				P99:      row.Latency.P99,
				Sample:   row.Latency.Sample,
				Coverage: row.Latency.Coverage,
			},
			LatencyP95:     row.LatencyP95,
			AvgTPS:         row.AvgTPS,
			HealthBucket:   row.HealthBucket,
			RecentError:    row.RecentError,
			ChannelCount:   row.ChannelCount,
			SuccessBuckets: successBuckets,
		})
	}
	adminhttp.WriteData(w, http.StatusOK, breakdownDTO{Dimension: dimension, Rows: out})
}

func (h *dashboardHandler) topErrors(w http.ResponseWriter, r *http.Request) {
	from, to, _, err := adminhttp.RangeWindow(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	groups, err := h.service.TopErrors(r.Context(), from, to)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	out := make([]errorGroupDTO, 0, len(groups))
	for _, g := range groups {
		out = append(out, errorGroupDTO{Code: g.Code, Total: g.Total, Share: g.Share})
	}
	adminhttp.WriteData(w, http.StatusOK, topErrorsDTO{Errors: out})
}

func (h *dashboardHandler) performanceTimeseries(w http.ResponseWriter, r *http.Request) {
	from, to, interval, err := adminhttp.RangeWindow(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	if q := adminhttp.QueryString(r, "interval"); q != "" {
		interval = q
	}
	points, err := h.service.PerformanceTimeseries(r.Context(), interval, from, to)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	out := make([]performancePointDTO, 0, len(points))
	for _, p := range points {
		out = append(out, performancePointDTO{
			Bucket:     adminhttp.RFC3339(p.Bucket),
			LatencyP95: p.LatencyP95,
			TtftP95:    p.TtftP95,
			TPS:        p.TPS,
		})
	}
	adminhttp.WriteData(w, http.StatusOK, performanceSeriesDTO{Interval: interval, From: adminhttp.RFC3339(from), To: adminhttp.RFC3339(to), Points: out})
}

func toRadarDTO(r dashboard.RadarReport) radarDTO {
	dto := radarDTO{
		Range: rangeWindowDTO{From: adminhttp.RFC3339(r.From), To: adminhttp.RFC3339(r.To)},
		Requests: radarRequestsDTO{
			Total:       r.Requests.Total,
			Succeeded:   r.Requests.Succeeded,
			Failed:      r.Requests.Failed,
			Canceled:    r.Requests.Canceled,
			SuccessRate: r.Requests.SuccessRate,
			ErrorRate:   r.Requests.ErrorRate,
			Timeout:     r.Timeout,
		},
		Latency: adminhttp.LatencyStatsDTO{
			Avg:      r.Latency.Avg,
			P50:      r.Latency.P50,
			P90:      r.Latency.P90,
			P95:      r.Latency.P95,
			P99:      r.Latency.P99,
			Sample:   r.Latency.Sample,
			Coverage: r.Latency.Coverage,
		},
		Ttft: ttftStatsDTO{
			Avg:      r.Ttft.Avg,
			P50:      r.Ttft.P50,
			P90:      r.Ttft.P90,
			P95:      r.Ttft.P95,
			P99:      r.Ttft.P99,
			Sample:   r.Ttft.Sample,
			Coverage: r.Ttft.Coverage,
			HasData:  r.Ttft.HasData,
		},
		TPS:    r.TPS,
		Tokens: dashboardTokensDTO{Input: r.Tokens.Input, Output: r.Tokens.Output, Total: r.Tokens.Total},
		Cache: cacheStatsDTO{
			ReadRate:            r.Cache.ReadRate,
			WriteRate:           r.Cache.WriteRate,
			InputTokens:         r.Cache.InputTokens,
			UncachedTokens:      r.Cache.UncachedTokens,
			CacheReadTokens:     r.Cache.CacheReadTokens,
			CacheWrite5mTokens:  r.Cache.CacheWrite5mTokens,
			CacheWrite1hTokens:  r.Cache.CacheWrite1hTokens,
			CacheWrite30mTokens: r.Cache.CacheWrite30mTokens,
		},

		RevenueUSD:        r.RevenueUSD,
		CostUSD:           r.CostUSD,
		MarginUSD:         r.MarginUSD,
		BillingExceptions: radarBillingExceptionDTO{Total: r.BillingExceptionTotal, Amount: r.BillingExceptionAmount},
		Settlement:        settlementBacklogDTO{Active: r.Settlement.Active, Dead: r.Settlement.Dead},
	}

	dto.ActionItems = make([]actionItemDTO, 0, len(r.ActionItems))
	for _, a := range r.ActionItems {
		dto.ActionItems = append(dto.ActionItems, actionItemDTO{Kind: a.Kind, Severity: a.Severity, Title: a.Title, Detail: a.Detail, Deeplink: a.Deeplink})
	}
	dto.BadChannels = make([]badChannelDTO, 0, len(r.BadChannels))
	for _, b := range r.BadChannels {
		dto.BadChannels = append(dto.BadChannels, badChannelDTO{
			ChannelID:        b.ChannelID,
			Name:             b.Name,
			Status:           b.Status,
			AttemptTotal:     b.AttemptTotal,
			AttemptSucceeded: b.AttemptSucceeded,
			SuccessRate:      b.SuccessRate,
			Bucket:           b.Bucket,
			RecentErrorCode:  b.RecentErrorCode,
		})
	}
	return dto
}
