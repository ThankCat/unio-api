package dashboard

import (
	"context"
	"time"

	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
)

// 平台状态短窗口样本保护阈值：终态请求数低于此值不判异常，避免误报（§3.1.9）。
const minStatusSamples = 50

// 仅展示币种（§3.1 首版仅 USD）。
const displayCurrency = "USD"

// 平台状态等级。
const (
	PlatformHealthy      = "healthy"
	PlatformDegraded     = "degraded"
	PlatformDown         = "down"
	PlatformInsufficient = "insufficient_data"
)

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

// PlatformStatus 是近窗口平台健康判定。
type PlatformStatus struct {
	Level       string
	Reason      string
	WindowFrom  time.Time
	WindowTo    time.Time
	Terminal    int64
	Succeeded   int64
	SuccessRate float64
	NoChannel   int64
	Timeout     int64
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

	PlatformStatus PlatformStatus

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
	BreakdownRoute   = "route"
	BreakdownChannel = "channel"
	BreakdownModel   = "model"
)

// BreakdownRow 是分组表现 Top 精简行（§1.8）。
type BreakdownRow struct {
	Label       string
	RefID       *int64 // route_id / channel_id（model 维度为 nil）
	Status      string // 渠道维度可带状态；其它空
	Terminal    int64
	Succeeded   int64
	SuccessRate float64
}

// PerformancePoint 是性能时序桶点。
type PerformancePoint struct {
	Bucket     time.Time
	LatencyP95 float64
	TtftP95    float64
	TPS        float64
}

// Radar 聚合概览雷达：cards + 平台状态 + 行动项 + 异常渠道 Top。
// statusFrom/statusTo 是独立的短窗口（如近 15min），与页面 range [from,to) 解耦。
func (s *Service) Radar(ctx context.Context, from, to, statusFrom, statusTo time.Time) (RadarReport, error) {
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
	statusRow, err := s.store.DashboardRadarStatusWindow(ctx, sqlc.DashboardRadarStatusWindowParams{FromTime: tsNarg(statusFrom), ToTime: tsNarg(statusTo)})
	if err != nil {
		return RadarReport{}, storeFailed(err, "aggregate status window")
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

	report.PlatformStatus = evaluatePlatformStatus(statusRow, statusFrom, statusTo)
	report.BadChannels = badChannels(badRows)
	report.ActionItems = buildActionItems(report)

	return report, nil
}

// Breakdown 返回某维度（route|channel|model）的分组表现 Top 精简行（§3.1.8）。
func (s *Service) Breakdown(ctx context.Context, dimension string, from, to time.Time) ([]BreakdownRow, error) {
	fromTS, toTS := tsNarg(from), tsNarg(to)
	switch dimension {
	case BreakdownRoute:
		rows, err := s.store.DashboardBreakdownRoute(ctx, sqlc.DashboardBreakdownRouteParams{FromTime: fromTS, ToTime: toTS})
		if err != nil {
			return nil, storeFailed(err, "aggregate route breakdown")
		}
		out := make([]BreakdownRow, 0, len(rows))
		for _, r := range rows {
			br := BreakdownRow{Terminal: r.TerminalTotal, Succeeded: r.SucceededTotal}
			if r.RouteID.Valid {
				id := r.RouteID.Int64
				br.RefID = &id
			}
			if r.RouteName.Valid && r.RouteName.String != "" {
				br.Label = r.RouteName.String
			} else {
				br.Label = "内置 / 未指定线路"
			}
			br.SuccessRate = successRate(r.SucceededTotal, r.TerminalTotal)
			out = append(out, br)
		}
		return out, nil
	case BreakdownChannel:
		rows, err := s.store.DashboardBreakdownChannel(ctx, sqlc.DashboardBreakdownChannelParams{FromTime: fromTS, ToTime: toTS})
		if err != nil {
			return nil, storeFailed(err, "aggregate channel breakdown")
		}
		out := make([]BreakdownRow, 0, len(rows))
		for _, r := range rows {
			br := BreakdownRow{Terminal: r.TerminalTotal, Succeeded: r.SucceededTotal}
			if r.ChannelID.Valid {
				id := r.ChannelID.Int64
				br.RefID = &id
			}
			if r.ChannelName.Valid && r.ChannelName.String != "" {
				br.Label = r.ChannelName.String
			} else {
				br.Label = "未知渠道"
			}
			if r.ChannelStatus.Valid {
				br.Status = r.ChannelStatus.String
			}
			br.SuccessRate = successRate(r.SucceededTotal, r.TerminalTotal)
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
			out = append(out, BreakdownRow{
				Label:       r.ModelID,
				Terminal:    r.TerminalTotal,
				Succeeded:   r.SucceededTotal,
				SuccessRate: successRate(r.SucceededTotal, r.TerminalTotal),
			})
		}
		return out, nil
	default:
		return nil, invalidArgument("dimension", "dimension must be one of route|channel|model")
	}
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

func evaluatePlatformStatus(row sqlc.DashboardRadarStatusWindowRow, from, to time.Time) PlatformStatus {
	st := PlatformStatus{
		WindowFrom: from,
		WindowTo:   to,
		Terminal:   row.TerminalTotal,
		Succeeded:  row.SucceededTotal,
		NoChannel:  row.NoChannelTotal,
		Timeout:    row.TimeoutTotal,
	}
	if row.TerminalTotal > 0 {
		st.SuccessRate = float64(row.SucceededTotal) / float64(row.TerminalTotal)
	}

	if row.TerminalTotal < minStatusSamples {
		st.Level = PlatformInsufficient
		st.Reason = "近窗口样本不足，暂不判定平台异常"
		return st
	}

	switch {
	case row.NoChannelTotal > 0 || st.SuccessRate < degradedThreshold:
		st.Level = PlatformDown
		st.Reason = "近窗口成功率过低或出现无可用渠道"
	case st.SuccessRate < healthyThreshold:
		st.Level = PlatformDegraded
		st.Reason = "近窗口成功率低于健康阈值"
	default:
		st.Level = PlatformHealthy
		st.Reason = "近窗口运行正常"
	}
	return st
}

func buildActionItems(r RadarReport) []ActionItem {
	items := make([]ActionItem, 0, 4)

	switch r.PlatformStatus.Level {
	case PlatformDown:
		items = append(items, ActionItem{
			Kind: "platform", Severity: "danger",
			Title:    "平台状态异常",
			Detail:   r.PlatformStatus.Reason,
			Deeplink: "/requests?status=failed",
		})
	case PlatformDegraded:
		items = append(items, ActionItem{
			Kind: "platform", Severity: "warning",
			Title:    "平台状态降级",
			Detail:   r.PlatformStatus.Reason,
			Deeplink: "/requests?status=failed",
		})
	}

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
