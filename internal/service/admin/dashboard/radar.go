package dashboard

import (
	"context"
	"time"

	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
	"github.com/jackc/pgx/v5/pgtype"
)

// 仅展示币种（§3.1 首版仅 USD）。
const displayCurrency = "USD"

// LatencyStats 是延迟分位画像（毫秒）。
// Sample = 区间内测到延迟（成功且 completed_at 非空）的请求数；
// Coverage = Sample / 成功请求，反映平均/分位的代表性。
type LatencyStats struct {
	Avg      float64
	P50      float64
	P90      float64
	P95      float64
	P99      float64
	Sample   int64
	Coverage float64
}

// TtftStats 是首 token 时间画像（毫秒）。
// Sample = 区间内测到首 token（response_started_at 非空）的请求数；
// Coverage = Sample / 区间总请求，反映平均/分位的代表性。
type TtftStats struct {
	Avg      float64
	P50      float64
	P90      float64
	P95      float64
	P99      float64
	Sample   int64
	Coverage float64
	HasData  bool
}

// CacheStats 是缓存命中画像。ReadRate = 缓存命中率 = 缓存重量 / 输入 token。
type CacheStats struct {
	ReadRate           float64
	WriteRate          float64
	InputTokens        int64
	UncachedTokens     int64
	CacheReadTokens    int64
	CacheWrite5mTokens int64
	CacheWrite1hTokens int64
}

// SettlementBacklog 是结算补偿积压（时点值）。
type SettlementBacklog struct {
	Active int64
	Dead   int64
}

// ActionItem 是「需要处理」列表项，带深链参数。
type ActionItem struct {
	Kind     string
	Severity string // warning / danger
	Title    string
	Detail   string
	Deeplink string
}

// BadChannel 是异常渠道 Top 精简行（§1.8：渠道·健康·成功率·最近错误）。
type BadChannel struct {
	ChannelID        int64
	Name             string
	Status           string
	AttemptTotal     int64
	AttemptSucceeded int64
	SuccessRate      float64
	Bucket           string
	RecentErrorCode  string
}

// RadarReport 是概览雷达一次性聚合结果（§3.1）。
type RadarReport struct {
	From time.Time
	To   time.Time

	// 计数与率
	Requests RequestStats
	Timeout  int64

	// 性能
	Latency LatencyStats
	Ttft    TtftStats
	TPS     float64

	// token / 缓存
	Tokens TokenStats
	Cache  CacheStats

	// 金额（仅 USD）
	RevenueUSD string
	CostUSD    string
	MarginUSD  string

	// 计费异常 / 结算积压
	BillingExceptionTotal  int64
	BillingExceptionAmount string
	Settlement             SettlementBacklog

	ActionItems []ActionItem
	BadChannels []BadChannel
}

// BreakdownDimension 合法取值。
const (
	BreakdownProvider = "provider"
	BreakdownChannel  = "channel"
	BreakdownModel    = "model"
	BreakdownRoute    = "route"
)

// BreakdownRow 是各维度表现 Top 精简行。
type BreakdownRow struct {
	Label          string
	RefID          *int64 // route_id / channel_id / provider_id（model 维度为 nil）
	Status         string // enabled/disabled（provider/channel/route）
	Terminal       int64
	Succeeded      int64
	Failed         int64
	SuccessRate    float64
	Tokens         int64  // 区间内该分组 token 合计（输入 + 输出）
	RevenueUSD     string // 区间内平台收入合计（USD，ledger debit）
	CostUSD        string // 区间内该分组上游成本合计（USD，十进制字符串）
	MarginUSD      string // 贡献利润 = 收入 − 成本（USD）
	Latency        LatencyStats
	LatencyP95     float64 // route/model 维度仍用 P95 单值
	AvgTPS         float64 // provider/channel 维度：成功 attempt 的加权平均输出速度
	HealthBucket   string  // healthy/degraded/unhealthy/no_data（按请求成功率分桶）
	RecentError    string
	ChannelCount   int64           // provider 维度：命中渠道数
	SuccessBuckets []SuccessBucket // channel 维度：最近 10 分钟 attempt 成功率桶
}

// SuccessBucket 是渠道表现中按小时聚合的 attempt 成功率。
type SuccessBucket struct {
	Bucket      time.Time
	Terminal    int64
	Succeeded   int64
	SuccessRate float64
}

