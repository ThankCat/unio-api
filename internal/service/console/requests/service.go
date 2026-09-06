package requests

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/admin/opsutil"
	consoleservice "github.com/ThankCat/unio-gateway/internal/service/console"
)

// Store 是客户请求日志只读查询所需的存储能力。
type Store interface {
	ListConsoleBilledRequests(context.Context, sqlc.ListConsoleBilledRequestsParams) ([]sqlc.ListConsoleBilledRequestsRow, error)
	CountConsoleBilledRequests(context.Context, sqlc.CountConsoleBilledRequestsParams) (int64, error)
	SummarizeConsoleBilledRequests(context.Context, sqlc.SummarizeConsoleBilledRequestsParams) (sqlc.SummarizeConsoleBilledRequestsRow, error)
	ListConsoleBilledRequestTopModels(context.Context, sqlc.ListConsoleBilledRequestTopModelsParams) ([]sqlc.ListConsoleBilledRequestTopModelsRow, error)
	// 卡片热力条复用用量统计的分桶查询，口径与这里的汇总一致。
	ListConsoleUsageTimeseries(context.Context, sqlc.ListConsoleUsageTimeseriesParams) ([]sqlc.ListConsoleUsageTimeseriesRow, error)
	ListConsoleFilterAPIKeys(context.Context, int64) ([]sqlc.ListConsoleFilterAPIKeysRow, error)
	ListConsoleBilledRequestEndpoints(context.Context, int64) ([]string, error)
	ListConsoleBilledRequestStreamTypes(context.Context, int64) ([]bool, error)
}

// ListParams 是当前用户实际扣费请求列表的查询条件。
type ListParams struct {
	UserID      int64
	APIKeyIDs   []int64
	Endpoints   []string
	StreamTypes []string
	Q           string
	From        *time.Time
	To          *time.Time
	SortField   string
	SortDesc    bool
	Limit       int32
	Offset      int32
}

// Item 是客户可见的实际扣费请求列表项。
type Item struct {
	ID                          int64
	RequestID                   string
	CreatedAt                   time.Time
	ClientIP                    string
	APIKeyID                    int64
	APIKeyName                  string
	APIKeyPrefix                string
	Endpoint                    string
	Stream                      bool
	RequestedModelID            string
	ModelDisplayName            string
	IngressProtocol             string
	InputPricePer1M             *string
	OutputPricePer1M            *string
	CacheReadPricePer1M         *string
	CacheCreation5mPricePer1M   *string
	CacheCreation1hPricePer1M   *string
	CacheCreation30mPricePer1M  *string
	ReasoningOutputPricePer1M   *string
	PriceServiceTier            *string
	ReasoningEffort             *string
	UncachedInputTokens         int64
	CacheReadInputTokens        int64
	CacheCreation5mInputTokens  int64
	CacheCreation1hInputTokens  int64
	CacheCreation30mInputTokens int64
	InputTokens                 int64
	OutputTokens                int64
	ReasoningOutputTokens       int64
	LatencyMs                   *int64
	FirstTokenMs                *int64
	TPS                         *float64
	UserChargeUSD               string
}

// SummaryParams 是账户累计汇总条件。筛选口径与列表相同；From/To 可空。
type SummaryParams struct {
	UserID      int64
	APIKeyIDs   []int64
	Endpoints   []string
	StreamTypes []string
	Q           string
	From        *time.Time
	To          *time.Time
	// SeriesFrom/SeriesTo 只控制热力条展示窗；省略时回落到 From/To。
	SeriesFrom *time.Time
	SeriesTo   *time.Time
	// PreviousFrom/PreviousTo 控制卡片环比窗；省略时使用等长上一周期。
	PreviousFrom *time.Time
	PreviousTo   *time.Time
	// Bucket/TZ 供卡片热力条分桶；留空时按天、按 UTC。
	Bucket string
	TZ     string
}

func supportedBucket(bucket string) bool {
	switch bucket {
	case "minute", "hour", "day", "week", "month", "quarter", "year":
		return true
	default:
		return false
	}
}

