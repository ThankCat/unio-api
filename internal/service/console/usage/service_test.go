package usage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	consoleservice "github.com/ThankCat/unio-gateway/internal/service/console"
)

type fakeStore struct {
	windowCalls  []sqlc.SummarizeConsoleUsageWindowParams
	windowRows   []sqlc.SummarizeConsoleUsageWindowRow
	seriesParams sqlc.ListConsoleUsageTimeseriesParams
	seriesRows   []sqlc.ListConsoleUsageTimeseriesRow
	seriesErr    error
	modelParams  sqlc.ListConsoleUsageByModelParams
	trendParams  sqlc.ListConsoleUsageTrendByGroupParams
	trendRows    []sqlc.ListConsoleUsageTrendByGroupRow
}

func (f *fakeStore) ListConsoleUsageTrendByGroup(_ context.Context, arg sqlc.ListConsoleUsageTrendByGroupParams) ([]sqlc.ListConsoleUsageTrendByGroupRow, error) {
	f.trendParams = arg
	return f.trendRows, nil
}

func (f *fakeStore) SummarizeConsoleUsageWindow(_ context.Context, arg sqlc.SummarizeConsoleUsageWindowParams) (sqlc.SummarizeConsoleUsageWindowRow, error) {
	f.windowCalls = append(f.windowCalls, arg)
	if len(f.windowRows) >= len(f.windowCalls) {
		return f.windowRows[len(f.windowCalls)-1], nil
	}
	return sqlc.SummarizeConsoleUsageWindowRow{}, nil
}

func (f *fakeStore) ListConsoleUsageTimeseries(_ context.Context, arg sqlc.ListConsoleUsageTimeseriesParams) ([]sqlc.ListConsoleUsageTimeseriesRow, error) {
	f.seriesParams = arg
	return f.seriesRows, f.seriesErr
}

func (f *fakeStore) ListConsoleUsageByModel(_ context.Context, arg sqlc.ListConsoleUsageByModelParams) ([]sqlc.ListConsoleUsageByModelRow, error) {
	f.modelParams = arg
	return []sqlc.ListConsoleUsageByModelRow{{
		GroupID:      "claude-opus-4-5",
		GroupName:    "Claude Opus 4.5",
		RequestCount: 3,
		TokenCount:   120,
	}}, nil
}

func (f *fakeStore) ListConsoleUsageByAPIKey(context.Context, sqlc.ListConsoleUsageByAPIKeyParams) ([]sqlc.ListConsoleUsageByAPIKeyRow, error) {
	return []sqlc.ListConsoleUsageByAPIKeyRow{{GroupID: 9, GroupName: "prod", RequestCount: 2}}, nil
}

func (f *fakeStore) ListConsoleUsageFilterModels(context.Context, int64) ([]sqlc.ListConsoleUsageFilterModelsRow, error) {
	return []sqlc.ListConsoleUsageFilterModelsRow{{ModelID: "gpt-5.6-terra", DisplayName: "GPT-5.6 Terra"}}, nil
}

func (f *fakeStore) ListConsoleFilterAPIKeys(context.Context, int64) ([]sqlc.ListConsoleFilterAPIKeysRow, error) {
	return []sqlc.ListConsoleFilterAPIKeysRow{{ID: 9, Name: "prod"}}, nil
}

func ts(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value, Valid: true}
}

func baseParams() OverviewParams {
	return OverviewParams{
		UserID: 2,
		From:   time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC),
		To:     time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC),
		Bucket: BucketDay,
		TZ:     "Asia/Shanghai",
	}
}

func TestOverviewSplitsSeriesAtCurrentWindowStart(t *testing.T) {
	params := baseParams()
	store := &fakeStore{seriesRows: []sqlc.ListConsoleUsageTimeseriesRow{
		{BucketStart: ts(params.From.AddDate(0, 0, -3)), RequestCount: 5},
		{BucketStart: ts(params.From.AddDate(0, 0, -1)), RequestCount: 7},
		{BucketStart: ts(params.From), RequestCount: 11},
		{BucketStart: ts(params.From.AddDate(0, 0, 2)), RequestCount: 13},
	}}

	overview, err := NewService(store).Overview(context.Background(), params)
	if err != nil {
		t.Fatalf("Overview: %v", err)
	}
	if len(overview.PreviousSeries) != 2 || len(overview.Series) != 2 {
		t.Fatalf("split = previous %d, current %d", len(overview.PreviousSeries), len(overview.Series))
	}
	if overview.Series[0].RequestCount != 11 || overview.PreviousSeries[0].RequestCount != 5 {
		t.Fatalf("unexpected split content: %+v / %+v", overview.PreviousSeries, overview.Series)
	}
}

