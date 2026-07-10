// Package modelops 提供模型商品控制台（§3.4）的只读运维聚合。
// 模型口径：request_records.requested_model_id = models.model_id；金额仅 USD。
package modelops

import (
	"context"
	"time"

	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-api/internal/service/admin/opsutil"
)

// Store 是模型运维聚合所需的只读存储能力（由 *sqlc.Queries 满足）。
type Store interface {
	ModelsOpsCounts(ctx context.Context) (sqlc.ModelsOpsCountsRow, error)
	ModelsOpsSellability(ctx context.Context) (sqlc.ModelsOpsSellabilityRow, error)
	ModelsOpsPriceCompleteness(ctx context.Context) (sqlc.ModelsOpsPriceCompletenessRow, error)
	ModelsOpsRequestAggregate(ctx context.Context, arg sqlc.ModelsOpsRequestAggregateParams) (sqlc.ModelsOpsRequestAggregateRow, error)
	ModelsOpsMarginUSD(ctx context.Context, arg sqlc.ModelsOpsMarginUSDParams) (sqlc.ModelsOpsMarginUSDRow, error)
	ModelsOpsTable(ctx context.Context, arg sqlc.ModelsOpsTableParams) ([]sqlc.ModelsOpsTableRow, error)
	ModelsOpsTableCount(ctx context.Context, arg sqlc.ModelsOpsTableCountParams) (int64, error)
	ModelOpsDetail(ctx context.Context, arg sqlc.ModelOpsDetailParams) (sqlc.ModelOpsDetailRow, error)
	ModelOpsChannels(ctx context.Context, arg sqlc.ModelOpsChannelsParams) ([]sqlc.ModelOpsChannelsRow, error)
	ModelOpsPerformanceTimeseries(ctx context.Context, arg sqlc.ModelOpsPerformanceTimeseriesParams) ([]sqlc.ModelOpsPerformanceTimeseriesRow, error)
	ModelOpsRequests(ctx context.Context, arg sqlc.ModelOpsRequestsParams) ([]sqlc.ModelOpsRequestsRow, error)
	ModelOpsRequestsCount(ctx context.Context, arg sqlc.ModelOpsRequestsCountParams) (int64, error)
}

// Service 提供模型运维只读聚合。
type Service struct {
	store Store
}

// NewService 创建模型运维聚合服务。
func NewService(store Store) *Service {
	return &Service{store: store}
}

// Summary 是模型总览（8 卡）。
type Summary struct {
	Total          int64
	Enabled        int64
	Disabled       int64
	Sellable       int64
	NoChannel      int64
	PriceTotal     int64
	PriceWithPrice int64
	RequestTotal   int64
	Succeeded      int64
	SuccessRate    float64
	RevenueUSD     string
	CostUSD        string
	MarginUSD      string
	MarginRate     float64
}

// Row 是模型商品运维主表行（静态元数据 + 渠道/基准价；请求/毛利等指标在详情页聚合）。
type Row struct {
	ID                        int64
	ModelID                   string
	DisplayName               string
	OwnedBy                   string
	Status                    string
	CreatedAt                 time.Time
	MaxOutputTokens           *int64
	ContextWindowTokens       *int64
	BindingsTotal             int64
	BindingsAvailable         int64
	CapabilitiesDeclaredCount int64
	HasPrice                  bool
	Sellable                  bool
	// 基准售价（DEC-026 model_prices 当前生效行，每 1M tokens）；无基准价时全部为 nil。
	BaseCurrency                *string
	BaseUncachedInputPrice      *string
	BaseCacheReadInputPrice     *string
	BaseCacheWrite5mInputPrice  *string
	BaseCacheWrite1hInputPrice  *string
	BaseCacheWrite30mInputPrice *string
	BaseOutputPrice             *string
	BaseReasoningOutputPrice    *string
}

// Detail 是模型详情页概览（含请求/延迟/毛利等运维指标）。
type Detail struct {
	RequestTotal      int64
	RequestSucceeded  int64
	SuccessRate       float64
	LatencyAvg        float64
	LatencyP50        float64
	LatencyP95        float64
	OutputTokens      int64
	InputTokens       int64
	CacheReadRate     float64
	TPS               float64
	RevenueUSD        string
	MarginUSD         string
	MarginRate        float64
	Sellable          bool
	BindingsTotal     int64
	BindingsAvailable int64
	ModelStatus       string
}