func previousWindow(params SummaryParams) (*time.Time, *time.Time) {
	if params.PreviousFrom != nil || params.PreviousTo != nil {
		return params.PreviousFrom, params.PreviousTo
	}
	if params.From == nil || params.To == nil {
		return nil, nil
	}
	d := params.To.Sub(*params.From)
	from := params.From.Add(-d)
	to := *params.From
	return &from, &to
}

// SummaryModel 是时间窗内实际扣费次数最多的模型之一。
type SummaryModel struct {
	ModelID          string
	DisplayName      string
	RequestCount     int64
	IngressProtocol  string
	InputPricePer1M  *string
	OutputPricePer1M *string
}

// Summary 是当前用户实际扣费请求的累计指标。
type Summary struct {
	RequestCount            int64
	StreamCount             int64
	TokenCount              int64
	InputTokenCount         int64
	OutputTokenCount        int64
	UncachedInputTokenCount int64
	CacheReadTokenCount     int64
	CacheCreationTokenCount int64
	ChargeUSD               string
	UncachedInputChargeUSD  string
	OutputChargeUSD         string
	CacheReadChargeUSD      string
	CacheCreationChargeUSD  string
	ListChargeUSD           string
	AverageLatencyMs        float64
	AverageFirstTokenMs     float64
	MedianLatencyMs         float64
	AverageTPS              float64
	TopModels               []SummaryModel
	// Previous 是等长的上一周期汇总，只在给了完整时间窗时有值，用于卡片环比。
	Previous *Window
	// Series 是当前周期的分桶序列，供卡片热力条使用。
	Series []Point
}

// Window 是上一周期的对照值，只保留卡片会用到的四个指标。
type Window struct {
	RequestCount     int64
	TokenCount       int64
	ChargeUSD        string
	AverageLatencyMs float64
}

// Point 是一个时间桶，热力条按它着色。
type Point struct {
	BucketStart      time.Time
	BucketEnd        time.Time
	IsFuture         bool
	RequestCount     int64
	TokenCount       int64
	ChargeUSD        string
	AverageLatencyMs float64
}

// FilterOption 是下拉筛选项。
type FilterOption struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// Filters 是当前用户的 API Key，以及扣费请求上出现过的端点和类型。
type Filters struct {
	APIKeys     []FilterOption
	Endpoints   []string
	StreamTypes []string
}

// Service 提供 Console 请求日志只读查询。
type Service struct {
	store Store
}

// NewService 创建客户请求日志服务。
func NewService(store Store) *Service {
	return &Service{store: store}
}

var _ Store = (*sqlc.Queries)(nil)

// List 返回当前用户的实际扣费请求分页列表。
func (s *Service) List(ctx context.Context, params ListParams) ([]Item, int64, *consoleservice.Error) {
	listParams := toListSQL(params)
	rows, err := s.store.ListConsoleBilledRequests(ctx, listParams)
	if err != nil {
		return nil, 0, consoleservice.RequestUnavailable("list charged requests", err)
	}
	total := int64(0)
	if len(rows) > 0 {
		total = rows[0].TotalCount
	} else if params.Offset > 0 {
		total, err = s.store.CountConsoleBilledRequests(ctx, toCountSQL(params))
		if err != nil {
			return nil, 0, consoleservice.RequestUnavailable("count charged requests", err)
		}
	}
	items := make([]Item, 0, len(rows))
	for _, row := range rows {
		items = append(items, toItem(row))
	}
	return items, total, nil
}

