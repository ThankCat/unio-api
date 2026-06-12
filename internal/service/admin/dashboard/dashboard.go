// Package dashboard 编排 admin 工作台看板（M9 运营首页）的只读聚合。
//
// 全部只读、不引入新业务事实，复用 M6 的事实表（request_records / usage_records /
// ledger_entries / cost_snapshots / user_balances / ledger_billing_exceptions /
// request_attempts）。安全/口径约定：
//   - 金额一律十进制字符串承载，绝不经 float；毛利用 big.Rat 精确相减（见 helpers.go）。
//   - 收入/成本/毛利/余额一律按币种分组，绝不跨币种相加。
//   - 时间区间 [from, to)（左闭右开），与 M6 列表过滤一致。
//   - channel 无 health 列，健康从区间内 request_attempts 成功率推导并分桶。
package dashboard

import (
	"context"
	"time"

	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
)

// 健康分桶阈值（按区间内 attempt 成功率）；后续可改为可配置。
const (
	healthyThreshold  = 0.95
	degradedThreshold = 0.80
)

// 时间序列指标与时间桶单位的合法取值。
const (
	MetricRequests = "requests"
	MetricTokens   = "tokens"
	MetricSpend    = "spend"

	IntervalHour = "hour"
	IntervalDay  = "day"
)

// Store 定义工作台看板所需的只读聚合存储能力（由 sqlc.Queries 满足）。
type Store interface {
	DashboardRequestStatusCounts(ctx context.Context, arg sqlc.DashboardRequestStatusCountsParams) ([]sqlc.DashboardRequestStatusCountsRow, error)
	DashboardTokenTotals(ctx context.Context, arg sqlc.DashboardTokenTotalsParams) (sqlc.DashboardTokenTotalsRow, error)
	DashboardRevenueByCurrency(ctx context.Context, arg sqlc.DashboardRevenueByCurrencyParams) ([]sqlc.DashboardRevenueByCurrencyRow, error)
	DashboardCostByCurrency(ctx context.Context, arg sqlc.DashboardCostByCurrencyParams) ([]sqlc.DashboardCostByCurrencyRow, error)
	DashboardBalanceByCurrency(ctx context.Context) ([]sqlc.DashboardBalanceByCurrencyRow, error)
	DashboardBillingExceptionSummary(ctx context.Context, arg sqlc.DashboardBillingExceptionSummaryParams) ([]sqlc.DashboardBillingExceptionSummaryRow, error)
	DashboardEnabledChannelCount(ctx context.Context) (int64, error)
	DashboardChannelHealth(ctx context.Context, arg sqlc.DashboardChannelHealthParams) ([]sqlc.DashboardChannelHealthRow, error)
	DashboardRequestsTimeseries(ctx context.Context, arg sqlc.DashboardRequestsTimeseriesParams) ([]sqlc.DashboardRequestsTimeseriesRow, error)
	DashboardTokensTimeseries(ctx context.Context, arg sqlc.DashboardTokensTimeseriesParams) ([]sqlc.DashboardTokensTimeseriesRow, error)
	DashboardSpendTimeseries(ctx context.Context, arg sqlc.DashboardSpendTimeseriesParams) ([]sqlc.DashboardSpendTimeseriesRow, error)
}

// MoneyByCurrency 是某币种的单一金额（十进制字符串）。
type MoneyByCurrency struct {
	Currency string
	Amount   string
}

// BalanceByCurrency 是某币种的余额总额、冻结额与可用额（十进制字符串）。
type BalanceByCurrency struct {
	Currency  string
	Balance   string
	Reserved  string
	Available string
}

// RequestStats 是区间内请求计数与成功/错误率（率以 [0,1] 比例返回，前端格式化为百分比）。
// 成功率/错误率分母为终态请求数（succeeded+failed+canceled），不含进行中（pending+running）。
type RequestStats struct {
	Total       int64
	Succeeded   int64
	Failed      int64
	Canceled    int64
	Pending     int64
	Running     int64
	SuccessRate float64
	ErrorRate   float64
}

// TokenStats 是区间内 token 用量汇总。
type TokenStats struct {
	Input  int64
	Output int64
	Total  int64
}