// ChannelRow 是抽屉渠道 Tab 行（最关键）。
type ChannelRow struct {
	ChannelID        int64
	ChannelName      string
	ChannelStatus    string
	BindingStatus    string
	UpstreamModel    string
	Priority         int32
	AttemptTotal     int64
	AttemptSucceeded int64
	SuccessRate      float64
	LatencyP95       float64
	HasPrice         bool
	InputCost        *string
	OutputCost       *string
}

// PerfPoint 是抽屉性能 Tab 时序点。
type PerfPoint struct {
	Bucket           time.Time
	RequestTotal     int64
	RequestSucceeded int64
	LatencyP95       float64
}

// RequestRow 是抽屉请求 Tab 行。
type RequestRow struct {
	RequestID      string
	At             time.Time
	Status         string
	ErrorCode      string
	FinalChannelID *int64
	LatencyMs      *float64
}

// TableParams 主表入参。
type TableParams struct {
	From      time.Time
	To        time.Time
	Status    string
	Search    string
	SortField string
	SortDesc  bool
	Limit     int32
	Offset    int32
}

// Summary 聚合模型总览（8 卡）。
func (s *Service) Summary(ctx context.Context, from, to time.Time) (Summary, error) {
	fromTS, toTS := opsutil.TsNarg(from), opsutil.TsNarg(to)

	counts, err := s.store.ModelsOpsCounts(ctx)
	if err != nil {
		return Summary{}, opsutil.StoreFailed(err, "count models")
	}
	sell, err := s.store.ModelsOpsSellability(ctx)
	if err != nil {
		return Summary{}, opsutil.StoreFailed(err, "aggregate sellability")
	}
	price, err := s.store.ModelsOpsPriceCompleteness(ctx)
	if err != nil {
		return Summary{}, opsutil.StoreFailed(err, "aggregate price completeness")
	}
	reqAgg, err := s.store.ModelsOpsRequestAggregate(ctx, sqlc.ModelsOpsRequestAggregateParams{FromTime: fromTS, ToTime: toTS})
	if err != nil {
		return Summary{}, opsutil.StoreFailed(err, "aggregate request")
	}
	margin, err := s.store.ModelsOpsMarginUSD(ctx, sqlc.ModelsOpsMarginUSDParams{FromTime: fromTS, ToTime: toTS})
	if err != nil {
		return Summary{}, opsutil.StoreFailed(err, "aggregate margin")
	}

	revenue := opsutil.NumericString(margin.RevenueUsd)
	cost := opsutil.NumericString(margin.CostUsd)
	marginAmt := opsutil.SubtractDecimal(revenue, cost)

	return Summary{
		Total:          counts.Total,
		Enabled:        counts.Enabled,
		Disabled:       counts.Disabled,
		Sellable:       sell.Sellable,
		NoChannel:      sell.NoChannel,
		PriceTotal:     price.Total,
		PriceWithPrice: price.WithPrice,
		RequestTotal:   reqAgg.TerminalTotal,
		Succeeded:      reqAgg.SucceededTotal,
		SuccessRate:    opsutil.SuccessRate(reqAgg.SucceededTotal, reqAgg.TerminalTotal),
		RevenueUSD:     revenue,
		CostUSD:        cost,
		MarginUSD:      marginAmt,
		MarginRate:     opsutil.Ratio(marginAmt, revenue),
	}, nil
}