// Summary 返回当前用户实际扣费请求的累计指标；筛选口径与列表相同。
func (s *Service) Summary(ctx context.Context, params SummaryParams) (Summary, *consoleservice.Error) {
	// 窗口与分桶合法性在任何查询之前一次判定：非法输入统一 400，不让主汇总先落库再由 compare 半路报错。
	plan, planErr := planCompare(params)
	if planErr != nil {
		return Summary{}, planErr
	}
	bounds := toSummarySQL(params)
	row, err := s.store.SummarizeConsoleBilledRequests(ctx, bounds)
	if err != nil {
		return Summary{}, consoleservice.RequestUnavailable("summarize charged requests", err)
	}
	models, err := s.store.ListConsoleBilledRequestTopModels(ctx, sqlc.ListConsoleBilledRequestTopModelsParams{
		UserID:      params.UserID,
		ApiKeyIds:   bounds.ApiKeyIds,
		Endpoints:   bounds.Endpoints,
		StreamTypes: bounds.StreamTypes,
		Q:           bounds.Q,
		FromTime:    bounds.FromTime,
		ToTime:      bounds.ToTime,
	})
	if err != nil {
		return Summary{}, consoleservice.RequestUnavailable("list top billed models", err)
	}
	topModels := make([]SummaryModel, 0, len(models))
	for _, model := range models {
		topModels = append(topModels, SummaryModel{
			ModelID:          model.RequestedModelID,
			DisplayName:      model.ModelDisplayName,
			RequestCount:     model.RequestCount,
			IngressProtocol:  model.IngressProtocol,
			InputPricePer1M:  opsutil.NumericStringPtr(model.InputPricePer1m),
			OutputPricePer1M: opsutil.NumericStringPtr(model.OutputPricePer1m),
		})
	}
	summary := Summary{
		RequestCount:            row.RequestCount,
		StreamCount:             row.StreamCount,
		TokenCount:              row.TokenCount,
		InputTokenCount:         row.InputTokenCount,
		OutputTokenCount:        row.OutputTokenCount,
		UncachedInputTokenCount: row.UncachedInputTokenCount,
		CacheReadTokenCount:     row.CacheReadTokenCount,
		CacheCreationTokenCount: row.CacheCreationTokenCount,
		ChargeUSD:               opsutil.NumericString(row.ChargeUsd),
		UncachedInputChargeUSD:  opsutil.NumericString(row.UncachedInputChargeUsd),
		OutputChargeUSD:         opsutil.NumericString(row.OutputChargeUsd),
		CacheReadChargeUSD:      opsutil.NumericString(row.CacheReadChargeUsd),
		CacheCreationChargeUSD:  opsutil.NumericString(row.CacheCreationChargeUsd),
		ListChargeUSD:           opsutil.NumericString(row.ListChargeUsd),
		AverageLatencyMs:        row.AverageLatencyMs,
		AverageFirstTokenMs:     row.AverageFirstTokenMs,
		MedianLatencyMs:         row.MedianLatencyMs,
		AverageTPS:              row.AverageTps,
		TopModels:               topModels,
	}

	// 环比和热力条都要求有时间窗；不给窗口时（全量统计）没有"上一周期"可言。
	if plan == nil {
		return summary, nil
	}
	previous, series, compareErr := s.compare(ctx, params, bounds, *plan)
	if compareErr != nil {
		return Summary{}, compareErr
	}
	summary.Previous = previous
	summary.Series = series
	return summary, nil
}

// maxSeriesBuckets 限制热力条展示窗一次生成的桶数。SQL 侧用 generate_series 为展示窗补全空桶，
// 桶数只受「窗长 ÷ 桶宽」约束；不设上限时 bucket=minute 配多年窗口会先打 PostgreSQL 再打进程内存。
// 与用量页 maxBuckets=1500（当前 + 上一周期）同量级。
const maxSeriesBuckets = 1500

// comparePlan 是 Summary 在落库前对环比窗、展示窗与分桶做完的一次性判定。
type comparePlan struct {
	bucket       string
	tz           string
	previousFrom time.Time
	previousTo   time.Time
	seriesFrom   time.Time
	seriesTo     time.Time
}