// ErrorGroup 是「失败原因」面板的单条错误码聚合（§错误可见性）。
// Share = Total / 区间内失败总数（[0,1] 比例），由 service 计算。
type ErrorGroup struct {
	Code  string
	Total int64
	Share float64
}

// PerformancePoint 是性能时序桶点。
type PerformancePoint struct {
	Bucket     time.Time
	LatencyP95 float64
	TtftP95    float64
	TPS        float64
}

// Radar 聚合概览雷达：cards + 行动项 + 异常渠道 Top。
func (s *Service) Radar(ctx context.Context, from, to time.Time) (RadarReport, error) {
	fromTS, toTS := tsNarg(from), tsNarg(to)

	perf, err := s.store.DashboardRadarRequestPerf(ctx, sqlc.DashboardRadarRequestPerfParams{FromTime: fromTS, ToTime: toTS})
	if err != nil {
		return RadarReport{}, storeFailed(err, "aggregate radar request perf")
	}
	tp, err := s.store.DashboardRadarThroughput(ctx, sqlc.DashboardRadarThroughputParams{FromTime: fromTS, ToTime: toTS})
	if err != nil {
		return RadarReport{}, storeFailed(err, "aggregate radar throughput")
	}
	tok, err := s.store.DashboardRadarTokens(ctx, sqlc.DashboardRadarTokensParams{FromTime: fromTS, ToTime: toTS})
	if err != nil {
		return RadarReport{}, storeFailed(err, "aggregate radar tokens")
	}
	backlog, err := s.store.DashboardRadarSettlementBacklog(ctx)
	if err != nil {
		return RadarReport{}, storeFailed(err, "aggregate settlement backlog")
	}
	badRows, err := s.store.DashboardRadarBadChannels(ctx, sqlc.DashboardRadarBadChannelsParams{FromTime: fromTS, ToTime: toTS})
	if err != nil {
		return RadarReport{}, storeFailed(err, "aggregate bad channels")
	}
	revenueRows, err := s.store.DashboardRevenueByCurrency(ctx, sqlc.DashboardRevenueByCurrencyParams{FromTime: fromTS, ToTime: toTS})
	if err != nil {
		return RadarReport{}, storeFailed(err, "aggregate revenue by currency")
	}
	costRows, err := s.store.DashboardCostByCurrency(ctx, sqlc.DashboardCostByCurrencyParams{FromTime: fromTS, ToTime: toTS})
	if err != nil {
		return RadarReport{}, storeFailed(err, "aggregate cost by currency")
	}
	exceptionRows, err := s.store.DashboardBillingExceptionSummary(ctx, sqlc.DashboardBillingExceptionSummaryParams{FromTime: fromTS, ToTime: toTS})
	if err != nil {
		return RadarReport{}, storeFailed(err, "aggregate billing exceptions")
	}

	report := RadarReport{From: from, To: to}

	// 请求计数与率（区间内 request 粒度）。
	report.Requests = RequestStats{
		Total:     perf.TerminalTotal + perf.PendingTotal,
		Succeeded: perf.SucceededTotal,
		Failed:    perf.FailedTotal,
		Canceled:  perf.CanceledTotal,
	}
	if perf.TerminalTotal > 0 {
		report.Requests.SuccessRate = float64(perf.SucceededTotal) / float64(perf.TerminalTotal)
		report.Requests.ErrorRate = float64(perf.FailedTotal+perf.CanceledTotal) / float64(perf.TerminalTotal)
	}
	report.Timeout = perf.TimeoutTotal

	report.Latency = LatencyStats{
		Avg:    perf.LatencyAvg,
		P50:    perf.LatencyP50,
		P90:    perf.LatencyP90,
		P95:    perf.LatencyP95,
		P99:    perf.LatencyP99,
		Sample: perf.LatencySample,
	}
	if perf.SucceededTotal > 0 {
		report.Latency.Coverage = float64(perf.LatencySample) / float64(perf.SucceededTotal)
	}
	report.Ttft = TtftStats{
		Avg:     perf.TtftAvg,
		P50:     perf.TtftP50,
		P90:     perf.TtftP90,
		P95:     perf.TtftP95,
		P99:     perf.TtftP99,
		Sample:  perf.TtftSample,
		HasData: perf.TtftSample > 0,
	}
	if report.Requests.Total > 0 {
		report.Ttft.Coverage = float64(perf.TtftSample) / float64(report.Requests.Total)
	}

	if tp.GenerationSeconds > 0 {
		report.TPS = float64(tp.OutputTokens) / tp.GenerationSeconds
	}

	report.Tokens = TokenStats{
		Input:  tok.UncachedInput + tok.CacheReadInput + tok.CacheWriteInput,
		Output: tok.OutputTokens,
	}
	report.Tokens.Total = report.Tokens.Input + report.Tokens.Output
	report.Cache = CacheStats{
		InputTokens:        report.Tokens.Input,
		UncachedTokens:     tok.UncachedInput,
		CacheReadTokens:    tok.CacheReadInput,
		CacheWrite5mTokens: tok.CacheWrite5mInput,
		CacheWrite1hTokens: tok.CacheWrite1hInput,
	}
	if report.Tokens.Input > 0 {
		cacheWeight := tok.CacheReadInput + tok.CacheWriteInput
		report.Cache.ReadRate = float64(cacheWeight) / float64(report.Tokens.Input)
		report.Cache.WriteRate = float64(tok.CacheWriteInput) / float64(report.Tokens.Input)
	}

	// 金额仅 USD。
	report.RevenueUSD = pickCurrency(moneyByCurrency(revenueRows), displayCurrency)
	report.CostUSD = pickCurrency(moneyByCurrency2(costRows), displayCurrency)
	report.MarginUSD = subtractDecimal(report.RevenueUSD, report.CostUSD)

	for _, row := range exceptionRows {
		report.BillingExceptionTotal += row.Total
	}
	report.BillingExceptionAmount = pickCurrency(exceptionAmounts(exceptionRows), displayCurrency)
	report.Settlement = SettlementBacklog{Active: backlog.ActiveTotal, Dead: backlog.DeadTotal}

	report.BadChannels = badChannels(badRows)
	report.ActionItems = buildActionItems(report)

	return report, nil
}

