// Package usage 提供 Console 用量统计只读查询：时间窗汇总（含上一周期）、分桶趋势与维度排行。
// 口径与 console/requests 完全一致：只统计当前用户、账本 USD 净扣费大于 0 的请求。
package usage

import (
	"context"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/admin/opsutil"
	consoleservice "github.com/ThankCat/unio-gateway/internal/service/console"
	consolerequests "github.com/ThankCat/unio-gateway/internal/service/console/requests"
)

// 分桶粒度。与前端 lib/range.ts 的 RangeBucket 对齐。
const (
	BucketMinute = "minute"
	BucketHour   = "hour"
	BucketDay    = "day"
)

// maxBuckets 限制单次返回的分桶总数（当前周期 + 上一周期）。
// 前端会按时间窗自动选粒度，这里只防手工构造 URL 打爆内存。
const maxBuckets = 1500

// groupRowLimit 是维度排行返回的最大行数。
const groupRowLimit = 20

// 维度排行支持的分组维度。
const (
	GroupByModel  = "model"
	GroupByAPIKey = "api_key"
)

// Store 是用量统计所需的存储能力。
type Store interface {
	SummarizeConsoleUsageWindow(context.Context, sqlc.SummarizeConsoleUsageWindowParams) (sqlc.SummarizeConsoleUsageWindowRow, error)
	ListConsoleUsageTimeseries(context.Context, sqlc.ListConsoleUsageTimeseriesParams) ([]sqlc.ListConsoleUsageTimeseriesRow, error)
	ListConsoleUsageTrendByGroup(context.Context, sqlc.ListConsoleUsageTrendByGroupParams) ([]sqlc.ListConsoleUsageTrendByGroupRow, error)
	ListConsoleUsageByModel(context.Context, sqlc.ListConsoleUsageByModelParams) ([]sqlc.ListConsoleUsageByModelRow, error)
	ListConsoleUsageByAPIKey(context.Context, sqlc.ListConsoleUsageByAPIKeyParams) ([]sqlc.ListConsoleUsageByAPIKeyRow, error)
	ListConsoleUsageFilterModels(context.Context, int64) ([]sqlc.ListConsoleUsageFilterModelsRow, error)
	ListConsoleFilterAPIKeys(context.Context, int64) ([]sqlc.ListConsoleFilterAPIKeysRow, error)
}

var _ Store = (*sqlc.Queries)(nil)

// Filters 是用量统计的筛选条件，口径与请求中心列表一致。
type Filters struct {
	APIKeyIDs   []int64
	ModelIDs    []string
	Endpoints   []string
	StreamTypes []string
	Q           string
}

// OverviewParams 是概览查询条件。From/To 必填，用于确定上一周期与分桶边界。
type OverviewParams struct {
	UserID int64
	From   time.Time
	To     time.Time
	Bucket string
	// TZ 是 IANA 时区名，决定按天/按小时分桶的边界。空值按 UTC。
	TZ string
	Filters
}

// GroupParams 是维度排行查询条件。
type GroupParams struct {
	UserID int64
	By     string
	From   time.Time
	To     time.Time
	Filters
}

// Window 是单个时间窗的用量与费用汇总。金额一律用十进制字符串，避免浮点误差。
type Window struct {
	RequestCount            int64
	TokenCount              int64
	UncachedInputTokenCount int64
	CacheReadTokenCount     int64
	CacheCreationTokenCount int64
	OutputTokenCount        int64
	ChargeUSD               string
	UncachedInputChargeUSD  string
	OutputChargeUSD         string
	CacheReadChargeUSD      string
	CacheCreationChargeUSD  string
	ListChargeUSD           string
	CacheSavedUSD           string
}

// Point 是一个时间桶的用量与费用。
type Point struct {
	BucketStart             time.Time
	RequestCount            int64
	TokenCount              int64
	UncachedInputTokenCount int64
	CacheReadTokenCount     int64
	CacheCreationTokenCount int64
	OutputTokenCount        int64
	ChargeUSD               string
	UncachedInputChargeUSD  string
	OutputChargeUSD         string
	CacheReadChargeUSD      string
	CacheCreationChargeUSD  string
	CacheSavedUSD           string
	AverageLatencyMs        float64
}

