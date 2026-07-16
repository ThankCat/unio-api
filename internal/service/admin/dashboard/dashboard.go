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

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/appsettings"
)

// 时间序列指标与时间桶单位的合法取值。
const (
	MetricRequests = "requests"
	MetricTokens   = "tokens"
	MetricSpend    = "spend"
	MetricCost     = "cost"

	IntervalMinute = "minute"
	IntervalHour   = "hour"
	IntervalDay    = "day"
)

// Store 定义工作台看板所需的只读聚合存储能力（由 sqlc.Queries 满足）。
type Store interface {
	DashboardRevenueByCurrency(ctx context.Context, arg sqlc.DashboardRevenueByCurrencyParams) ([]sqlc.DashboardRevenueByCurrencyRow, error)
	DashboardCostByCurrency(ctx context.Context, arg sqlc.DashboardCostByCurrencyParams) ([]sqlc.DashboardCostByCurrencyRow, error)
	DashboardBillingExceptionSummary(ctx context.Context, arg sqlc.DashboardBillingExceptionSummaryParams) ([]sqlc.DashboardBillingExceptionSummaryRow, error)
	DashboardRequestsTimeseries(ctx context.Context, arg sqlc.DashboardRequestsTimeseriesParams) ([]sqlc.DashboardRequestsTimeseriesRow, error)
	DashboardTokensTimeseries(ctx context.Context, arg sqlc.DashboardTokensTimeseriesParams) ([]sqlc.DashboardTokensTimeseriesRow, error)
	DashboardSpendTimeseries(ctx context.Context, arg sqlc.DashboardSpendTimeseriesParams) ([]sqlc.DashboardSpendTimeseriesRow, error)
	DashboardCostTimeseries(ctx context.Context, arg sqlc.DashboardCostTimeseriesParams) ([]sqlc.DashboardCostTimeseriesRow, error)

	// §3.1 概览雷达重构（radar / breakdown / performance 时序）。
	DashboardRadarRequestPerf(ctx context.Context, arg sqlc.DashboardRadarRequestPerfParams) (sqlc.DashboardRadarRequestPerfRow, error)
	DashboardRadarThroughput(ctx context.Context, arg sqlc.DashboardRadarThroughputParams) (sqlc.DashboardRadarThroughputRow, error)
	DashboardRadarTokens(ctx context.Context, arg sqlc.DashboardRadarTokensParams) (sqlc.DashboardRadarTokensRow, error)
	DashboardRadarSettlementBacklog(ctx context.Context) (sqlc.DashboardRadarSettlementBacklogRow, error)
	DashboardRadarBadChannels(ctx context.Context, arg sqlc.DashboardRadarBadChannelsParams) ([]sqlc.DashboardRadarBadChannelsRow, error)
	DashboardBreakdownProvider(ctx context.Context, arg sqlc.DashboardBreakdownProviderParams) ([]sqlc.DashboardBreakdownProviderRow, error)
	DashboardBreakdownRoute(ctx context.Context, arg sqlc.DashboardBreakdownRouteParams) ([]sqlc.DashboardBreakdownRouteRow, error)
	DashboardBreakdownChannel(ctx context.Context, arg sqlc.DashboardBreakdownChannelParams) ([]sqlc.DashboardBreakdownChannelRow, error)
	DashboardChannelSuccessBuckets(ctx context.Context, arg sqlc.DashboardChannelSuccessBucketsParams) ([]sqlc.DashboardChannelSuccessBucketsRow, error)
	DashboardBreakdownModel(ctx context.Context, arg sqlc.DashboardBreakdownModelParams) ([]sqlc.DashboardBreakdownModelRow, error)
	DashboardPerformanceTimeseries(ctx context.Context, arg sqlc.DashboardPerformanceTimeseriesParams) ([]sqlc.DashboardPerformanceTimeseriesRow, error)
	DashboardTopErrors(ctx context.Context, arg sqlc.DashboardTopErrorsParams) ([]sqlc.DashboardTopErrorsRow, error)
}

// MoneyByCurrency 是某币种的单一金额（十进制字符串）。
type MoneyByCurrency struct {
	Currency string
	Amount   string
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
// cost 与 spend 同形（bucket+currency+amount），复用 SpendPoint 承载，仅字段名区分。
type Series struct {
	Metric        string
	Interval      string
	From          time.Time
	To            time.Time
	RequestPoints []RequestPoint
	TokenPoints   []TokenPoint
	SpendPoints   []SpendPoint
	CostPoints    []SpendPoint
}

// Service 提供工作台看板只读聚合。
type Service struct {
	store Store
	// settings 供每请求现读健康分桶阈值(admin_backend.channel_health_thresholds);
	// nil(单测)回代码默认。
	settings *appsettings.SettingsStore
}

// NewService 创建工作台看板只读聚合服务。
func NewService(store Store, settings *appsettings.SettingsStore) *Service {
	return &Service{store: store, settings: settings}
}

// healthThresholds 读取当前生效的分桶阈值。
func (s *Service) healthThresholds(ctx context.Context) appsettings.ChannelHealthThresholds {
	return appsettings.AdminBackendChannelHealthThresholds(ctx, s.settings)
}

// Timeseries 按 metric 分派返回 [from, to) 区间内按时间桶聚合的序列。
// metric 须为 requests|tokens|spend|cost，interval 须为 minute|hour|day（否则 admin_invalid_argument）。
func (s *Service) Timeseries(ctx context.Context, metric, interval string, from, to time.Time) (Series, error) {
	if !validInterval(interval) {
		return Series{}, invalidArgument("interval", "interval must be one of minute|hour|day")
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
	case MetricCost:
		rows, err := s.store.DashboardCostTimeseries(ctx, sqlc.DashboardCostTimeseriesParams{Unit: interval, FromTime: fromTS, ToTime: toTS})
		if err != nil {
			return Series{}, storeFailed(err, "aggregate cost timeseries")
		}
		out.CostPoints = make([]SpendPoint, 0, len(rows))
		for _, row := range rows {
			out.CostPoints = append(out.CostPoints, SpendPoint{Bucket: row.Bucket.Time, Currency: row.Currency, Amount: numericString(row.Total)})
		}
	default:
		return Series{}, invalidArgument("metric", "metric must be one of requests|tokens|spend|cost")
	}

	return out, nil
}

func validInterval(interval string) bool {
	return interval == IntervalMinute || interval == IntervalHour || interval == IntervalDay
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

