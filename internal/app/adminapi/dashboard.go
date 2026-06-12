package adminapi

import (
	"context"
	"net/http"
	"time"

	"github.com/ThankCat/unio-api/internal/service/admin/dashboard"
)

// defaultDashboardWindow 是 from/to 均缺省时的默认回看窗口（近 7 天）。
const defaultDashboardWindow = 7 * 24 * time.Hour

// DashboardService 定义 adminapi 工作台看板（M9 只读聚合）所需的最小能力。
type DashboardService interface {
	Overview(ctx context.Context, from, to time.Time) (dashboard.Overview, error)
	Timeseries(ctx context.Context, metric, interval string, from, to time.Time) (dashboard.Series, error)
}

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
	}
	return dto
}