// Overview 是用量统计首屏所需的全部聚合：卡片当期/上期、趋势序列与上期序列。
type Overview struct {
	Bucket         string
	From           time.Time
	To             time.Time
	PreviousFrom   time.Time
	PreviousTo     time.Time
	Current        Window
	Previous       Window
	Series         []Point
	PreviousSeries []Point
}

// TrendParams 是按维度拆分的趋势查询条件。
type TrendParams struct {
	UserID int64
	From   time.Time
	To     time.Time
	Bucket string
	TZ     string
	// Dimension 取 model / api_key，与维度排行同名。
	Dimension string
	Filters
}

// TrendSlice 是某个时间桶里某个分组的值。
type TrendSlice struct {
	GroupID      string
	RequestCount int64
	TokenCount   int64
	ChargeUSD    string
}

// TrendPoint 是一个时间桶，含该桶下各分组的拆分。
type TrendPoint struct {
	BucketStart time.Time
	Slices      []TrendSlice
}

// TrendGroup 是图例条目，按窗口内总消费降序，前端据此定颜色与堆叠顺序。
type TrendGroup struct {
	ID           string
	Name         string
	RequestCount int64
	TokenCount   int64
	ChargeUSD    string
}

// Trend 是按维度拆分的趋势序列。
type Trend struct {
	Bucket    string
	Dimension string
	From      time.Time
	To        time.Time
	Groups    []TrendGroup
	Series    []TrendPoint
}

// OtherGroupID 标记被折叠的尾部分组。趋势图上十几种颜色读不出来，
// 只保留消费靠前的若干个，其余合并到这一项。
const OtherGroupID = "__other__"

// trendTopN 是趋势图保留的分组数上限，超出的并入 OtherGroupID。
const trendTopN = 6

// Trend 返回按 model / api_key 拆分的分桶序列。
// 空桶在这里补齐：SQL 若按「桶 × 分组」补齐会放大成桶数乘分组数行，
// 而前端需要完整的时间轴，否则「最近几天没用过」会被压缩掉看不出来。
func (s *Service) Trend(ctx context.Context, params TrendParams) (Trend, *consoleservice.Error) {
	bucket, err := normalizeBucket(params.Bucket)
	if err != nil {
		return Trend{}, err
	}
	dimension, err := normalizeGroupBy(params.Dimension)
	if err != nil {
		return Trend{}, err
	}
	if !params.To.After(params.From) {
		return Trend{}, consoleservice.InvalidArgument("to", "to must be later than from.")
	}
	if err := checkBucketCount(params.To.Sub(params.From)/2, bucket); err != nil {
		return Trend{}, err
	}
	tz := normalizeTZ(params.TZ)

	rows, sqlErr := s.store.ListConsoleUsageTrendByGroup(ctx, sqlc.ListConsoleUsageTrendByGroupParams{
		UserID:      params.UserID,
		FromTime:    pgtype.Timestamptz{Time: params.From, Valid: true},
		ToTime:      pgtype.Timestamptz{Time: params.To, Valid: true},
		Tz:          tz,
		Bucket:      bucket,
		Dimension:   dimension,
		ApiKeyIds:   emptyInts(params.APIKeyIDs),
		ModelIds:    emptyStrings(params.ModelIDs),
		Endpoints:   emptyStrings(consolerequests.InternalEndpoints(params.Endpoints)),
		StreamTypes: emptyStrings(params.StreamTypes),
		TopN:        trendTopN,
	})
	if sqlErr != nil {
		return Trend{}, consoleservice.RequestUnavailable("list usage trend by group", sqlErr)
	}

	return Trend{
		Bucket:    bucket,
		Dimension: dimension,
		From:      params.From,
		To:        params.To,
		Groups:    trendGroups(rows),
		Series:    trendSeries(rows, params.From, params.To, bucket, tz),
	}, nil
}