// Breakdown 返回某维度（provider|channel|model|route）的表现 Top 精简行。
func (s *Service) Breakdown(ctx context.Context, dimension string, from, to time.Time) ([]BreakdownRow, error) {
	fromTS, toTS := tsNarg(from), tsNarg(to)
	switch dimension {
	case BreakdownProvider:
		rows, err := s.store.DashboardBreakdownProvider(ctx, sqlc.DashboardBreakdownProviderParams{FromTime: fromTS, ToTime: toTS})
		if err != nil {
			return nil, storeFailed(err, "aggregate provider breakdown")
		}
		out := make([]BreakdownRow, 0, len(rows))
		for _, r := range rows {
			br := BreakdownRow{
				Tokens: r.TokensTotal,
				Latency: requestLatencyStats(
					r.LatencyAvg, r.LatencyP50, r.LatencyP90, r.LatencyP95, r.LatencyP99,
					r.LatencySample, r.SucceededTotal,
				),
				ChannelCount: r.ChannelCount,
				AvgTPS:       r.AvgTps,
			}
			applyBreakdownMoney(&br, r.RevenueUsd, r.CostUsd)
			fillBreakdownCounts(&br, r.SucceededTotal, r.TerminalTotal, r.FailedTotal)
			id := r.ProviderID
			br.RefID = &id
			br.Label = r.ProviderName
			if br.Label == "" {
				br.Label = "未知服务商"
			}
			br.Status = r.ProviderStatus
			out = append(out, br)
		}
		return out, nil
	case BreakdownRoute:
		rows, err := s.store.DashboardBreakdownRoute(ctx, sqlc.DashboardBreakdownRouteParams{FromTime: fromTS, ToTime: toTS})
		if err != nil {
			return nil, storeFailed(err, "aggregate route breakdown")
		}
		out := make([]BreakdownRow, 0, len(rows))
		for _, r := range rows {
			br := BreakdownRow{
				Tokens:     r.TokensTotal,
				LatencyP95: r.LatencyP95,
			}
			applyBreakdownMoney(&br, r.RevenueUsd, r.CostUsd)
			fillBreakdownCounts(&br, r.SucceededTotal, r.TerminalTotal, r.FailedTotal)
			if r.RouteID.Valid {
				id := r.RouteID.Int64
				br.RefID = &id
			}
			if r.RouteName.Valid && r.RouteName.String != "" {
				br.Label = r.RouteName.String
			} else {
				br.Label = "内置 / 未指定线路"
			}
			if r.RouteStatus.Valid {
				br.Status = r.RouteStatus.String
			}
			if r.RecentErrorCode.Valid {
				br.RecentError = r.RecentErrorCode.String
			}
			out = append(out, br)
		}
		return out, nil
	case BreakdownChannel:
		rows, err := s.store.DashboardBreakdownChannel(ctx, sqlc.DashboardBreakdownChannelParams{FromTime: fromTS, ToTime: toTS})
		if err != nil {
			return nil, storeFailed(err, "aggregate channel breakdown")
		}
		bucketRows, err := s.store.DashboardChannelSuccessBuckets(ctx, sqlc.DashboardChannelSuccessBucketsParams{FromTime: fromTS, ToTime: toTS})
		if err != nil {
			return nil, storeFailed(err, "aggregate channel success buckets")
		}
		bucketsByChannel := make(map[int64][]SuccessBucket)
		for _, r := range bucketRows {
			bucketsByChannel[r.ChannelID] = append(bucketsByChannel[r.ChannelID], SuccessBucket{
				Bucket:      r.Bucket.Time,
				Terminal:    r.TerminalTotal,
				Succeeded:   r.SucceededTotal,
				SuccessRate: r.SuccessRate,
			})
		}
		out := make([]BreakdownRow, 0, len(rows))
		for _, r := range rows {
			br := BreakdownRow{
				Tokens: r.TokensTotal,
				Latency: requestLatencyStats(
					r.LatencyAvg, r.LatencyP50, r.LatencyP90, r.LatencyP95, r.LatencyP99,
					r.LatencySample, r.SucceededTotal,
				),
				AvgTPS:         r.AvgTps,
				SuccessBuckets: bucketsByChannel[r.ChannelID],
			}
			applyBreakdownMoney(&br, r.RevenueUsd, r.CostUsd)
			fillBreakdownCounts(&br, r.SucceededTotal, r.TerminalTotal, r.FailedTotal)
			id := r.ChannelID
			br.RefID = &id
			br.Label = r.ChannelName
			if br.Label == "" {
				br.Label = "未知渠道"
			}
			br.Status = r.ChannelStatus
			if r.RecentErrorCode.Valid {
				br.RecentError = r.RecentErrorCode.String
			}
			out = append(out, br)
		}
		return out, nil
	case BreakdownModel:
		rows, err := s.store.DashboardBreakdownModel(ctx, sqlc.DashboardBreakdownModelParams{FromTime: fromTS, ToTime: toTS})
		if err != nil {
			return nil, storeFailed(err, "aggregate model breakdown")
		}
		out := make([]BreakdownRow, 0, len(rows))
		for _, r := range rows {
			br := BreakdownRow{
				Label:      r.ModelID,
				Tokens:     r.TokensTotal,
				LatencyP95: r.LatencyP95,
			}
			applyBreakdownMoney(&br, r.RevenueUsd, r.CostUsd)
			fillBreakdownCounts(&br, r.SucceededTotal, r.TerminalTotal, r.FailedTotal)
			out = append(out, br)
		}
		return out, nil
	default:
		return nil, invalidArgument("dimension", "dimension must be one of provider|channel|model|route")
	}
}

