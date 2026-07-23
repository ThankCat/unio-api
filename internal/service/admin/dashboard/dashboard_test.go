package dashboard

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

func mustNumeric(t *testing.T, s string) pgtype.Numeric {
	t.Helper()
	var n pgtype.Numeric
	if err := n.Scan(s); err != nil {
		t.Fatalf("scan numeric %q: %v", s, err)
	}
	return n
}

// fakeStore 用预置数据满足 Store 接口；timeseries 调用记录最近一次 Unit 以断言透传。
type fakeStore struct {
	revenueRows   []sqlc.DashboardRevenueByCurrencyRow
	costRows      []sqlc.DashboardCostByCurrencyRow
	exceptionRows []sqlc.DashboardBillingExceptionSummaryRow

	requestsTS []sqlc.DashboardRequestsTimeseriesRow
	tokensTS   []sqlc.DashboardTokensTimeseriesRow
	spendTS    []sqlc.DashboardSpendTimeseriesRow
	costTS     []sqlc.DashboardCostTimeseriesRow

	// §3.1 雷达重构。
	perfRow        sqlc.DashboardRadarRequestPerfRow
	throughput     sqlc.DashboardRadarThroughputRow
	radarTokens    sqlc.DashboardRadarTokensRow
	backlog        sqlc.DashboardRadarSettlementBacklogRow
	badChannels    []sqlc.DashboardRadarBadChannelsRow
	providerBD     []sqlc.DashboardBreakdownProviderRow
	routeBD        []sqlc.DashboardBreakdownRouteRow
	channelBD      []sqlc.DashboardBreakdownChannelRow
	channelBuckets []sqlc.DashboardChannelSuccessBucketsRow
	modelBD        []sqlc.DashboardBreakdownModelRow
	perfTS         []sqlc.DashboardPerformanceTimeseriesRow
	topErrors      []sqlc.DashboardTopErrorsRow

	gotUnit string
}

func (s *fakeStore) DashboardRevenueByCurrency(context.Context, sqlc.DashboardRevenueByCurrencyParams) ([]sqlc.DashboardRevenueByCurrencyRow, error) {
	return s.revenueRows, nil
}
func (s *fakeStore) DashboardCostByCurrency(context.Context, sqlc.DashboardCostByCurrencyParams) ([]sqlc.DashboardCostByCurrencyRow, error) {
	return s.costRows, nil
}
func (s *fakeStore) DashboardBillingExceptionSummary(context.Context, sqlc.DashboardBillingExceptionSummaryParams) ([]sqlc.DashboardBillingExceptionSummaryRow, error) {
	return s.exceptionRows, nil
}
func (s *fakeStore) DashboardRequestsTimeseries(_ context.Context, arg sqlc.DashboardRequestsTimeseriesParams) ([]sqlc.DashboardRequestsTimeseriesRow, error) {
	s.gotUnit = arg.Unit
	return s.requestsTS, nil
}
func (s *fakeStore) DashboardTokensTimeseries(_ context.Context, arg sqlc.DashboardTokensTimeseriesParams) ([]sqlc.DashboardTokensTimeseriesRow, error) {
	s.gotUnit = arg.Unit
	return s.tokensTS, nil
}
func (s *fakeStore) DashboardSpendTimeseries(_ context.Context, arg sqlc.DashboardSpendTimeseriesParams) ([]sqlc.DashboardSpendTimeseriesRow, error) {
	s.gotUnit = arg.Unit
	return s.spendTS, nil
}
func (s *fakeStore) DashboardCostTimeseries(_ context.Context, arg sqlc.DashboardCostTimeseriesParams) ([]sqlc.DashboardCostTimeseriesRow, error) {
	s.gotUnit = arg.Unit
	return s.costTS, nil
}
func (s *fakeStore) DashboardRadarRequestPerf(context.Context, sqlc.DashboardRadarRequestPerfParams) (sqlc.DashboardRadarRequestPerfRow, error) {
	return s.perfRow, nil
}
func (s *fakeStore) DashboardRadarThroughput(context.Context, sqlc.DashboardRadarThroughputParams) (sqlc.DashboardRadarThroughputRow, error) {
	return s.throughput, nil
}
func (s *fakeStore) DashboardRadarTokens(context.Context, sqlc.DashboardRadarTokensParams) (sqlc.DashboardRadarTokensRow, error) {
	return s.radarTokens, nil
}
func (s *fakeStore) DashboardRadarSettlementBacklog(context.Context) (sqlc.DashboardRadarSettlementBacklogRow, error) {
	return s.backlog, nil
}
func (s *fakeStore) DashboardRadarBadChannels(context.Context, sqlc.DashboardRadarBadChannelsParams) ([]sqlc.DashboardRadarBadChannelsRow, error) {
	return s.badChannels, nil
}
func (s *fakeStore) DashboardBreakdownProvider(context.Context, sqlc.DashboardBreakdownProviderParams) ([]sqlc.DashboardBreakdownProviderRow, error) {
	return s.providerBD, nil
}
func (s *fakeStore) DashboardBreakdownRoute(context.Context, sqlc.DashboardBreakdownRouteParams) ([]sqlc.DashboardBreakdownRouteRow, error) {
	return s.routeBD, nil
}
func (s *fakeStore) DashboardBreakdownChannel(context.Context, sqlc.DashboardBreakdownChannelParams) ([]sqlc.DashboardBreakdownChannelRow, error) {
	return s.channelBD, nil
}
func (s *fakeStore) DashboardChannelSuccessBuckets(context.Context, sqlc.DashboardChannelSuccessBucketsParams) ([]sqlc.DashboardChannelSuccessBucketsRow, error) {
	return s.channelBuckets, nil
}
func (s *fakeStore) DashboardBreakdownModel(context.Context, sqlc.DashboardBreakdownModelParams) ([]sqlc.DashboardBreakdownModelRow, error) {
	return s.modelBD, nil
}
func (s *fakeStore) DashboardPerformanceTimeseries(_ context.Context, arg sqlc.DashboardPerformanceTimeseriesParams) ([]sqlc.DashboardPerformanceTimeseriesRow, error) {
	s.gotUnit = arg.Unit
	return s.perfTS, nil
}
func (s *fakeStore) DashboardTopErrors(context.Context, sqlc.DashboardTopErrorsParams) ([]sqlc.DashboardTopErrorsRow, error) {
	return s.topErrors, nil
}