// ExceptionGroup 是某 event_type 的计费异常计数与平台承担金额。
type ExceptionGroup struct {
	EventType      string
	Total          int64
	PlatformAmount string
}

// ChannelHealth 是单个 channel 的健康画像（按区间内 attempt 成功率推导）。
type ChannelHealth struct {
	ChannelID        int64
	Name             string
	Status           string
	AttemptTotal     int64
	AttemptSucceeded int64
	SuccessRate      float64
	Bucket           string // healthy / degraded / unhealthy / no_data
}

// ChannelStats 是 channel 概览：启用数 + 健康分桶计数 + 明细。
type ChannelStats struct {
	EnabledCount int64
	Healthy      int
	Degraded     int
	Unhealthy    int
	NoData       int
	Channels     []ChannelHealth
}

// Overview 是工作台看板首屏的全部 KPI 聚合。
type Overview struct {
	From              time.Time
	To                time.Time
	Requests          RequestStats
	Tokens            TokenStats
	Revenue           []MoneyByCurrency
	Cost              []MoneyByCurrency
	Margin            []MoneyByCurrency
	Balance           []BalanceByCurrency
	BillingExceptions []ExceptionGroup
	Channels          ChannelStats
}

// RequestPoint / TokenPoint / SpendPoint 是时间序列的单个桶点。
type RequestPoint struct {
	Bucket    time.Time
	Total     int64
	Succeeded int64
}

type TokenPoint struct {
	Bucket time.Time
	Input  int64
	Output int64
}

type SpendPoint struct {
	Bucket   time.Time
	Currency string
	Amount   string
}

// Series 是某指标的时间序列结果；按 Metric 只填对应一组点。
type Series struct {
	Metric        string
	Interval      string
	From          time.Time
	To            time.Time
	RequestPoints []RequestPoint
	TokenPoints   []TokenPoint
	SpendPoints   []SpendPoint
}

// Service 提供工作台看板只读聚合。
type Service struct {
	store Store
}

// NewService 创建工作台看板只读聚合服务。
func NewService(store Store) *Service {
	return &Service{store: store}
}

// Overview 聚合 [from, to) 区间内的全部首屏 KPI。
func (s *Service) Overview(ctx context.Context, from, to time.Time) (Overview, error) {
	fromTS, toTS := tsNarg(from), tsNarg(to)

	statusRows, err := s.store.DashboardRequestStatusCounts(ctx, sqlc.DashboardRequestStatusCountsParams{FromTime: fromTS, ToTime: toTS})
	if err != nil {
		return Overview{}, storeFailed(err, "aggregate request status counts")
	}

	tokenRow, err := s.store.DashboardTokenTotals(ctx, sqlc.DashboardTokenTotalsParams{FromTime: fromTS, ToTime: toTS})
	if err != nil {
		return Overview{}, storeFailed(err, "aggregate token totals")
	}

	revenueRows, err := s.store.DashboardRevenueByCurrency(ctx, sqlc.DashboardRevenueByCurrencyParams{FromTime: fromTS, ToTime: toTS})
	if err != nil {
		return Overview{}, storeFailed(err, "aggregate revenue by currency")
	}

	costRows, err := s.store.DashboardCostByCurrency(ctx, sqlc.DashboardCostByCurrencyParams{FromTime: fromTS, ToTime: toTS})
	if err != nil {
		return Overview{}, storeFailed(err, "aggregate cost by currency")
	}

	balanceRows, err := s.store.DashboardBalanceByCurrency(ctx)
	if err != nil {
		return Overview{}, storeFailed(err, "aggregate balance by currency")
	}

	exceptionRows, err := s.store.DashboardBillingExceptionSummary(ctx, sqlc.DashboardBillingExceptionSummaryParams{FromTime: fromTS, ToTime: toTS})
	if err != nil {
		return Overview{}, storeFailed(err, "aggregate billing exceptions")
	}

	enabledChannels, err := s.store.DashboardEnabledChannelCount(ctx)
	if err != nil {
		return Overview{}, storeFailed(err, "count enabled channels")
	}

	healthRows, err := s.store.DashboardChannelHealth(ctx, sqlc.DashboardChannelHealthParams{FromTime: fromTS, ToTime: toTS})
	if err != nil {
		return Overview{}, storeFailed(err, "aggregate channel health")
	}

	revenue := moneyByCurrency(revenueRows)
	cost := moneyByCurrency2(costRows)

	return Overview{
		From:              from,
		To:                to,
		Requests:          requestStats(statusRows),
		Tokens:            tokenStats(tokenRow),
		Revenue:           revenue,
		Cost:              cost,
		Margin:            marginByCurrency(revenue, cost),
		Balance:           balanceByCurrency(balanceRows),
		BillingExceptions: exceptionGroups(exceptionRows),
		Channels:          channelStats(enabledChannels, healthRows),
	}, nil
}