// trendGroups 汇总各分组在整个窗口的合计，按消费降序；__other__ 永远排最后。
func trendGroups(rows []sqlc.ListConsoleUsageTrendByGroupRow) []TrendGroup {
	type acc struct {
		charge   float64
		name     string
		requests int64
		tokens   int64
	}
	order := make([]string, 0, trendTopN+1)
	seen := make(map[string]*acc, trendTopN+1)
	for _, row := range rows {
		item, ok := seen[row.GroupID]
		if !ok {
			item = &acc{name: row.GroupName}
			seen[row.GroupID] = item
			order = append(order, row.GroupID)
		}
		item.charge += numericFloat(row.ChargeUsd)
		item.requests += row.RequestCount
		item.tokens += row.TokenCount
	}
	groups := make([]TrendGroup, 0, len(order))
	for _, id := range order {
		item := seen[id]
		groups = append(groups, TrendGroup{
			ChargeUSD:    strconv.FormatFloat(item.charge, 'f', -1, 64),
			ID:           id,
			Name:         item.name,
			RequestCount: item.requests,
			TokenCount:   item.tokens,
		})
	}
	sort.SliceStable(groups, func(i, j int) bool {
		if groups[i].ID == OtherGroupID {
			return false
		}
		if groups[j].ID == OtherGroupID {
			return true
		}
		left, right := numericOf(groups[i].ChargeUSD), numericOf(groups[j].ChargeUSD)
		if left != right {
			return left > right
		}
		return groups[i].ID < groups[j].ID
	})
	return groups
}

// trendSeries 把稀疏的「桶 × 分组」行铺回完整时间轴。
func trendSeries(
	rows []sqlc.ListConsoleUsageTrendByGroupRow,
	from, to time.Time,
	bucket, tz string,
) []TrendPoint {
	byBucket := make(map[int64][]TrendSlice, len(rows))
	for _, row := range rows {
		key := row.BucketStart.Time.UTC().Unix()
		byBucket[key] = append(byBucket[key], TrendSlice{
			ChargeUSD:    opsutil.NumericString(row.ChargeUsd),
			GroupID:      row.GroupID,
			RequestCount: row.RequestCount,
			TokenCount:   row.TokenCount,
		})
	}
	starts := bucketStarts(from, to, bucket, tz)
	series := make([]TrendPoint, 0, len(starts))
	for _, start := range starts {
		series = append(series, TrendPoint{
			BucketStart: start,
			Slices:      byBucket[start.UTC().Unix()],
		})
	}
	return series
}

// bucketStarts 按时区生成完整的桶起点。
// 用 AddDate 而不是加固定 24h：跨夏令时的那一天不是 24 小时，
// 固定步长会让之后所有桶偏移一小时，跟 date_trunc 的结果对不上。
func bucketStarts(from, to time.Time, bucket, tz string) []time.Time {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc = time.UTC
	}
	local := from.In(loc)
	var cursor time.Time
	switch bucket {
	case BucketMinute:
		cursor = time.Date(local.Year(), local.Month(), local.Day(), local.Hour(), local.Minute(), 0, 0, loc)
	case BucketHour:
		cursor = time.Date(local.Year(), local.Month(), local.Day(), local.Hour(), 0, 0, 0, loc)
	default:
		cursor = time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	}

	out := make([]time.Time, 0, 32)
	for cursor.Before(to) && len(out) <= maxBuckets {
		out = append(out, cursor)
		switch bucket {
		case BucketMinute:
			cursor = cursor.Add(time.Minute)
		case BucketHour:
			cursor = cursor.Add(time.Hour)
		default:
			cursor = cursor.AddDate(0, 0, 1)
		}
	}
	return out
}

func numericFloat(value pgtype.Numeric) float64 {
	return numericOf(opsutil.NumericString(value))
}

func numericOf(value string) float64 {
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0
	}
	return parsed
}

// GroupItem 是维度排行的一行。
type GroupItem struct {
	ID               string
	Name             string
	RequestCount     int64
	TokenCount       int64
	ChargeUSD        string
	IngressProtocol  string
	InputPricePer1M  *string
	OutputPricePer1M *string
}