// Table 返回模型商品运维主表（分页）。
func (s *Service) Table(ctx context.Context, p TableParams) ([]Row, int64, error) {
	rows, err := s.store.ModelsOpsTable(ctx, sqlc.ModelsOpsTableParams{
		Status:     opsutil.TextNarg(p.Status),
		Search:     opsutil.TextNarg(p.Search),
		SortField:  opsutil.TextNarg(p.SortField),
		SortDesc:   opsutil.BoolNarg(p.SortDesc),
		PageLimit:  p.Limit,
		PageOffset: p.Offset,
	})
	if err != nil {
		return nil, 0, opsutil.StoreFailed(err, "list model ops table")
	}
	total, err := s.store.ModelsOpsTableCount(ctx, sqlc.ModelsOpsTableCountParams{
		Status: opsutil.TextNarg(p.Status),
		Search: opsutil.TextNarg(p.Search),
	})
	if err != nil {
		return nil, 0, opsutil.StoreFailed(err, "count model ops table")
	}
	out := make([]Row, 0, len(rows))
	for _, r := range rows {
		// base_currency 经 CASE 包裹由 sqlc 推断为 interface{}（可空），命中基准价时为 string。
		var baseCurrency *string
		if v, ok := r.BaseCurrency.(string); ok {
			baseCurrency = &v
		}
		out = append(out, Row{
			ID:                          r.ID,
			ModelID:                     r.ModelID,
			DisplayName:                 r.DisplayName,
			OwnedBy:                     r.OwnedBy,
			Status:                      r.Status,
			CreatedAt:                   r.CreatedAt.Time,
			MaxOutputTokens:             opsutil.Int8Value(r.MaxOutputTokens),
			ContextWindowTokens:         opsutil.Int8Value(r.ContextWindowTokens),
			BindingsTotal:               r.BindingsTotal,
			BindingsAvailable:           r.BindingsAvailable,
			CapabilitiesDeclaredCount:   r.CapabilitiesDeclaredCount,
			HasPrice:                    r.HasPrice,
			Sellable:                    r.Status == "enabled" && r.BindingsAvailable > 0,
			BaseCurrency:                baseCurrency,
			BaseUncachedInputPrice:      opsutil.NumericStringPtr(r.BaseUncachedInputPrice),
			BaseCacheReadInputPrice:     opsutil.NumericStringPtr(r.BaseCacheReadInputPrice),
			BaseCacheWrite5mInputPrice:  opsutil.NumericStringPtr(r.BaseCacheWrite5mInputPrice),
			BaseCacheWrite1hInputPrice:  opsutil.NumericStringPtr(r.BaseCacheWrite1hInputPrice),
			BaseCacheWrite30mInputPrice: opsutil.NumericStringPtr(r.BaseCacheWrite30mInputPrice),
			BaseOutputPrice:             opsutil.NumericStringPtr(r.BaseOutputPrice),
			BaseReasoningOutputPrice:    opsutil.NumericStringPtr(r.BaseReasoningOutputPrice),
		})
	}
	return out, total, nil
}

// Detail 返回单模型抽屉概览。
func (s *Service) Detail(ctx context.Context, modelID int64, from, to time.Time) (Detail, error) {
	r, err := s.store.ModelOpsDetail(ctx, sqlc.ModelOpsDetailParams{ModelID: modelID, FromTime: opsutil.TsNarg(from), ToTime: opsutil.TsNarg(to)})
	if err != nil {
		return Detail{}, opsutil.StoreFailed(err, "model ops detail")
	}
	revenue := opsutil.NumericString(r.RevenueUsd)
	cost := opsutil.NumericString(r.CostUsd)
	marginAmt := opsutil.SubtractDecimal(revenue, cost)

	d := Detail{
		RequestTotal:      r.RequestTotal,
		RequestSucceeded:  r.RequestSucceeded,
		SuccessRate:       opsutil.SuccessRate(r.RequestSucceeded, r.RequestTotal),
		LatencyAvg:        r.LatencyAvg,
		LatencyP50:        r.LatencyP50,
		LatencyP95:        r.LatencyP95,
		OutputTokens:      r.OutputTokens,
		InputTokens:       r.InputTokens,
		RevenueUSD:        revenue,
		MarginUSD:         marginAmt,
		MarginRate:        opsutil.Ratio(marginAmt, revenue),
		Sellable:          r.ModelStatus == "enabled" && r.BindingsAvailable > 0,
		BindingsTotal:     r.BindingsTotal,
		BindingsAvailable: r.BindingsAvailable,
		ModelStatus:       r.ModelStatus,
	}
	if r.InputTokens > 0 {
		d.CacheReadRate = float64(r.CacheReadTokens+r.CacheWriteTokens) / float64(r.InputTokens)
	}
	if r.GenerationSeconds > 0 {
		d.TPS = float64(r.OutputTokens) / r.GenerationSeconds
	}
	return d, nil
}