// Timeseries 按 metric 分派返回 [from, to) 区间内按时间桶聚合的序列。
// metric 须为 requests|tokens|spend，interval 须为 hour|day（否则 admin_invalid_argument）。
func (s *Service) Timeseries(ctx context.Context, metric, interval string, from, to time.Time) (Series, error) {
	if interval != IntervalHour && interval != IntervalDay {
		return Series{}, invalidArgument("interval", "interval must be one of hour|day")
	}

	out := Series{Metric: metric, Interval: interval, From: from, To: to}
	fromTS, toTS := tsNarg(from), tsNarg(to)

	switch metric {
	case MetricRequests:
		rows, err := s.store.DashboardRequestsTimeseries(ctx, sqlc.DashboardRequestsTimeseriesParams{Unit: interval, FromTime: fromTS, ToTime: toTS})
		if err != nil {
			return Series{}, storeFailed(err, "aggregate requests timeseries")
		}
		out.RequestPoints = make([]RequestPoint, 0, len(rows))
		for _, row := range rows {
			out.RequestPoints = append(out.RequestPoints, RequestPoint{Bucket: row.Bucket.Time, Total: row.Total, Succeeded: row.Succeeded})
		}
	case MetricTokens:
		rows, err := s.store.DashboardTokensTimeseries(ctx, sqlc.DashboardTokensTimeseriesParams{Unit: interval, FromTime: fromTS, ToTime: toTS})
		if err != nil {
			return Series{}, storeFailed(err, "aggregate tokens timeseries")
		}
		out.TokenPoints = make([]TokenPoint, 0, len(rows))
		for _, row := range rows {
			out.TokenPoints = append(out.TokenPoints, TokenPoint{Bucket: row.Bucket.Time, Input: row.InputTokens, Output: row.OutputTokens})
		}
	case MetricSpend:
		rows, err := s.store.DashboardSpendTimeseries(ctx, sqlc.DashboardSpendTimeseriesParams{Unit: interval, FromTime: fromTS, ToTime: toTS})
		if err != nil {
			return Series{}, storeFailed(err, "aggregate spend timeseries")
		}
		out.SpendPoints = make([]SpendPoint, 0, len(rows))
		for _, row := range rows {
			out.SpendPoints = append(out.SpendPoints, SpendPoint{Bucket: row.Bucket.Time, Currency: row.Currency, Amount: numericString(row.Total)})
		}
	default:
		return Series{}, invalidArgument("metric", "metric must be one of requests|tokens|spend")
	}

	return out, nil
}

func requestStats(rows []sqlc.DashboardRequestStatusCountsRow) RequestStats {
	var st RequestStats
	for _, row := range rows {
		switch row.Status {
		case "succeeded":
			st.Succeeded = row.Total
		case "failed":
			st.Failed = row.Total
		case "canceled":
			st.Canceled = row.Total
		case "pending":
			st.Pending = row.Total
		case "running":
			st.Running = row.Total
		}
		st.Total += row.Total
	}

	terminal := st.Succeeded + st.Failed + st.Canceled
	if terminal > 0 {
		st.SuccessRate = float64(st.Succeeded) / float64(terminal)
		st.ErrorRate = float64(st.Failed+st.Canceled) / float64(terminal)
	}
	return st
}