// FilterOption 是下拉筛选项。
type FilterOption struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// ModelOption 是模型筛选项。
type ModelOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// UsageFilters 是用量统计筛选栏所需的选项集合。
type UsageFilters struct {
	APIKeys []FilterOption
	Models  []ModelOption
}

// Service 提供 Console 用量统计只读查询。
type Service struct {
	store Store
}

// NewService 创建用量统计服务。
func NewService(store Store) *Service {
	return &Service{store: store}
}

// Overview 返回当前周期与上一周期的汇总，以及两段分桶序列。
// 上一周期定义为紧邻当前周期、等长的时间窗：[from-(to-from), from)。
func (s *Service) Overview(ctx context.Context, params OverviewParams) (Overview, *consoleservice.Error) {
	bucket, err := normalizeBucket(params.Bucket)
	if err != nil {
		return Overview{}, err
	}
	if !params.To.After(params.From) {
		return Overview{}, consoleservice.InvalidArgument("to", "to must be later than from.")
	}
	span := params.To.Sub(params.From)
	previousFrom := params.From.Add(-span)
	if err := checkBucketCount(span, bucket); err != nil {
		return Overview{}, err
	}

	current, sqlErr := s.store.SummarizeConsoleUsageWindow(ctx, toWindowSQL(params.UserID, params.Filters, &params.From, &params.To))
	if sqlErr != nil {
		return Overview{}, consoleservice.RequestUnavailable("summarize usage window", sqlErr)
	}
	previous, sqlErr := s.store.SummarizeConsoleUsageWindow(ctx, toWindowSQL(params.UserID, params.Filters, &previousFrom, &params.From))
	if sqlErr != nil {
		return Overview{}, consoleservice.RequestUnavailable("summarize previous usage window", sqlErr)
	}

	// 两个周期首尾相接，一次查完再按 from 切分，省掉一次全表扫描。
	rows, sqlErr := s.store.ListConsoleUsageTimeseries(ctx, sqlc.ListConsoleUsageTimeseriesParams{
		UserID:      params.UserID,
		FromTime:    pgtype.Timestamptz{Time: previousFrom, Valid: true},
		ToTime:      pgtype.Timestamptz{Time: params.To, Valid: true},
		Bucket:      bucket,
		Tz:          normalizeTZ(params.TZ),
		ApiKeyIds:   emptyInts(params.APIKeyIDs),
		ModelIds:    emptyStrings(params.ModelIDs),
		Endpoints:   emptyStrings(consolerequests.InternalEndpoints(params.Endpoints)),
		StreamTypes: emptyStrings(params.StreamTypes),
		Q:           textNarg(params.Q),
	})
	if sqlErr != nil {
		return Overview{}, consoleservice.RequestUnavailable("list usage timeseries", sqlErr)
	}

	series := make([]Point, 0, len(rows))
	previousSeries := make([]Point, 0, len(rows))
	for _, row := range rows {
		point := toPoint(row)
		if point.BucketStart.Before(params.From) {
			previousSeries = append(previousSeries, point)
			continue
		}
		series = append(series, point)
	}

	return Overview{
		Bucket:         bucket,
		From:           params.From,
		To:             params.To,
		PreviousFrom:   previousFrom,
		PreviousTo:     params.From,
		Current:        toWindow(current),
		Previous:       toWindow(previous),
		Series:         series,
		PreviousSeries: previousSeries,
	}, nil
}

