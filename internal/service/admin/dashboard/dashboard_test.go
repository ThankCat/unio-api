package dashboard

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
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
	statusRows    []sqlc.DashboardRequestStatusCountsRow
	tokenRow      sqlc.DashboardTokenTotalsRow
	revenueRows   []sqlc.DashboardRevenueByCurrencyRow
	costRows      []sqlc.DashboardCostByCurrencyRow
	balanceRows   []sqlc.DashboardBalanceByCurrencyRow
	exceptionRows []sqlc.DashboardBillingExceptionSummaryRow
	enabled       int64
	healthRows    []sqlc.DashboardChannelHealthRow

	requestsTS []sqlc.DashboardRequestsTimeseriesRow
	tokensTS   []sqlc.DashboardTokensTimeseriesRow
	spendTS    []sqlc.DashboardSpendTimeseriesRow

	gotUnit string
}

func (s *fakeStore) DashboardRequestStatusCounts(context.Context, sqlc.DashboardRequestStatusCountsParams) ([]sqlc.DashboardRequestStatusCountsRow, error) {
	return s.statusRows, nil
}
func (s *fakeStore) DashboardTokenTotals(context.Context, sqlc.DashboardTokenTotalsParams) (sqlc.DashboardTokenTotalsRow, error) {
	return s.tokenRow, nil
}
func (s *fakeStore) DashboardRevenueByCurrency(context.Context, sqlc.DashboardRevenueByCurrencyParams) ([]sqlc.DashboardRevenueByCurrencyRow, error) {
	return s.revenueRows, nil
}
func (s *fakeStore) DashboardCostByCurrency(context.Context, sqlc.DashboardCostByCurrencyParams) ([]sqlc.DashboardCostByCurrencyRow, error) {
	return s.costRows, nil
}
func (s *fakeStore) DashboardBalanceByCurrency(context.Context) ([]sqlc.DashboardBalanceByCurrencyRow, error) {
	return s.balanceRows, nil
}
func (s *fakeStore) DashboardBillingExceptionSummary(context.Context, sqlc.DashboardBillingExceptionSummaryParams) ([]sqlc.DashboardBillingExceptionSummaryRow, error) {
	return s.exceptionRows, nil
}
func (s *fakeStore) DashboardEnabledChannelCount(context.Context) (int64, error) {
	return s.enabled, nil
}
func (s *fakeStore) DashboardChannelHealth(context.Context, sqlc.DashboardChannelHealthParams) ([]sqlc.DashboardChannelHealthRow, error) {
	return s.healthRows, nil
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

func TestOverviewAggregates(t *testing.T) {
	store := &fakeStore{
		statusRows: []sqlc.DashboardRequestStatusCountsRow{
			{Status: "succeeded", Total: 90},
			{Status: "failed", Total: 8},
			{Status: "canceled", Total: 2},
			{Status: "running", Total: 5},
		},
		tokenRow:    sqlc.DashboardTokenTotalsRow{InputTokens: 1000, OutputTokens: 250},
		revenueRows: []sqlc.DashboardRevenueByCurrencyRow{{Currency: "USD", Total: mustNumeric(t, "12.50")}},
		costRows:    []sqlc.DashboardCostByCurrencyRow{{Currency: "USD", Total: mustNumeric(t, "4.30")}},
		balanceRows: []sqlc.DashboardBalanceByCurrencyRow{{Currency: "USD", TotalBalance: mustNumeric(t, "100.00"), TotalReserved: mustNumeric(t, "10.00")}},
		exceptionRows: []sqlc.DashboardBillingExceptionSummaryRow{
			{EventType: "write_off", Total: 3, PlatformAmount: mustNumeric(t, "1.25")},
		},
		enabled: 4,
		healthRows: []sqlc.DashboardChannelHealthRow{
			{ChannelID: 1, Name: "ch-healthy", Status: "enabled", AttemptTotal: 100, AttemptSucceeded: 99},
			{ChannelID: 2, Name: "ch-degraded", Status: "enabled", AttemptTotal: 100, AttemptSucceeded: 85},
			{ChannelID: 3, Name: "ch-unhealthy", Status: "enabled", AttemptTotal: 100, AttemptSucceeded: 50},
			{ChannelID: 4, Name: "ch-nodata", Status: "disabled", AttemptTotal: 0, AttemptSucceeded: 0},
		},
	}

	out, err := NewService(store).Overview(context.Background(), time.Now().Add(-time.Hour), time.Now())
	if err != nil {
		t.Fatalf("overview: %v", err)
	}

	if out.Requests.Total != 105 {
		t.Fatalf("expected total 105, got %d", out.Requests.Total)
	}
	// 成功率分母为终态请求数 90+8+2=100，不含进行中 running=5。
	if out.Requests.SuccessRate != 0.9 {
		t.Fatalf("expected success rate 0.9, got %v", out.Requests.SuccessRate)
	}
	if out.Requests.ErrorRate != 0.1 {
		t.Fatalf("expected error rate 0.1, got %v", out.Requests.ErrorRate)
	}
	if out.Tokens.Total != 1250 {
		t.Fatalf("expected token total 1250, got %d", out.Tokens.Total)
	}

	if len(out.Margin) != 1 || out.Margin[0].Currency != "USD" || out.Margin[0].Amount != "8.2" {
		t.Fatalf("expected USD margin 8.2, got %+v", out.Margin)
	}
	if len(out.Balance) != 1 || out.Balance[0].Available != "90" {
		t.Fatalf("expected USD available 90, got %+v", out.Balance)
	}

	if out.Channels.EnabledCount != 4 {
		t.Fatalf("expected enabled 4, got %d", out.Channels.EnabledCount)
	}
	if out.Channels.Healthy != 1 || out.Channels.Degraded != 1 || out.Channels.Unhealthy != 1 || out.Channels.NoData != 1 {
		t.Fatalf("unexpected health distribution: %+v", out.Channels)
	}
}

func TestMarginCoversCostOnlyCurrency(t *testing.T) {
	store := &fakeStore{
		revenueRows: []sqlc.DashboardRevenueByCurrencyRow{{Currency: "USD", Total: mustNumeric(t, "5.00")}},
		// CNY 只在成本侧出现：毛利应为 0 - 2 = -2。
		costRows: []sqlc.DashboardCostByCurrencyRow{
			{Currency: "USD", Total: mustNumeric(t, "2.00")},
			{Currency: "CNY", Total: mustNumeric(t, "2.00")},
		},
	}

	out, err := NewService(store).Overview(context.Background(), time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("overview: %v", err)
	}

	got := map[string]string{}
	for _, m := range out.Margin {
		got[m.Currency] = m.Amount
	}
	if got["USD"] != "3" {
		t.Fatalf("expected USD margin 3, got %q", got["USD"])
	}
	if got["CNY"] != "-2" {
		t.Fatalf("expected CNY margin -2, got %q", got["CNY"])
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
	}
	svc := NewService(store)

	reqSeries, err := svc.Timeseries(context.Background(), MetricRequests, IntervalHour, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("requests timeseries: %v", err)
	}
	if len(reqSeries.RequestPoints) != 1 || reqSeries.TokenPoints != nil || reqSeries.SpendPoints != nil {
		t.Fatalf("requests metric should fill only RequestPoints: %+v", reqSeries)
	}
	if store.gotUnit != IntervalHour {
		t.Fatalf("expected unit hour passed through, got %q", store.gotUnit)
	}

	spendSeries, err := svc.Timeseries(context.Background(), MetricSpend, IntervalDay, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("spend timeseries: %v", err)
	}
	if len(spendSeries.SpendPoints) != 1 || spendSeries.SpendPoints[0].Amount != "1.5" {
		t.Fatalf("spend metric should fill SpendPoints with trimmed amount: %+v", spendSeries.SpendPoints)
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