// planCompare 校验环比与热力条所需的全部输入；没有完整时间窗时返回 nil（跳过 compare）。
// 起止相等时仍要允许生成完整的展示窗，例如当天刚过 00:00 的 24 个小时格。
func planCompare(params SummaryParams) (*comparePlan, *consoleservice.Error) {
	if params.From != nil && params.To != nil && params.To.Before(*params.From) {
		return nil, consoleservice.InvalidArgument("to", "to must be later than or equal to from.")
	}
	if params.From == nil || params.To == nil {
		return nil, nil
	}
	bucket, tz := params.Bucket, params.TZ
	if bucket == "" {
		bucket = "day"
	}
	if !supportedBucket(bucket) {
		return nil, consoleservice.InvalidArgument("bucket", "bucket must be minute, hour, day, week, month, quarter, or year.")
	}
	if tz == "" {
		tz = "UTC"
	}
	previousFrom, previousTo := previousWindow(params)
	if previousFrom == nil || previousTo == nil || previousTo.Before(*previousFrom) {
		return nil, consoleservice.InvalidArgument("previous_to", "previous_to must be later than or equal to previous_from.")
	}
	seriesFrom, seriesTo := params.From, params.To
	if params.SeriesFrom != nil || params.SeriesTo != nil {
		if params.SeriesFrom == nil || params.SeriesTo == nil || params.SeriesTo.Before(*params.SeriesFrom) {
			return nil, consoleservice.InvalidArgument("series_to", "series_to must be later than or equal to series_from.")
		}
		seriesFrom, seriesTo = params.SeriesFrom, params.SeriesTo
	}
	if estimateBuckets(seriesTo.Sub(*seriesFrom), bucket) > maxSeriesBuckets {
		return nil, consoleservice.InvalidArgument("bucket", "the selected range is too long for this bucket; choose a coarser bucket.")
	}
	return &comparePlan{
		bucket:       bucket,
		tz:           tz,
		previousFrom: *previousFrom,
		previousTo:   *previousTo,
		seriesFrom:   *seriesFrom,
		seriesTo:     *seriesTo,
	}, nil
}

// estimateBuckets 按桶宽下界估算展示窗桶数（月/季/年按最短自然长度取整，宁可略高估）。
// 边界对齐（date_trunc 把窗两端各扩到整桶）最多再多两格。
func estimateBuckets(span time.Duration, bucket string) int64 {
	var width time.Duration
	switch bucket {
	case "minute":
		width = time.Minute
	case "hour":
		width = time.Hour
	case "day":
		width = 24 * time.Hour
	case "week":
		width = 7 * 24 * time.Hour
	case "month":
		width = 28 * 24 * time.Hour
	case "quarter":
		width = 90 * 24 * time.Hour
	case "year":
		width = 365 * 24 * time.Hour
	default:
		width = 24 * time.Hour
	}
	if span < 0 {
		span = 0
	}
	return int64(span/width) + 2
}

// compare 取等长的上一周期汇总，以及当前周期的分桶序列。
// 分桶复用用量统计那条 timeseries：两边口径本就一致，没必要再写一份聚合。
func (s *Service) compare(
	ctx context.Context,
	params SummaryParams,
	bounds sqlc.SummarizeConsoleBilledRequestsParams,
	plan comparePlan,
) (*Window, []Point, *consoleservice.Error) {
	prevBounds := bounds
	prevBounds.FromTime = pgtype.Timestamptz{Time: plan.previousFrom, Valid: true}
	prevBounds.ToTime = pgtype.Timestamptz{Time: plan.previousTo, Valid: true}
	prevRow, err := s.store.SummarizeConsoleBilledRequests(ctx, prevBounds)
	if err != nil {
		return nil, nil, consoleservice.RequestUnavailable("summarize previous charged requests", err)
	}

	rows, err := s.store.ListConsoleUsageTimeseries(ctx, sqlc.ListConsoleUsageTimeseriesParams{
		UserID:      params.UserID,
		FromTime:    pgtype.Timestamptz{Time: *params.From, Valid: true},
		ToTime:      pgtype.Timestamptz{Time: *params.To, Valid: true},
		Bucket:      plan.bucket,
		Tz:          plan.tz,
		ApiKeyIds:   bounds.ApiKeyIds,
		ModelIds:    []string{},
		Endpoints:   bounds.Endpoints,
		StreamTypes: bounds.StreamTypes,
		Q:           bounds.Q,
		SeriesFrom:  pgtype.Timestamptz{Time: plan.seriesFrom, Valid: true},
		SeriesTo:    pgtype.Timestamptz{Time: plan.seriesTo, Valid: true},
	})
	if err != nil {
		return nil, nil, consoleservice.RequestUnavailable("list request timeseries", err)
	}

	series := make([]Point, 0, len(rows))
	for _, row := range rows {
		series = append(series, Point{
			AverageLatencyMs: row.AverageLatencyMs,
			BucketStart:      row.BucketStart.Time,
			BucketEnd:        row.BucketEnd.Time,
			ChargeUSD:        opsutil.NumericString(row.ChargeUsd),
			IsFuture:         !row.BucketStart.Time.Before(*params.To),
			RequestCount:     row.RequestCount,
			TokenCount:       row.TokenCount,
		})
	}
	return &Window{
		AverageLatencyMs: prevRow.AverageLatencyMs,
		ChargeUSD:        opsutil.NumericString(prevRow.ChargeUsd),
		RequestCount:     prevRow.RequestCount,
		TokenCount:       prevRow.TokenCount,
	}, series, nil
}