// Channels 返回单模型承载渠道（绑定）+ attempt 指标。
func (s *Service) Channels(ctx context.Context, modelID int64, from, to time.Time) ([]ChannelRow, error) {
	rows, err := s.store.ModelOpsChannels(ctx, sqlc.ModelOpsChannelsParams{ModelID: modelID, FromTime: opsutil.TsNarg(from), ToTime: opsutil.TsNarg(to)})
	if err != nil {
		return nil, opsutil.StoreFailed(err, "model ops channels")
	}
	out := make([]ChannelRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, ChannelRow{
			ChannelID:        r.ChannelID,
			ChannelName:      r.ChannelName,
			ChannelStatus:    r.ChannelStatus,
			BindingStatus:    r.BindingStatus,
			UpstreamModel:    r.UpstreamModel,
			Priority:         r.Priority,
			AttemptTotal:     r.AttemptTotal,
			AttemptSucceeded: r.AttemptSucceeded,
			SuccessRate:      opsutil.SuccessRate(r.AttemptSucceeded, r.AttemptTotal),
			LatencyP95:       r.LatencyP95,
			HasPrice:         r.HasPrice,
			InputCost:        opsutil.NumericStringPtr(r.InputCost),
			OutputCost:       opsutil.NumericStringPtr(r.OutputCost),
		})
	}
	return out, nil
}

// PerformanceTimeseries 返回单模型性能趋势。
func (s *Service) PerformanceTimeseries(ctx context.Context, modelID int64, interval string, from, to time.Time) ([]PerfPoint, error) {
	if interval != "hour" && interval != "day" {
		return nil, opsutil.InvalidArgument("interval", "interval must be one of hour|day")
	}
	rows, err := s.store.ModelOpsPerformanceTimeseries(ctx, sqlc.ModelOpsPerformanceTimeseriesParams{Unit: interval, ModelID: modelID, FromTime: opsutil.TsNarg(from), ToTime: opsutil.TsNarg(to)})
	if err != nil {
		return nil, opsutil.StoreFailed(err, "model ops performance timeseries")
	}
	out := make([]PerfPoint, 0, len(rows))
	for _, r := range rows {
		out = append(out, PerfPoint{Bucket: r.Bucket.Time, RequestTotal: r.RequestTotal, RequestSucceeded: r.RequestSucceeded, LatencyP95: r.LatencyP95})
	}
	return out, nil
}

// Requests 返回单模型最近请求（分页）。
func (s *Service) Requests(ctx context.Context, modelID int64, from, to time.Time, limit, offset int32) ([]RequestRow, int64, error) {
	rows, err := s.store.ModelOpsRequests(ctx, sqlc.ModelOpsRequestsParams{ModelID: modelID, FromTime: opsutil.TsNarg(from), ToTime: opsutil.TsNarg(to), PageLimit: limit, PageOffset: offset})
	if err != nil {
		return nil, 0, opsutil.StoreFailed(err, "model ops requests")
	}
	total, err := s.store.ModelOpsRequestsCount(ctx, sqlc.ModelOpsRequestsCountParams{ModelID: modelID, FromTime: opsutil.TsNarg(from), ToTime: opsutil.TsNarg(to)})
	if err != nil {
		return nil, 0, opsutil.StoreFailed(err, "model ops requests count")
	}
	out := make([]RequestRow, 0, len(rows))
	for _, r := range rows {
		row := RequestRow{
			RequestID:      r.RequestID,
			At:             r.CreatedAt.Time,
			Status:         r.Status,
			ErrorCode:      opsutil.TextValue(r.ErrorCode),
			FinalChannelID: opsutil.Int8Value(r.FinalChannelID),
		}
		if v, ok := r.LatencyMs.(float64); ok {
			row.LatencyMs = &v
		}
		out = append(out, row)
	}
	return out, total, nil
}