// Groups 返回按模型/密钥分组的用量排行，按消费降序。
func (s *Service) Groups(ctx context.Context, params GroupParams) ([]GroupItem, *consoleservice.Error) {
	from, to := &params.From, &params.To
	switch params.By {
	case GroupByModel:
		rows, err := s.store.ListConsoleUsageByModel(ctx, sqlc.ListConsoleUsageByModelParams{
			UserID:      params.UserID,
			FromTime:    tsNarg(from),
			ToTime:      tsNarg(to),
			ApiKeyIds:   emptyInts(params.APIKeyIDs),
			ModelIds:    emptyStrings(params.ModelIDs),
			Endpoints:   emptyStrings(consolerequests.InternalEndpoints(params.Endpoints)),
			StreamTypes: emptyStrings(params.StreamTypes),
			Q:           textNarg(params.Q),
			RowLimit:    groupRowLimit,
		})
		if err != nil {
			return nil, consoleservice.RequestUnavailable("list usage by model", err)
		}
		out := make([]GroupItem, 0, len(rows))
		for _, row := range rows {
			out = append(out, GroupItem{
				ID:               row.GroupID,
				Name:             row.GroupName,
				RequestCount:     row.RequestCount,
				TokenCount:       row.TokenCount,
				ChargeUSD:        opsutil.NumericString(row.ChargeUsd),
				IngressProtocol:  row.IngressProtocol,
				InputPricePer1M:  opsutil.NumericStringPtr(row.InputPricePer1m),
				OutputPricePer1M: opsutil.NumericStringPtr(row.OutputPricePer1m),
			})
		}
		return out, nil
	case GroupByAPIKey:
		rows, err := s.store.ListConsoleUsageByAPIKey(ctx, sqlc.ListConsoleUsageByAPIKeyParams{
			UserID:      params.UserID,
			FromTime:    tsNarg(from),
			ToTime:      tsNarg(to),
			ApiKeyIds:   emptyInts(params.APIKeyIDs),
			ModelIds:    emptyStrings(params.ModelIDs),
			Endpoints:   emptyStrings(consolerequests.InternalEndpoints(params.Endpoints)),
			StreamTypes: emptyStrings(params.StreamTypes),
			Q:           textNarg(params.Q),
			RowLimit:    groupRowLimit,
		})
		if err != nil {
			return nil, consoleservice.RequestUnavailable("list usage by api key", err)
		}
		return apiKeyGroups(rows), nil
	default:
		return nil, consoleservice.InvalidArgument("by", "by must be model or api_key.")
	}
}

// Filters 返回用量统计筛选栏所需的密钥与模型选项。
func (s *Service) Filters(ctx context.Context, userID int64) (UsageFilters, *consoleservice.Error) {
	keys, err := s.store.ListConsoleFilterAPIKeys(ctx, userID)
	if err != nil {
		return UsageFilters{}, consoleservice.RequestUnavailable("list user api keys", err)
	}
	models, err := s.store.ListConsoleUsageFilterModels(ctx, userID)
	if err != nil {
		return UsageFilters{}, consoleservice.RequestUnavailable("list usage filter models", err)
	}
	out := UsageFilters{
		APIKeys: make([]FilterOption, 0, len(keys)),
		Models:  make([]ModelOption, 0, len(models)),
	}
	for _, key := range keys {
		out.APIKeys = append(out.APIKeys, FilterOption{ID: key.ID, Name: key.Name})
	}
	for _, model := range models {
		out.Models = append(out.Models, ModelOption{ID: model.ModelID, Name: model.DisplayName})
	}
	return out, nil
}

func apiKeyGroups(rows []sqlc.ListConsoleUsageByAPIKeyRow) []GroupItem {
	out := make([]GroupItem, 0, len(rows))
	for _, row := range rows {
		out = append(out, GroupItem{
			ID:           strconv.FormatInt(row.GroupID, 10),
			Name:         row.GroupName,
			RequestCount: row.RequestCount,
			TokenCount:   row.TokenCount,
			ChargeUSD:    opsutil.NumericString(row.ChargeUsd),
		})
	}
	return out
}

func normalizeGroupBy(dimension string) (string, *consoleservice.Error) {
	switch dimension {
	case "":
		return GroupByModel, nil
	case GroupByModel, GroupByAPIKey:
		return dimension, nil
	default:
		return "", consoleservice.InvalidArgument("by", "by must be model or api_key.")
	}
}

func normalizeBucket(bucket string) (string, *consoleservice.Error) {
	switch bucket {
	case "":
		return BucketDay, nil
	case BucketMinute, BucketHour, BucketDay:
		return bucket, nil
	default:
		return "", consoleservice.InvalidArgument("bucket", "bucket must be minute, hour, or day.")
	}
}