// Filters 返回当前用户的 API Key，以及扣费请求上出现过的端点和类型。
func (s *Service) Filters(ctx context.Context, userID int64) (Filters, *consoleservice.Error) {
	keys, err := s.store.ListConsoleFilterAPIKeys(ctx, userID)
	if err != nil {
		return Filters{}, consoleservice.RequestUnavailable("list user api keys", err)
	}
	endpoints, err := s.store.ListConsoleBilledRequestEndpoints(ctx, userID)
	if err != nil {
		return Filters{}, consoleservice.RequestUnavailable("list billed request endpoints", err)
	}
	streams, err := s.store.ListConsoleBilledRequestStreamTypes(ctx, userID)
	if err != nil {
		return Filters{}, consoleservice.RequestUnavailable("list billed request stream types", err)
	}
	out := Filters{
		APIKeys:     make([]FilterOption, 0, len(keys)),
		Endpoints:   make([]string, 0, len(endpoints)),
		StreamTypes: make([]string, 0, len(streams)),
	}
	for _, key := range keys {
		out.APIKeys = append(out.APIKeys, FilterOption{ID: key.ID, Name: key.Name})
	}
	for _, endpoint := range endpoints {
		out.Endpoints = append(out.Endpoints, PublicEndpoint(endpoint))
	}
	for _, stream := range streams {
		out.StreamTypes = append(out.StreamTypes, publicStreamType(stream))
	}
	return out, nil
}

func publicStreamType(stream bool) string {
	if stream {
		return "stream"
	}
	return "sync"
}

func toItem(row sqlc.ListConsoleBilledRequestsRow) Item {
	item := Item{
		ID:                          row.ID,
		RequestID:                   row.RequestID,
		CreatedAt:                   row.CreatedAt.Time,
		ClientIP:                    textValue(row.ClientIp),
		APIKeyID:                    row.ApiKeyID,
		APIKeyName:                  textValue(row.ApiKeyName),
		APIKeyPrefix:                textValue(row.ApiKeyPrefix),
		Endpoint:                    PublicEndpoint(row.Endpoint),
		Stream:                      row.Stream,
		RequestedModelID:            row.RequestedModelID,
		ModelDisplayName:            modelDisplayName(row.ModelDisplayName, row.RequestedModelID),
		IngressProtocol:             row.IngressProtocol,
		InputPricePer1M:             opsutil.NumericStringPtr(row.InputPricePer1m),
		OutputPricePer1M:            opsutil.NumericStringPtr(row.OutputPricePer1m),
		CacheReadPricePer1M:         opsutil.NumericStringPtr(row.CacheReadPricePer1m),
		CacheCreation5mPricePer1M:   opsutil.NumericStringPtr(row.CacheCreation5mPricePer1m),
		CacheCreation1hPricePer1M:   opsutil.NumericStringPtr(row.CacheCreation1hPricePer1m),
		CacheCreation30mPricePer1M:  opsutil.NumericStringPtr(row.CacheCreation30mPricePer1m),
		ReasoningOutputPricePer1M:   opsutil.NumericStringPtr(row.ReasoningOutputPricePer1m),
		PriceServiceTier:            textPtr(row.PriceServiceTier),
		ReasoningEffort:             textPtr(row.ReasoningEffort),
		UncachedInputTokens:         row.UncachedInputTokens,
		CacheReadInputTokens:        row.CacheReadInputTokens,
		CacheCreation5mInputTokens:  row.CacheCreation5mInputTokens,
		CacheCreation1hInputTokens:  row.CacheCreation1hInputTokens,
		CacheCreation30mInputTokens: row.CacheCreation30mInputTokens,
		InputTokens:                 row.InputTokens,
		OutputTokens:                row.OutputTokens,
		ReasoningOutputTokens:       row.ReasoningOutputTokens,
		UserChargeUSD:               opsutil.NumericString(row.UserChargeUsd),
	}
	item.LatencyMs, item.FirstTokenMs, item.TPS = deriveTiming(
		row.Stream,
		row.StartedAt,
		row.CompletedAt,
		row.GatewayFirstTokenAt,
		row.OutputTokens,
	)
	return item
}