func TestRadarAggregates(t *testing.T) {
	store := &fakeStore{
		perfRow: sqlc.DashboardRadarRequestPerfRow{
			// terminal_total 口径为 succeeded+failed（不含 canceled）；total = terminal + canceled + pending。
			TerminalTotal: 100, SucceededTotal: 96, FailedTotal: 4, CanceledTotal: 1, PendingTotal: 4,
			TimeoutTotal: 2, LatencyAvg: 800, LatencyP50: 700, LatencyP90: 1500, LatencyP95: 1800, LatencyP99: 2500,
			LatencySample: 96,
			TtftAvg:       1200, TtftP50: 900, TtftP90: 1500, TtftP95: 1600, TtftP99: 2200, TtftSample: 84,
		},
		throughput:  sqlc.DashboardRadarThroughputRow{OutputTokens: 5000, GenerationSeconds: 100},
		radarTokens: sqlc.DashboardRadarTokensRow{UncachedInput: 600, CacheReadInput: 300, CacheWriteInput: 100, OutputTokens: 5000},
		backlog:     sqlc.DashboardRadarSettlementBacklogRow{ActiveTotal: 2, DeadTotal: 1},
		revenueRows: []sqlc.DashboardRevenueByCurrencyRow{{Currency: "USD", Total: mustNumeric(t, "20.00")}},
		costRows:    []sqlc.DashboardCostByCurrencyRow{{Currency: "USD", Total: mustNumeric(t, "8.00")}},
		exceptionRows: []sqlc.DashboardBillingExceptionSummaryRow{
			{EventType: "write_off", Total: 3, PlatformAmount: mustNumeric(t, "1.25")},
		},
		badChannels: []sqlc.DashboardRadarBadChannelsRow{
			{ChannelID: 9, Name: "ch-bad", Status: "enabled", AttemptTotal: 100, AttemptSucceeded: 50, AttemptFailed: 45},
		},
	}

	now := time.Now()
	out, err := NewService(store).Radar(context.Background(), now.Add(-24*time.Hour), now)
	if err != nil {
		t.Fatalf("radar: %v", err)
	}
	if out.Requests.Total != 105 { // terminal(succ+fail) + canceled + pending
		t.Fatalf("requests total = %d, want 105", out.Requests.Total)
	}
	if out.Requests.SuccessRate < 0.95 || out.Requests.SuccessRate > 0.961 {
		t.Fatalf("success rate = %v, want ~0.96", out.Requests.SuccessRate)
	}
	if out.TPS != 50 { // 5000 / 100
		t.Fatalf("tps = %v, want 50", out.TPS)
	}
	if out.Cache.ReadRate < 0.39 || out.Cache.ReadRate > 0.41 { // (300+100)/1000
		t.Fatalf("cache hit rate = %v, want ~0.4", out.Cache.ReadRate)
	}
	if out.Latency.Sample != 96 {
		t.Fatalf("latency sample = %d, want 96", out.Latency.Sample)
	}
	if out.Latency.Coverage != 1 { // 96/96 succeeded
		t.Fatalf("latency coverage = %v, want 1", out.Latency.Coverage)
	}
	if !out.Ttft.HasData || out.Ttft.Sample != 84 {
		t.Fatalf("ttft sample = %d hasData=%v, want 84/true", out.Ttft.Sample, out.Ttft.HasData)
	}
	if out.Ttft.P99 != 2200 {
		t.Fatalf("ttft p99 = %v, want 2200", out.Ttft.P99)
	}
	if out.Ttft.Coverage < 0.79 || out.Ttft.Coverage > 0.81 { // 84/105
		t.Fatalf("ttft coverage = %v, want ~0.8", out.Ttft.Coverage)
	}
	if out.MarginUSD != "12" { // 20 - 8
		t.Fatalf("margin = %q, want 12", out.MarginUSD)
	}
	if out.Settlement.Dead != 1 {
		t.Fatalf("dead backlog = %d, want 1", out.Settlement.Dead)
	}
	// 主观渠道健康行动项已删除；这里只保留结算与计费客观异常。
	if len(out.ActionItems) < 2 {
		t.Fatalf("action items = %d, want >= 2", len(out.ActionItems))
	}
	if len(out.BadChannels) != 1 || out.BadChannels[0].AttemptFailed != 45 {
		t.Fatalf("bad channels = %+v", out.BadChannels)
	}
}