// normalizeTZ 校验 IANA 时区名；无法解析时回退 UTC，不让分桶因为客户端传错而失败。
func normalizeTZ(tz string) string {
	if tz == "" {
		return "UTC"
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return "UTC"
	}
	return tz
}

func bucketDuration(bucket string) time.Duration {
	switch bucket {
	case BucketMinute:
		return time.Minute
	case BucketHour:
		return time.Hour
	default:
		return 24 * time.Hour
	}
}

func checkBucketCount(span time.Duration, bucket string) *consoleservice.Error {
	// 当前周期与上一周期都要出图，按两倍窗口估算桶数。
	estimated := int64(2*span/bucketDuration(bucket)) + 2
	if estimated > maxBuckets {
		return consoleservice.InvalidArgument("bucket", "the selected range is too long for this bucket; choose a coarser bucket.")
	}
	return nil
}

func toWindowSQL(userID int64, filters Filters, from, to *time.Time) sqlc.SummarizeConsoleUsageWindowParams {
	return sqlc.SummarizeConsoleUsageWindowParams{
		UserID:      userID,
		FromTime:    tsNarg(from),
		ToTime:      tsNarg(to),
		ApiKeyIds:   emptyInts(filters.APIKeyIDs),
		ModelIds:    emptyStrings(filters.ModelIDs),
		Endpoints:   emptyStrings(consolerequests.InternalEndpoints(filters.Endpoints)),
		StreamTypes: emptyStrings(filters.StreamTypes),
		Q:           textNarg(filters.Q),
	}
}

func toWindow(row sqlc.SummarizeConsoleUsageWindowRow) Window {
	return Window{
		RequestCount:            row.RequestCount,
		TokenCount:              row.TokenCount,
		UncachedInputTokenCount: row.UncachedInputTokenCount,
		CacheReadTokenCount:     row.CacheReadTokenCount,
		CacheCreationTokenCount: row.CacheCreationTokenCount,
		OutputTokenCount:        row.OutputTokenCount,
		ChargeUSD:               opsutil.NumericString(row.ChargeUsd),
		UncachedInputChargeUSD:  opsutil.NumericString(row.UncachedInputChargeUsd),
		OutputChargeUSD:         opsutil.NumericString(row.OutputChargeUsd),
		CacheReadChargeUSD:      opsutil.NumericString(row.CacheReadChargeUsd),
		CacheCreationChargeUSD:  opsutil.NumericString(row.CacheCreationChargeUsd),
		ListChargeUSD:           opsutil.NumericString(row.ListChargeUsd),
		CacheSavedUSD:           opsutil.NumericString(row.CacheSavedUsd),
	}
}

func toPoint(row sqlc.ListConsoleUsageTimeseriesRow) Point {
	return Point{
		BucketStart:             row.BucketStart.Time,
		RequestCount:            row.RequestCount,
		TokenCount:              row.TokenCount,
		UncachedInputTokenCount: row.UncachedInputTokenCount,
		CacheReadTokenCount:     row.CacheReadTokenCount,
		CacheCreationTokenCount: row.CacheCreationTokenCount,
		OutputTokenCount:        row.OutputTokenCount,
		ChargeUSD:               opsutil.NumericString(row.ChargeUsd),
		UncachedInputChargeUSD:  opsutil.NumericString(row.UncachedInputChargeUsd),
		OutputChargeUSD:         opsutil.NumericString(row.OutputChargeUsd),
		CacheReadChargeUSD:      opsutil.NumericString(row.CacheReadChargeUsd),
		CacheCreationChargeUSD:  opsutil.NumericString(row.CacheCreationChargeUsd),
		CacheSavedUSD:           opsutil.NumericString(row.CacheSavedUsd),
		AverageLatencyMs:        row.AverageLatencyMs,
	}
}

func emptyInts(values []int64) []int64 {
	if values == nil {
		return []int64{}
	}
	return values
}

func emptyStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func textNarg(value string) pgtype.Text {
	if value == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: value, Valid: true}
}

func tsNarg(value *time.Time) pgtype.Timestamptz {
	if value == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *value, Valid: true}
}