func toListSQL(params ListParams) sqlc.ListConsoleBilledRequestsParams {
	limit := params.Limit
	if limit <= 0 {
		limit = 20
	}
	return sqlc.ListConsoleBilledRequestsParams{
		UserID:      params.UserID,
		ApiKeyIds:   emptyInts(params.APIKeyIDs),
		Endpoints:   emptyStrings(InternalEndpoints(params.Endpoints)),
		StreamTypes: emptyStrings(params.StreamTypes),
		Q:           textNarg(params.Q),
		FromTime:    tsNarg(params.From),
		ToTime:      tsNarg(params.To),
		SortField:   textNarg(params.SortField),
		SortDesc:    pgtype.Bool{Bool: params.SortDesc, Valid: true},
		PageLimit:   limit,
		PageOffset:  params.Offset,
	}
}

func toSummarySQL(params SummaryParams) sqlc.SummarizeConsoleBilledRequestsParams {
	return sqlc.SummarizeConsoleBilledRequestsParams{
		UserID:      params.UserID,
		ApiKeyIds:   emptyInts(params.APIKeyIDs),
		Endpoints:   emptyStrings(InternalEndpoints(params.Endpoints)),
		StreamTypes: emptyStrings(params.StreamTypes),
		Q:           textNarg(params.Q),
		FromTime:    tsNarg(params.From),
		ToTime:      tsNarg(params.To),
	}
}

func toCountSQL(params ListParams) sqlc.CountConsoleBilledRequestsParams {
	return sqlc.CountConsoleBilledRequestsParams{
		UserID:      params.UserID,
		ApiKeyIds:   emptyInts(params.APIKeyIDs),
		Endpoints:   emptyStrings(InternalEndpoints(params.Endpoints)),
		StreamTypes: emptyStrings(params.StreamTypes),
		Q:           textNarg(params.Q),
		FromTime:    tsNarg(params.From),
		ToTime:      tsNarg(params.To),
	}
}

func deriveTiming(
	stream bool,
	started, completed, firstToken pgtype.Timestamptz,
	outputTokens int64,
) (latencyMs *int64, firstTokenMs *int64, tps *float64) {
	if started.Valid && completed.Valid {
		ms := completed.Time.Sub(started.Time).Milliseconds()
		if ms < 0 {
			ms = 0
		}
		latencyMs = &ms
	}
	if !stream || !firstToken.Valid || !started.Valid {
		return latencyMs, nil, nil
	}
	ttft := firstToken.Time.Sub(started.Time).Milliseconds()
	if ttft >= 0 {
		firstTokenMs = &ttft
	}
	if completed.Valid && outputTokens > 0 {
		genSec := completed.Time.Sub(firstToken.Time).Seconds()
		if genSec > 0 {
			value := float64(outputTokens) / genSec
			tps = &value
		}
	}
	return latencyMs, firstTokenMs, tps
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

func textValue(value pgtype.Text) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func modelDisplayName(displayName pgtype.Text, modelID string) string {
	if name := textValue(displayName); name != "" {
		return name
	}
	return modelID
}

func textPtr(value pgtype.Text) *string {
	if !value.Valid {
		return nil
	}
	s := value.String
	return &s
}