func TestBreakdownInvalidDimension(t *testing.T) {
	_, err := NewService(&fakeStore{}).Breakdown(context.Background(), "bogus", time.Time{}, time.Now())
	if err == nil {
		t.Fatal("expected error for invalid dimension")
	}
}

func TestTimeseriesDispatch(t *testing.T) {
	store := &fakeStore{
		requestsTS: []sqlc.DashboardRequestsTimeseriesRow{
			{Bucket: pgtype.Timestamptz{Time: time.Now(), Valid: true}, Total: 10, Succeeded: 9},
		},
		tokensTS: []sqlc.DashboardTokensTimeseriesRow{
			{Bucket: pgtype.Timestamptz{Time: time.Now(), Valid: true}, InputTokens: 100, OutputTokens: 20},
		},
		spendTS: []sqlc.DashboardSpendTimeseriesRow{
			{Bucket: pgtype.Timestamptz{Time: time.Now(), Valid: true}, Currency: "USD", Total: mustNumeric(t, "1.50")},
		},
		costTS: []sqlc.DashboardCostTimeseriesRow{
			{Bucket: pgtype.Timestamptz{Time: time.Now(), Valid: true}, Currency: "USD", Total: mustNumeric(t, "0.75")},
		},
	}
	svc := NewService(store)

	reqSeries, err := svc.Timeseries(context.Background(), MetricRequests, IntervalMinute, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("requests timeseries: %v", err)
	}
	if len(reqSeries.RequestPoints) != 1 || reqSeries.TokenPoints != nil || reqSeries.SpendPoints != nil {
		t.Fatalf("requests metric should fill only RequestPoints: %+v", reqSeries)
	}
	if store.gotUnit != IntervalMinute {
		t.Fatalf("expected unit minute passed through, got %q", store.gotUnit)
	}

	spendSeries, err := svc.Timeseries(context.Background(), MetricSpend, IntervalDay, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("spend timeseries: %v", err)
	}
	if len(spendSeries.SpendPoints) != 1 || spendSeries.SpendPoints[0].Amount != "1.5" {
		t.Fatalf("spend metric should fill SpendPoints with trimmed amount: %+v", spendSeries.SpendPoints)
	}

	costSeries, err := svc.Timeseries(context.Background(), MetricCost, IntervalDay, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("cost timeseries: %v", err)
	}
	if len(costSeries.CostPoints) != 1 || costSeries.CostPoints[0].Amount != "0.75" {
		t.Fatalf("cost metric should fill CostPoints with trimmed amount: %+v", costSeries.CostPoints)
	}
	if costSeries.SpendPoints != nil || costSeries.RequestPoints != nil {
		t.Fatalf("cost metric should fill only CostPoints: %+v", costSeries)
	}
}

func TestTimeseriesRejectsBadArgs(t *testing.T) {
	svc := NewService(&fakeStore{})

	if _, err := svc.Timeseries(context.Background(), "bogus", IntervalHour, time.Time{}, time.Time{}); failure.CodeOf(err) != failure.CodeAdminInvalidArgument {
		t.Fatalf("expected invalid argument for bad metric, got %v", err)
	}
	if _, err := svc.Timeseries(context.Background(), MetricRequests, "week", time.Time{}, time.Time{}); failure.CodeOf(err) != failure.CodeAdminInvalidArgument {
		t.Fatalf("expected invalid argument for bad interval, got %v", err)
	}
}
