package adminapi

import (
	"context"
	"net/http"
	"time"

	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/service/admin/dashboard"
)

// defaultDashboardWindow 是 from/to 均缺省时的默认回看窗口（近 7 天）。
const defaultDashboardWindow = 7 * 24 * time.Hour

// DashboardService 定义 adminapi 工作台看板（M9 只读聚合）所需的最小能力。
type DashboardService interface {
	Overview(ctx context.Context, from, to time.Time) (dashboard.Overview, error)
	Timeseries(ctx context.Context, metric, interval string, from, to time.Time) (dashboard.Series, error)

	// §3.1 概览重构：雷达 / 分组表现 / 性能时序。
	Radar(ctx context.Context, from, to, statusFrom, statusTo time.Time) (dashboard.RadarReport, error)
	Breakdown(ctx context.Context, dimension string, from, to time.Time) ([]dashboard.BreakdownRow, error)
	PerformanceTimeseries(ctx context.Context, interval string, from, to time.Time) ([]dashboard.PerformancePoint, error)
}

// 概览页时间预设窗口（§3.1）：状态短窗口固定 15 分钟，与页面 range 解耦。
const dashboardStatusWindow = 15 * time.Minute

// dashboardOverviewDTO 是首屏 KPI 概览响应体。
type dashboardOverviewDTO struct {
	From              string                 `json:"from"`
	To                string                 `json:"to"`
	Requests          dashboardRequestsDTO   `json:"requests"`
	Tokens            dashboardTokensDTO     `json:"tokens"`
	Revenue           []moneyByCurrencyDTO   `json:"revenue"`
	Cost              []moneyByCurrencyDTO   `json:"cost"`
	Margin            []moneyByCurrencyDTO   `json:"margin"`
	Balance           []balanceByCurrencyDTO `json:"balance"`
	BillingExceptions []exceptionGroupDTO    `json:"billing_exceptions"`
	Channels          channelStatsDTO        `json:"channels"`
}

type dashboardRequestsDTO struct {
	Total       int64   `json:"total"`
	Succeeded   int64   `json:"succeeded"`
	Failed      int64   `json:"failed"`
	Canceled    int64   `json:"canceled"`
	Pending     int64   `json:"pending"`
	Running     int64   `json:"running"`
	SuccessRate float64 `json:"success_rate"`
	ErrorRate   float64 `json:"error_rate"`
}

type dashboardTokensDTO struct {
	Input  int64 `json:"input"`
	Output int64 `json:"output"`
	Total  int64 `json:"total"`
}

type moneyByCurrencyDTO struct {
	Currency string `json:"currency"`
	Amount   string `json:"amount"`
}

type balanceByCurrencyDTO struct {
	Currency  string `json:"currency"`
	Balance   string `json:"balance"`
	Reserved  string `json:"reserved"`
	Available string `json:"available"`
}

type exceptionGroupDTO struct {
	EventType      string `json:"event_type"`
	Total          int64  `json:"total"`
	PlatformAmount string `json:"platform_amount"`
}

type channelStatsDTO struct {
	EnabledCount int64              `json:"enabled_count"`
	Healthy      int                `json:"healthy"`
	Degraded     int                `json:"degraded"`
	Unhealthy    int                `json:"unhealthy"`
	NoData       int                `json:"no_data"`
	Channels     []channelHealthDTO `json:"channels"`
}