func TestOverviewQueriesPreviousWindowOfEqualLength(t *testing.T) {
	params := baseParams()
	store := &fakeStore{}

	overview, err := NewService(store).Overview(context.Background(), params)
	if err != nil {
		t.Fatalf("Overview: %v", err)
	}
	if len(store.windowCalls) != 2 {
		t.Fatalf("expected current + previous window queries, got %d", len(store.windowCalls))
	}
	current, previous := store.windowCalls[0], store.windowCalls[1]
	if !current.FromTime.Time.Equal(params.From) || !current.ToTime.Time.Equal(params.To) {
		t.Fatalf("current window = %v..%v", current.FromTime.Time, current.ToTime.Time)
	}
	wantPreviousFrom := params.From.Add(-params.To.Sub(params.From))
	if !previous.FromTime.Time.Equal(wantPreviousFrom) || !previous.ToTime.Time.Equal(params.From) {
		t.Fatalf("previous window = %v..%v, want %v..%v",
			previous.FromTime.Time, previous.ToTime.Time, wantPreviousFrom, params.From)
	}
	if !overview.PreviousFrom.Equal(wantPreviousFrom) || !overview.PreviousTo.Equal(params.From) {
		t.Fatalf("reported previous window = %v..%v", overview.PreviousFrom, overview.PreviousTo)
	}
	// 序列一次查完两个周期，起点必须回到上一周期。
	if !store.seriesParams.FromTime.Time.Equal(wantPreviousFrom) || !store.seriesParams.ToTime.Time.Equal(params.To) {
		t.Fatalf("series window = %v..%v", store.seriesParams.FromTime.Time, store.seriesParams.ToTime.Time)
	}
}

func TestOverviewRejectsRangeTooLongForBucket(t *testing.T) {
	params := baseParams()
	params.Bucket = BucketMinute
	params.To = params.From.AddDate(0, 0, 30)

	_, err := NewService(&fakeStore{}).Overview(context.Background(), params)
	if err == nil || err.Code != consoleservice.CodeInvalidArgument || err.Param != "bucket" {
		t.Fatalf("expected invalid bucket argument, got %+v", err)
	}
}

func TestOverviewRejectsNonPositiveRange(t *testing.T) {
	params := baseParams()
	params.To = params.From

	_, err := NewService(&fakeStore{}).Overview(context.Background(), params)
	if err == nil || err.Param != "to" {
		t.Fatalf("expected invalid to argument, got %+v", err)
	}
}

func TestOverviewRejectsUnknownBucket(t *testing.T) {
	params := baseParams()
	params.Bucket = "week"

	_, err := NewService(&fakeStore{}).Overview(context.Background(), params)
	if err == nil || err.Param != "bucket" {
		t.Fatalf("expected invalid bucket argument, got %+v", err)
	}
}

func TestOverviewFallsBackToUTCForUnknownTimezone(t *testing.T) {
	params := baseParams()
	params.TZ = "Mars/Olympus"

	if _, err := NewService(&fakeStore{}).Overview(context.Background(), params); err != nil {
		t.Fatalf("Overview: %v", err)
	}
}

func TestOverviewSurfacesTimeseriesFailureAsUnavailable(t *testing.T) {
	store := &fakeStore{seriesErr: errors.New("connection refused")}

	_, err := NewService(store).Overview(context.Background(), baseParams())
	if err == nil || err.Code != consoleservice.CodeRequestUnavailable {
		t.Fatalf("expected request_unavailable, got %+v", err)
	}
}

func trendParams() TrendParams {
	return TrendParams{
		Bucket:    BucketDay,
		Dimension: GroupByModel,
		From:      time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC),
		TZ:        "Asia/Shanghai",
		To:        time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC),
		UserID:    2,
	}
}

func money(value string) pgtype.Numeric {
	var out pgtype.Numeric
	if err := out.Scan(value); err != nil {
		panic(err)
	}
	return out
}