func tokenStats(row sqlc.DashboardTokenTotalsRow) TokenStats {
	return TokenStats{
		Input:  row.InputTokens,
		Output: row.OutputTokens,
		Total:  row.InputTokens + row.OutputTokens,
	}
}

func moneyByCurrency(rows []sqlc.DashboardRevenueByCurrencyRow) []MoneyByCurrency {
	out := make([]MoneyByCurrency, 0, len(rows))
	for _, row := range rows {
		out = append(out, MoneyByCurrency{Currency: row.Currency, Amount: numericString(row.Total)})
	}
	return out
}

func moneyByCurrency2(rows []sqlc.DashboardCostByCurrencyRow) []MoneyByCurrency {
	out := make([]MoneyByCurrency, 0, len(rows))
	for _, row := range rows {
		out = append(out, MoneyByCurrency{Currency: row.Currency, Amount: numericString(row.Total)})
	}
	return out
}

// marginByCurrency 按币种求毛利 = 收入 − 成本，覆盖两侧出现过的全部币种（缺失侧按 0）。
func marginByCurrency(revenue, cost []MoneyByCurrency) []MoneyByCurrency {
	revMap := make(map[string]string, len(revenue))
	for _, m := range revenue {
		revMap[m.Currency] = m.Amount
	}
	costMap := make(map[string]string, len(cost))
	for _, m := range cost {
		costMap[m.Currency] = m.Amount
	}

	// 以收入顺序为主，再补只在成本侧出现的币种，保持稳定输出顺序。
	seen := make(map[string]bool, len(revenue)+len(cost))
	out := make([]MoneyByCurrency, 0, len(revenue)+len(cost))
	appendCur := func(cur string) {
		if seen[cur] {
			return
		}
		seen[cur] = true
		rev := revMap[cur]
		if rev == "" {
			rev = "0"
		}
		c := costMap[cur]
		if c == "" {
			c = "0"
		}
		out = append(out, MoneyByCurrency{Currency: cur, Amount: subtractDecimal(rev, c)})
	}
	for _, m := range revenue {
		appendCur(m.Currency)
	}
	for _, m := range cost {
		appendCur(m.Currency)
	}
	return out
}

func balanceByCurrency(rows []sqlc.DashboardBalanceByCurrencyRow) []BalanceByCurrency {
	out := make([]BalanceByCurrency, 0, len(rows))
	for _, row := range rows {
		balance := numericString(row.TotalBalance)
		reserved := numericString(row.TotalReserved)
		out = append(out, BalanceByCurrency{
			Currency:  row.Currency,
			Balance:   balance,
			Reserved:  reserved,
			Available: subtractDecimal(balance, reserved),
		})
	}
	return out
}

func exceptionGroups(rows []sqlc.DashboardBillingExceptionSummaryRow) []ExceptionGroup {
	out := make([]ExceptionGroup, 0, len(rows))
	for _, row := range rows {
		out = append(out, ExceptionGroup{
			EventType:      row.EventType,
			Total:          row.Total,
			PlatformAmount: numericString(row.PlatformAmount),
		})
	}
	return out
}

func channelStats(enabled int64, rows []sqlc.DashboardChannelHealthRow) ChannelStats {
	st := ChannelStats{EnabledCount: enabled, Channels: make([]ChannelHealth, 0, len(rows))}
	for _, row := range rows {
		ch := ChannelHealth{
			ChannelID:        row.ChannelID,
			Name:             row.Name,
			Status:           row.Status,
			AttemptTotal:     row.AttemptTotal,
			AttemptSucceeded: row.AttemptSucceeded,
		}
		switch {
		case row.AttemptTotal == 0:
			ch.Bucket = "no_data"
			st.NoData++
		default:
			ch.SuccessRate = float64(row.AttemptSucceeded) / float64(row.AttemptTotal)
			switch {
			case ch.SuccessRate >= healthyThreshold:
				ch.Bucket = "healthy"
				st.Healthy++
			case ch.SuccessRate >= degradedThreshold:
				ch.Bucket = "degraded"
				st.Degraded++
			default:
				ch.Bucket = "unhealthy"
				st.Unhealthy++
			}
		}
		st.Channels = append(st.Channels, ch)
	}
	return st
}