// TopErrors 返回区间内失败请求的错误码分布（Top 10），并计算每类占全部失败的比例。
func (s *Service) TopErrors(ctx context.Context, from, to time.Time) ([]ErrorGroup, error) {
	rows, err := s.store.DashboardTopErrors(ctx, sqlc.DashboardTopErrorsParams{
		FromTime: tsNarg(from),
		ToTime:   tsNarg(to),
	})
	if err != nil {
		return nil, storeFailed(err, "aggregate top errors")
	}
	out := make([]ErrorGroup, 0, len(rows))
	for _, r := range rows {
		g := ErrorGroup{Code: r.ErrorCode, Total: r.Total}
		if r.FailedTotal > 0 {
			g.Share = float64(r.Total) / float64(r.FailedTotal)
		}
		out = append(out, g)
	}
	return out, nil
}

// PerformanceTimeseries 返回性能趋势（P95 延迟 / P95 TTFT / TPS）。
func (s *Service) PerformanceTimeseries(ctx context.Context, interval string, from, to time.Time) ([]PerformancePoint, error) {
	if !validInterval(interval) {
		return nil, invalidArgument("interval", "interval must be one of minute|hour|day")
	}
	rows, err := s.store.DashboardPerformanceTimeseries(ctx, sqlc.DashboardPerformanceTimeseriesParams{Unit: interval, FromTime: tsNarg(from), ToTime: tsNarg(to)})
	if err != nil {
		return nil, storeFailed(err, "aggregate performance timeseries")
	}
	out := make([]PerformancePoint, 0, len(rows))
	for _, r := range rows {
		p := PerformancePoint{Bucket: r.Bucket.Time, LatencyP95: r.LatencyP95, TtftP95: r.TtftP95}
		if r.GenerationSeconds > 0 {
			p.TPS = float64(r.OutputTokens) / r.GenerationSeconds
		}
		out = append(out, p)
	}
	return out, nil
}