// SQL 只回有数据的「桶 × 分组」，中间没请求的日子必须由 service 补回来，
// 否则前端会把 5 天的数据摊满整条轴，看不出中间断过。
func TestTrendFillsGapsAcrossTheWholeWindow(t *testing.T) {
	params := trendParams()
	shanghai, _ := time.LoadLocation("Asia/Shanghai")
	day := func(d int) pgtype.Timestamptz {
		return ts(time.Date(2026, 8, d, 0, 0, 0, 0, shanghai))
	}
	store := &fakeStore{trendRows: []sqlc.ListConsoleUsageTrendByGroupRow{
		{BucketStart: day(15), GroupID: "a", GroupName: "A", ChargeUsd: money("3"), RequestCount: 2},
		{BucketStart: day(18), GroupID: "a", GroupName: "A", ChargeUsd: money("5"), RequestCount: 4},
	}}

	trend, err := NewService(store).Trend(context.Background(), params)
	if err != nil {
		t.Fatalf("Trend: %v", err)
	}
	// 窗口在 +08 是 8/15 08:00 → 8/20 08:00，跨 6 个日历日；
	// 与 SQL 里 generate_series(date_trunc(from), date_trunc(to - 1µs)) 的算法一致。
	if len(trend.Series) != 6 {
		t.Fatalf("expected 6 daily buckets, got %d", len(trend.Series))
	}
	filled := 0
	for _, point := range trend.Series {
		if len(point.Slices) > 0 {
			filled++
		}
	}
	if filled != 2 {
		t.Fatalf("expected 2 buckets with data, got %d", filled)
	}
	if len(trend.Series[1].Slices) != 0 {
		t.Fatalf("8/16 should be an empty bucket, got %+v", trend.Series[1].Slices)
	}
}

// __other__ 是折叠出来的尾巴，不管它金额多大都排最后，
// 否则图例第一位会是「其他」，读者第一眼看到的是个没有含义的名字。
func TestTrendKeepsOtherLastInLegend(t *testing.T) {
	params := trendParams()
	shanghai, _ := time.LoadLocation("Asia/Shanghai")
	day := ts(time.Date(2026, 8, 15, 0, 0, 0, 0, shanghai))
	store := &fakeStore{trendRows: []sqlc.ListConsoleUsageTrendByGroupRow{
		{BucketStart: day, GroupID: OtherGroupID, GroupName: OtherGroupID, ChargeUsd: money("99")},
		{BucketStart: day, GroupID: "a", GroupName: "A", ChargeUsd: money("10")},
		{BucketStart: day, GroupID: "b", GroupName: "B", ChargeUsd: money("50")},
	}}

	trend, err := NewService(store).Trend(context.Background(), params)
	if err != nil {
		t.Fatalf("Trend: %v", err)
	}
	got := make([]string, 0, len(trend.Groups))
	for _, group := range trend.Groups {
		got = append(got, group.ID)
	}
	if len(got) != 3 || got[0] != "b" || got[1] != "a" || got[2] != OtherGroupID {
		t.Fatalf("legend order = %v, want [b a %s]", got, OtherGroupID)
	}
}

func TestTrendRejectsUnknownDimension(t *testing.T) {
	params := trendParams()
	params.Dimension = "endpoint"

	if _, err := NewService(&fakeStore{}).Trend(context.Background(), params); err == nil {
		t.Fatal("expected an invalid argument error")
	}
}

func TestGroupsRejectsUnknownDimension(t *testing.T) {
	_, err := NewService(&fakeStore{}).Groups(context.Background(), GroupParams{By: "endpoint"})
	if err == nil || err.Param != "by" {
		t.Fatalf("expected invalid by argument, got %+v", err)
	}
}

func TestGroupsByModelForwardsFilters(t *testing.T) {
	store := &fakeStore{}
	params := GroupParams{
		UserID: 2,
		By:     GroupByModel,
		From:   time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC),
		To:     time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC),
		Filters: Filters{
			ModelIDs: []string{"claude-opus-4-5"},
			Q:        "  opus  ",
		},
	}

	items, err := NewService(store).Groups(context.Background(), params)
	if err != nil {
		t.Fatalf("Groups: %v", err)
	}
	if len(items) != 1 || items[0].ID != "claude-opus-4-5" {
		t.Fatalf("unexpected items: %+v", items)
	}
	if len(store.modelParams.ModelIds) != 1 || store.modelParams.ModelIds[0] != "claude-opus-4-5" {
		t.Fatalf("model filter not forwarded: %+v", store.modelParams.ModelIds)
	}
	if !store.modelParams.Q.Valid || store.modelParams.Q.String != "  opus  " {
		t.Fatalf("q not forwarded verbatim: %+v", store.modelParams.Q)
	}
}

func TestFiltersReturnsKeysAndModels(t *testing.T) {
	filters, err := NewService(&fakeStore{}).Filters(context.Background(), 2)
	if err != nil {
		t.Fatalf("Filters: %v", err)
	}
	if len(filters.APIKeys) != 1 || len(filters.Models) != 1 {
		t.Fatalf("unexpected filters: %+v", filters)
	}
	if filters.Models[0].ID != "gpt-5.6-terra" {
		t.Fatalf("unexpected model option: %+v", filters.Models[0])
	}
}