type channelHealthDTO struct {
	ChannelID        int64   `json:"channel_id"`
	Name             string  `json:"name"`
	Status           string  `json:"status"`
	AttemptTotal     int64   `json:"attempt_total"`
	AttemptSucceeded int64   `json:"attempt_succeeded"`
	SuccessRate      float64 `json:"success_rate"`
	Bucket           string  `json:"bucket"`
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

func (h *dashboardHandler) overview(w http.ResponseWriter, r *http.Request) {
	from, to, err := dashboardRange(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	overview, err := h.service.Overview(r.Context(), from, to)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeData(w, http.StatusOK, toDashboardOverviewDTO(overview))
}

func (h *dashboardHandler) timeseries(w http.ResponseWriter, r *http.Request) {
	from, to, err := dashboardRange(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	// metric/interval 合法性由 service 校验（非法返回 admin_invalid_argument → 400）。
	series, err := h.service.Timeseries(r.Context(), queryString(r, "metric"), queryString(r, "interval"), from, to)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeData(w, http.StatusOK, toDashboardSeriesDTO(series))
}

// dashboardRange 解析 from/to（RFC3339）；缺省默认近 7 天（to=now，from=to-7d）。
func dashboardRange(r *http.Request) (time.Time, time.Time, error) {
	fromPtr, err := optionalTimeQuery(r, "from")
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	toPtr, err := optionalTimeQuery(r, "to")
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

func toDashboardOverviewDTO(o dashboard.Overview) dashboardOverviewDTO {
	return dashboardOverviewDTO{
		From: rfc3339(o.From),
		To:   rfc3339(o.To),
		Requests: dashboardRequestsDTO{
			Total:       o.Requests.Total,
			Succeeded:   o.Requests.Succeeded,
			Failed:      o.Requests.Failed,
			Canceled:    o.Requests.Canceled,
			Pending:     o.Requests.Pending,
			Running:     o.Requests.Running,
			SuccessRate: o.Requests.SuccessRate,
			ErrorRate:   o.Requests.ErrorRate,
		},
		Tokens: dashboardTokensDTO{
			Input:  o.Tokens.Input,
			Output: o.Tokens.Output,
			Total:  o.Tokens.Total,
		},
		Revenue:           toMoneyDTOs(o.Revenue),
		Cost:              toMoneyDTOs(o.Cost),
		Margin:            toMoneyDTOs(o.Margin),
		Balance:           toBalanceDTOs(o.Balance),
		BillingExceptions: toExceptionDTOs(o.BillingExceptions),
		Channels:          toChannelStatsDTO(o.Channels),
	}
}

func toMoneyDTOs(in []dashboard.MoneyByCurrency) []moneyByCurrencyDTO {
	out := make([]moneyByCurrencyDTO, 0, len(in))
	for _, m := range in {
		out = append(out, moneyByCurrencyDTO{Currency: m.Currency, Amount: m.Amount})
	}
	return out
}

func toBalanceDTOs(in []dashboard.BalanceByCurrency) []balanceByCurrencyDTO {
	out := make([]balanceByCurrencyDTO, 0, len(in))
	for _, b := range in {
		out = append(out, balanceByCurrencyDTO{Currency: b.Currency, Balance: b.Balance, Reserved: b.Reserved, Available: b.Available})
	}
	return out
}

func toExceptionDTOs(in []dashboard.ExceptionGroup) []exceptionGroupDTO {
	out := make([]exceptionGroupDTO, 0, len(in))
	for _, e := range in {
		out = append(out, exceptionGroupDTO{EventType: e.EventType, Total: e.Total, PlatformAmount: e.PlatformAmount})
	}
	return out
}

func toChannelStatsDTO(c dashboard.ChannelStats) channelStatsDTO {
	channels := make([]channelHealthDTO, 0, len(c.Channels))
	for _, ch := range c.Channels {
		channels = append(channels, channelHealthDTO{
			ChannelID:        ch.ChannelID,
			Name:             ch.Name,
			Status:           ch.Status,
			AttemptTotal:     ch.AttemptTotal,
			AttemptSucceeded: ch.AttemptSucceeded,
			SuccessRate:      ch.SuccessRate,
			Bucket:           ch.Bucket,
		})
	}
	return channelStatsDTO{
		EnabledCount: c.EnabledCount,
		Healthy:      c.Healthy,
		Degraded:     c.Degraded,
		Unhealthy:    c.Unhealthy,
		NoData:       c.NoData,
		Channels:     channels,
	}
}

func toDashboardSeriesDTO(s dashboard.Series) dashboardSeriesDTO {
	dto := dashboardSeriesDTO{Metric: s.Metric, Interval: s.Interval, From: rfc3339(s.From), To: rfc3339(s.To)}
	switch s.Metric {
	case dashboard.MetricRequests:
		points := make([]requestPointDTO, 0, len(s.RequestPoints))
		for _, p := range s.RequestPoints {
			points = append(points, requestPointDTO{Bucket: rfc3339(p.Bucket), Total: p.Total, Succeeded: p.Succeeded})
		}
		dto.Points = points
	case dashboard.MetricTokens:
		points := make([]tokenPointDTO, 0, len(s.TokenPoints))
		for _, p := range s.TokenPoints {
			points = append(points, tokenPointDTO{Bucket: rfc3339(p.Bucket), Input: p.Input, Output: p.Output})
		}
		dto.Points = points
	case dashboard.MetricSpend:
		points := make([]spendPointDTO, 0, len(s.SpendPoints))
		for _, p := range s.SpendPoints {
			points = append(points, spendPointDTO{Bucket: rfc3339(p.Bucket), Currency: p.Currency, Amount: p.Amount})
		}
		dto.Points = points
	case dashboard.MetricCost:
		points := make([]spendPointDTO, 0, len(s.CostPoints))
		for _, p := range s.CostPoints {
			points = append(points, spendPointDTO{Bucket: rfc3339(p.Bucket), Currency: p.Currency, Amount: p.Amount})
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

type platformStatusDTO struct {
	Level       string  `json:"level"`
	Reason      string  `json:"reason"`
	WindowFrom  string  `json:"window_from"`
	WindowTo    string  `json:"window_to"`
	Terminal    int64   `json:"terminal"`
	Succeeded   int64   `json:"succeeded"`
	SuccessRate float64 `json:"success_rate"`
	NoChannel   int64   `json:"no_channel"`
	Timeout     int64   `json:"timeout"`
}

type latencyStatsDTO struct {
	Avg float64 `json:"avg"`
	P50 float64 `json:"p50"`
	P90 float64 `json:"p90"`
	P95 float64 `json:"p95"`
	P99 float64 `json:"p99"`
}

type ttftStatsDTO struct {
	P50     float64 `json:"p50"`
	P95     float64 `json:"p95"`
	HasData bool    `json:"has_data"`
}

type cacheStatsDTO struct {
	ReadRate    float64 `json:"read_rate"`
	WriteRate   float64 `json:"write_rate"`
	InputTokens int64   `json:"input_tokens"`
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
	Range             rangeWindowDTO           `json:"range"`
	PlatformStatus    platformStatusDTO        `json:"platform_status"`
	Requests          radarRequestsDTO         `json:"requests"`
	Latency           latencyStatsDTO          `json:"latency"`
	Ttft              ttftStatsDTO             `json:"ttft"`
	TPS               float64                  `json:"tps"`
	Tokens            dashboardTokensDTO       `json:"tokens"`
	Cache             cacheStatsDTO            `json:"cache"`
	RevenueUSD        string                   `json:"revenue_usd"`
	CostUSD           string                   `json:"cost_usd"`
	MarginUSD         string                   `json:"margin_usd"`
	BillingExceptions radarBillingExceptionDTO `json:"billing_exceptions"`
	Settlement        settlementBacklogDTO     `json:"settlement_backlog"`
	ActionItems       []actionItemDTO          `json:"action_items"`
	BadChannels       []badChannelDTO          `json:"bad_channels"`
}

type breakdownRowDTO struct {
	Label       string  `json:"label"`
	RefID       *int64  `json:"ref_id"`
	Status      string  `json:"status"`
	Terminal    int64   `json:"terminal"`
	Succeeded   int64   `json:"succeeded"`
	SuccessRate float64 `json:"success_rate"`
}

type breakdownDTO struct {
	Dimension string            `json:"dimension"`
	Rows      []breakdownRowDTO `json:"rows"`
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

// rangePreset 解析 ?range=24h|3d|7d|30d|all，返回 [from,to) 与建议时间桶。
// all → from 零值（不过滤）；缺省 24h。
func rangePreset(r *http.Request) (from, to time.Time, interval string, err error) {
	to = time.Now()
	switch queryString(r, "range") {
	case "", "24h":
		return to.Add(-24 * time.Hour), to, dashboard.IntervalHour, nil
	case "3d":
		return to.Add(-3 * 24 * time.Hour), to, dashboard.IntervalHour, nil
	case "7d":
		return to.Add(-7 * 24 * time.Hour), to, dashboard.IntervalDay, nil
	case "30d":
		return to.Add(-30 * 24 * time.Hour), to, dashboard.IntervalDay, nil
	case "all":
		return time.Time{}, to, dashboard.IntervalDay, nil
	default:
		return time.Time{}, time.Time{}, "", failure.New(
			failure.CodeAdminInvalidArgument,
			failure.WithMessage("range must be one of 24h|3d|7d|30d|all"),
			failure.WithField("field", "range"),
		)
	}
}

func (h *dashboardHandler) radar(w http.ResponseWriter, r *http.Request) {
	from, to, _, err := rangePreset(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	statusTo := time.Now()
	statusFrom := statusTo.Add(-dashboardStatusWindow)

	report, err := h.service.Radar(r.Context(), from, to, statusFrom, statusTo)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeData(w, http.StatusOK, toRadarDTO(report))
}

func (h *dashboardHandler) breakdown(w http.ResponseWriter, r *http.Request) {
	from, to, _, err := rangePreset(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	dimension := queryString(r, "dimension")
	rows, err := h.service.Breakdown(r.Context(), dimension, from, to)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	out := make([]breakdownRowDTO, 0, len(rows))
	for _, row := range rows {
		out = append(out, breakdownRowDTO{
			Label:       row.Label,
			RefID:       row.RefID,
			Status:      row.Status,
			Terminal:    row.Terminal,
			Succeeded:   row.Succeeded,
			SuccessRate: row.SuccessRate,
		})
	}
	writeData(w, http.StatusOK, breakdownDTO{Dimension: dimension, Rows: out})
}

func (h *dashboardHandler) performanceTimeseries(w http.ResponseWriter, r *http.Request) {
	from, to, interval, err := rangePreset(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	if q := queryString(r, "interval"); q != "" {
		interval = q
	}
	points, err := h.service.PerformanceTimeseries(r.Context(), interval, from, to)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	out := make([]performancePointDTO, 0, len(points))
	for _, p := range points {
		out = append(out, performancePointDTO{
			Bucket:     rfc3339(p.Bucket),
			LatencyP95: p.LatencyP95,
			TtftP95:    p.TtftP95,
			TPS:        p.TPS,
		})
	}
	writeData(w, http.StatusOK, performanceSeriesDTO{Interval: interval, From: rfc3339(from), To: rfc3339(to), Points: out})
}

func toRadarDTO(r dashboard.RadarReport) radarDTO {
	dto := radarDTO{
		Range: rangeWindowDTO{From: rfc3339(r.From), To: rfc3339(r.To)},
		PlatformStatus: platformStatusDTO{
			Level:       r.PlatformStatus.Level,
			Reason:      r.PlatformStatus.Reason,
			WindowFrom:  rfc3339(r.PlatformStatus.WindowFrom),
			WindowTo:    rfc3339(r.PlatformStatus.WindowTo),
			Terminal:    r.PlatformStatus.Terminal,
			Succeeded:   r.PlatformStatus.Succeeded,
			SuccessRate: r.PlatformStatus.SuccessRate,
			NoChannel:   r.PlatformStatus.NoChannel,
			Timeout:     r.PlatformStatus.Timeout,
		},
		Requests: radarRequestsDTO{
			Total:       r.Requests.Total,
			Succeeded:   r.Requests.Succeeded,
			Failed:      r.Requests.Failed,
			Canceled:    r.Requests.Canceled,
			SuccessRate: r.Requests.SuccessRate,
			ErrorRate:   r.Requests.ErrorRate,
			Timeout:     r.Timeout,
		},
		Latency: latencyStatsDTO{Avg: r.Latency.Avg, P50: r.Latency.P50, P90: r.Latency.P90, P95: r.Latency.P95, P99: r.Latency.P99},
		Ttft:    ttftStatsDTO{P50: r.Ttft.P50, P95: r.Ttft.P95, HasData: r.Ttft.HasData},
		TPS:     r.TPS,
		Tokens:  dashboardTokensDTO{Input: r.Tokens.Input, Output: r.Tokens.Output, Total: r.Tokens.Total},
		Cache:   cacheStatsDTO{ReadRate: r.Cache.ReadRate, WriteRate: r.Cache.WriteRate, InputTokens: r.Cache.InputTokens},

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