func successRate(succeeded, terminal int64) float64 {
	if terminal <= 0 {
		return 0
	}
	return float64(succeeded) / float64(terminal)
}

func requestLatencyStats(avg, p50, p90, p95, p99 float64, sample, succeeded int64) LatencyStats {
	s := LatencyStats{Avg: avg, P50: p50, P90: p90, P95: p95, P99: p99, Sample: sample}
	if succeeded > 0 {
		s.Coverage = float64(sample) / float64(succeeded)
	}
	return s
}

func fillBreakdownCounts(br *BreakdownRow, succeeded, terminal, failed int64) {
	br.Terminal = terminal
	br.Succeeded = succeeded
	br.Failed = failed
	br.SuccessRate = successRate(succeeded, terminal)
	br.HealthBucket = healthBucket(succeeded, terminal)
}

func applyBreakdownMoney(br *BreakdownRow, revenue, cost pgtype.Numeric) {
	br.RevenueUSD = numericString(revenue)
	br.CostUSD = numericString(cost)
	br.MarginUSD = subtractDecimal(br.RevenueUSD, br.CostUSD)
}

// healthBucket 按成功率分桶（与 channelStats 同阈值）。
func healthBucket(succeeded, total int64) string {
	if total == 0 {
		return "no_data"
	}
	rate := float64(succeeded) / float64(total)
	switch {
	case rate >= healthyThreshold:
		return "healthy"
	case rate >= degradedThreshold:
		return "degraded"
	default:
		return "unhealthy"
	}
}

func badChannels(rows []sqlc.DashboardRadarBadChannelsRow) []BadChannel {
	out := make([]BadChannel, 0, len(rows))
	for _, r := range rows {
		bc := BadChannel{
			ChannelID:        r.ChannelID,
			Name:             r.Name,
			Status:           r.Status,
			AttemptTotal:     r.AttemptTotal,
			AttemptSucceeded: r.AttemptSucceeded,
			SuccessRate:      successRate(r.AttemptSucceeded, r.AttemptTotal),
			Bucket:           healthBucket(r.AttemptSucceeded, r.AttemptTotal),
		}
		if r.RecentErrorCode.Valid {
			bc.RecentErrorCode = r.RecentErrorCode.String
		}
		out = append(out, bc)
	}
	return out
}

func buildActionItems(r RadarReport) []ActionItem {
	items := make([]ActionItem, 0, 4)

	if r.Settlement.Dead > 0 {
		items = append(items, ActionItem{
			Kind: "settlement_dead", Severity: "danger",
			Title:    "结算补偿失败需人工处理",
			Detail:   "存在已耗尽自动重试的结算补偿任务",
			Deeplink: "/system?tab=jobs",
		})
	}
	if r.BillingExceptionTotal > 0 {
		items = append(items, ActionItem{
			Kind: "billing_exception", Severity: "warning",
			Title:    "存在计费异常",
			Detail:   "区间内新增计费异常事件",
			Deeplink: "/ledger?tab=exceptions",
		})
	}
	for _, bc := range r.BadChannels {
		if bc.Bucket == "unhealthy" {
			items = append(items, ActionItem{
				Kind: "channel", Severity: "warning",
				Title:    "渠道不健康：" + bc.Name,
				Detail:   "近区间成功率偏低，建议检查上游与凭据",
				Deeplink: "/channels",
			})
		}
	}
	return items
}

// pickCurrency 从按币种分组的金额里取指定币种，缺失则 "0"。
func pickCurrency(rows []MoneyByCurrency, currency string) string {
	for _, m := range rows {
		if m.Currency == currency {
			return m.Amount
		}
	}
	return "0"
}

func exceptionAmounts(rows []sqlc.DashboardBillingExceptionSummaryRow) []MoneyByCurrency {
	// ledger_billing_exceptions 无 currency 列（platform_amount 已是平台币种），统一并入 USD 展示桶。
	total := "0"
	for _, r := range rows {
		total = addDecimal(total, numericString(r.PlatformAmount))
	}
	return []MoneyByCurrency{{Currency: displayCurrency, Amount: total}}
}
