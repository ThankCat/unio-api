// Package modelops 提供模型商品控制台（§3.4）的只读运维聚合。
// 模型口径：request_records.requested_model_id = models.model_id；金额仅 USD。
package modelops

import (
	"context"
	"github.com/jackc/pgx/v5/pgtype"
	"time"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/admin/opsutil"
)

// Store 是模型运维聚合所需的只读存储能力（由 *sqlc.Queries 满足）。
type Store interface {
	ModelsOpsTable(ctx context.Context, arg sqlc.ModelsOpsTableParams) ([]sqlc.ModelsOpsTableRow, error)
	ModelsOpsTableCount(ctx context.Context, arg sqlc.ModelsOpsTableCountParams) (int64, error)
	ModelOpsDetail(ctx context.Context, arg sqlc.ModelOpsDetailParams) (sqlc.ModelOpsDetailRow, error)
	ModelOpsChannels(ctx context.Context, arg sqlc.ModelOpsChannelsParams) ([]sqlc.ModelOpsChannelsRow, error)
	ModelOpsPerformanceTimeseries(ctx context.Context, arg sqlc.ModelOpsPerformanceTimeseriesParams) ([]sqlc.ModelOpsPerformanceTimeseriesRow, error)
	ModelOpsRequests(ctx context.Context, arg sqlc.ModelOpsRequestsParams) ([]sqlc.ModelOpsRequestsRow, error)
	ModelOpsRequestsCount(ctx context.Context, arg sqlc.ModelOpsRequestsCountParams) (int64, error)
	ModelOpsErrors(ctx context.Context, arg sqlc.ModelOpsErrorsParams) ([]sqlc.ModelOpsErrorsRow, error)
}

// Service 提供模型运维只读聚合。
type Service struct {
	store Store
}

// NewService 创建模型运维聚合服务。
func NewService(store Store) *Service {
	return &Service{store: store}
}

// Row 是模型商品运维主表行（静态元数据 + 渠道/基准价；请求/毛利等指标在详情页聚合）。
type Row struct {
	ID          int64
	ModelID     string
	DisplayName string
	OwnedBy     string
	Status      string
	// Family 是模型系列（来自 models.dev），空串表示未归类。
	Family string
	// Description / KnowledgeCutoff 是展示元数据（采纳快照，可编辑），空串表示未填。
	Description     string
	KnowledgeCutoff string
	// DisabledReason 解释模型为何被停用；启用中为 nil。
	DisabledReason            *string
	CreatedAt                 time.Time
	MaxOutputTokens           *int64
	ContextWindowTokens       *int64
	BindingsTotal             int64
	BindingsAvailable         int64
	CapabilitiesDeclaredCount int64
	HasPrice                  bool
	// SupplyAvailable 表示当前是否存在可计费的基础渠道候选；它不表示任何 Route 是否售卖该模型。
	SupplyAvailable bool
	// 基准售价（DEC-026 model_prices 当前生效行，每 1M tokens）；无基准价时全部为 nil。
	BaseCurrency                   *string
	BaseUncachedInputPrice         *string
	BaseCacheReadInputPrice        *string
	BaseCacheCreation5mInputPrice  *string
	BaseCacheCreation1hInputPrice  *string
	BaseCacheCreation30mInputPrice *string
	BaseOutputPrice                *string
	BaseReasoningOutputPrice       *string
	// 售价：绝对售价整组非空时整组覆盖，否则基准价 × 倍率。两套实体可共存，不能混算。
	BaseSaleDiscount                   *string
	BaseSaleUncachedInputPrice         *string
	BaseSaleCacheReadInputPrice        *string
	BaseSaleCacheCreation5mInputPrice  *string
	BaseSaleCacheCreation1hInputPrice  *string
	BaseSaleCacheCreation30mInputPrice *string
	BaseSaleOutputPrice                *string
	BaseSaleReasoningOutputPrice       *string
	// 当前生效基准价的长上下文阶梯（无基准价或未启用时 Enabled=false，其余为 nil）。
	BaseLongContextEnabled                 bool
	BaseLongContextThreshold               *int64
	BaseLongContextInputMultiplier         *string
	BaseLongContextOutputMultiplier        *string
	BaseFastPriceConfigured                bool
	BaseFastUncachedInputPrice             *string
	BaseFastCacheReadInputPrice            *string
	BaseFastCacheCreation5mInputPrice      *string
	BaseFastCacheCreation1hInputPrice      *string
	BaseFastCacheCreation30mInputPrice     *string
	BaseFastOutputPrice                    *string
	BaseFastReasoningOutputPrice           *string
	BaseFastSaleUncachedInputPrice         *string
	BaseFastSaleCacheReadInputPrice        *string
	BaseFastSaleCacheCreation5mInputPrice  *string
	BaseFastSaleCacheCreation1hInputPrice  *string
	BaseFastSaleCacheCreation30mInputPrice *string
	BaseFastSaleOutputPrice                *string
	BaseFastSaleReasoningOutputPrice       *string
}

// Detail 是模型详情页概览（含请求/延迟/毛利等运维指标）。
type Detail struct {
	RequestTotal     int64
	RequestSucceeded int64
	SuccessRate      float64
	LatencyAvg       float64
	LatencyP50       float64
	LatencyP90       float64
	LatencyP95       float64
	LatencyP99       float64
	// GatewayTTFTP95 只统计流式请求；GatewayTTFTSample 为 0 时该值无意义，前端应显示「—」。
	GatewayTTFTP95    float64
	GatewayTTFTSample int64
	OutputTokens      int64
	InputTokens       int64
	CacheReadRate     float64
	TPS               float64
	RevenueUSD        string
	CostUSD           string
	MarginUSD         string
	MarginRate        float64
	// SupplyAvailable 表示当前是否存在可计费的基础渠道候选；它不表示任何 Route 是否售卖该模型。
	SupplyAvailable   bool
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
	// Fast 档成本；两侧都没配 Fast 价时为 nil，表示该渠道对本模型不区分 Fast。
	FastInputCost  *string
	FastOutputCost *string
	// 成本币种与 USD 折算（多货币）：CostCurrency 是原币（绝对路径=provider 币种，倍率路径=USD）；
	// *_USD 为按最新汇率折算的展示口径（与守卫同源），缺汇率时为 nil；FxRate/FxRateDate 供脚注展示。
	CostCurrency      string
	InputCostUSD      *string
	OutputCostUSD     *string
	FastInputCostUSD  *string
	FastOutputCostUSD *string
	CostFxRate        *string
	CostFxRateDate    *string
}

// dateStringPtr 把可空 DATE 转成 YYYY-MM-DD 字符串指针（汇率日脚注展示）。
func dateStringPtr(d pgtype.Date) *string {
	if !d.Valid {
		return nil
	}
	s := d.Time.Format("2006-01-02")
	return &s
}

// PerfPoint 是抽屉性能 Tab 时序点。收入与成本按各自时间戳分桶，
// 求和与 Detail 的 RevenueUSD / 成本一致。
type PerfPoint struct {
	Bucket           time.Time
	RequestTotal     int64
	RequestSucceeded int64
	LatencyP95       float64
	RevenueUSD       string
	CostUSD          string
	MarginUSD        string
}

// ErrorRow 是排障分区的一类错误：同一 error_code 在时间窗内的汇总。
type ErrorRow struct {
	ErrorCode   string
	Occurrences int64
	LastSeenAt  time.Time
	// SampleRequestID 是该错误码最近一次发生的请求，用于直接跳证据中心。
	SampleRequestID string
	// ChannelsTouched 是该错误码涉及的落点渠道数；>1 说明不是单一渠道的问题。
	ChannelsTouched int64
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
			ID:                                     r.ID,
			ModelID:                                r.ModelID,
			DisplayName:                            r.DisplayName,
			OwnedBy:                                r.OwnedBy,
			Family:                                 r.Family,
			Description:                            r.Description,
			KnowledgeCutoff:                        r.KnowledgeCutoff,
			DisabledReason:                         textPtr(r.DisabledReason),
			Status:                                 r.Status,
			CreatedAt:                              r.CreatedAt.Time,
			MaxOutputTokens:                        opsutil.Int8Value(r.MaxOutputTokens),
			ContextWindowTokens:                    opsutil.Int8Value(r.ContextWindowTokens),
			BindingsTotal:                          r.BindingsTotal,
			BindingsAvailable:                      r.BindingsAvailable,
			CapabilitiesDeclaredCount:              r.CapabilitiesDeclaredCount,
			HasPrice:                               r.HasPrice,
			SupplyAvailable:                        r.Status == "enabled" && r.BindingsAvailable > 0,
			BaseCurrency:                           baseCurrency,
			BaseUncachedInputPrice:                 opsutil.NumericStringPtr(r.BaseUncachedInputPrice),
			BaseCacheReadInputPrice:                opsutil.NumericStringPtr(r.BaseCacheReadInputPrice),
			BaseCacheCreation5mInputPrice:          opsutil.NumericStringPtr(r.BaseCacheCreation5mInputPrice),
			BaseCacheCreation1hInputPrice:          opsutil.NumericStringPtr(r.BaseCacheCreation1hInputPrice),
			BaseCacheCreation30mInputPrice:         opsutil.NumericStringPtr(r.BaseCacheCreation30mInputPrice),
			BaseOutputPrice:                        opsutil.NumericStringPtr(r.BaseOutputPrice),
			BaseReasoningOutputPrice:               opsutil.NumericStringPtr(r.BaseReasoningOutputPrice),
			BaseSaleDiscount:                       opsutil.NumericStringPtr(r.BaseSaleDiscount),
			BaseSaleUncachedInputPrice:             opsutil.NumericStringPtr(r.BaseSaleUncachedInputPrice),
			BaseSaleCacheReadInputPrice:            opsutil.NumericStringPtr(r.BaseSaleCacheReadInputPrice),
			BaseSaleCacheCreation5mInputPrice:      opsutil.NumericStringPtr(r.BaseSaleCacheCreation5mInputPrice),
			BaseSaleCacheCreation1hInputPrice:      opsutil.NumericStringPtr(r.BaseSaleCacheCreation1hInputPrice),
			BaseSaleCacheCreation30mInputPrice:     opsutil.NumericStringPtr(r.BaseSaleCacheCreation30mInputPrice),
			BaseSaleOutputPrice:                    opsutil.NumericStringPtr(r.BaseSaleOutputPrice),
			BaseSaleReasoningOutputPrice:           opsutil.NumericStringPtr(r.BaseSaleReasoningOutputPrice),
			BaseLongContextEnabled:                 r.BaseLongContextEnabled,
			BaseLongContextThreshold:               opsutil.Int8Value(r.BaseLongContextThreshold),
			BaseLongContextInputMultiplier:         opsutil.NumericStringPtr(r.BaseLongContextInputMultiplier),
			BaseLongContextOutputMultiplier:        opsutil.NumericStringPtr(r.BaseLongContextOutputMultiplier),
			BaseFastPriceConfigured:                r.BaseFastPriceConfigured,
			BaseFastUncachedInputPrice:             opsutil.NumericStringPtr(r.BaseFastUncachedInputPrice),
			BaseFastCacheReadInputPrice:            opsutil.NumericStringPtr(r.BaseFastCacheReadInputPrice),
			BaseFastCacheCreation5mInputPrice:      opsutil.NumericStringPtr(r.BaseFastCacheCreation5mInputPrice),
			BaseFastCacheCreation1hInputPrice:      opsutil.NumericStringPtr(r.BaseFastCacheCreation1hInputPrice),
			BaseFastCacheCreation30mInputPrice:     opsutil.NumericStringPtr(r.BaseFastCacheCreation30mInputPrice),
			BaseFastOutputPrice:                    opsutil.NumericStringPtr(r.BaseFastOutputPrice),
			BaseFastReasoningOutputPrice:           opsutil.NumericStringPtr(r.BaseFastReasoningOutputPrice),
			BaseFastSaleUncachedInputPrice:         opsutil.NumericStringPtr(r.BaseFastSaleUncachedInputPrice),
			BaseFastSaleCacheReadInputPrice:        opsutil.NumericStringPtr(r.BaseFastSaleCacheReadInputPrice),
			BaseFastSaleCacheCreation5mInputPrice:  opsutil.NumericStringPtr(r.BaseFastSaleCacheCreation5mInputPrice),
			BaseFastSaleCacheCreation1hInputPrice:  opsutil.NumericStringPtr(r.BaseFastSaleCacheCreation1hInputPrice),
			BaseFastSaleCacheCreation30mInputPrice: opsutil.NumericStringPtr(r.BaseFastSaleCacheCreation30mInputPrice),
			BaseFastSaleOutputPrice:                opsutil.NumericStringPtr(r.BaseFastSaleOutputPrice),
			BaseFastSaleReasoningOutputPrice:       opsutil.NumericStringPtr(r.BaseFastSaleReasoningOutputPrice),
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
		LatencyP90:        r.LatencyP90,
		LatencyP95:        r.LatencyP95,
		LatencyP99:        r.LatencyP99,
		GatewayTTFTP95:    r.GatewayTtftP95,
		GatewayTTFTSample: r.GatewayTtftSample,
		OutputTokens:      r.OutputTokens,
		InputTokens:       r.InputTokens,
		RevenueUSD:        revenue,
		CostUSD:           cost,
		MarginUSD:         marginAmt,
		MarginRate:        opsutil.Ratio(marginAmt, revenue),
		SupplyAvailable:   r.ModelStatus == "enabled" && r.BindingsAvailable > 0,
		BindingsTotal:     r.BindingsTotal,
		BindingsAvailable: r.BindingsAvailable,
		ModelStatus:       r.ModelStatus,
	}
	d.CacheReadRate = cacheReadRate(r.CacheReadTokens, r.InputTokens)
	if r.GenerationSeconds > 0 {
		d.TPS = float64(r.OutputTokens) / r.GenerationSeconds
	}
	return d, nil
}

// textPtr 把可空文本转成 *string（NULL → nil）。
func textPtr(value pgtype.Text) *string {
	if !value.Valid {
		return nil
	}
	out := value.String
	return &out
}

func cacheReadRate(cacheReadTokens, inputTokens int64) float64 {
	if inputTokens <= 0 {
		return 0
	}
	return float64(cacheReadTokens) / float64(inputTokens)
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
			ChannelID:         r.ChannelID,
			ChannelName:       r.ChannelName,
			ChannelStatus:     r.ChannelStatus,
			BindingStatus:     r.BindingStatus,
			UpstreamModel:     r.UpstreamModel,
			Priority:          r.Priority,
			AttemptTotal:      r.AttemptTotal,
			AttemptSucceeded:  r.AttemptSucceeded,
			SuccessRate:       opsutil.SuccessRate(r.AttemptSucceeded, r.AttemptTotal),
			LatencyP95:        r.LatencyP95,
			HasPrice:          r.HasPrice,
			InputCost:         opsutil.NumericStringPtr(r.InputCost),
			OutputCost:        opsutil.NumericStringPtr(r.OutputCost),
			FastInputCost:     opsutil.NumericStringPtr(r.FastInputCost),
			FastOutputCost:    opsutil.NumericStringPtr(r.FastOutputCost),
			CostCurrency:      r.CostCurrency,
			InputCostUSD:      opsutil.NumericStringPtr(r.InputCostUsd),
			OutputCostUSD:     opsutil.NumericStringPtr(r.OutputCostUsd),
			FastInputCostUSD:  opsutil.NumericStringPtr(r.FastInputCostUsd),
			FastOutputCostUSD: opsutil.NumericStringPtr(r.FastOutputCostUsd),
			CostFxRate:        opsutil.NumericStringPtr(r.CostFxRate),
			CostFxRateDate:    dateStringPtr(r.CostFxRateDate),
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
		revenue := opsutil.NumericString(r.RevenueUsd)
		cost := opsutil.NumericString(r.CostUsd)
		out = append(out, PerfPoint{
			Bucket:           r.Bucket.Time,
			RequestTotal:     r.RequestTotal,
			RequestSucceeded: r.RequestSucceeded,
			LatencyP95:       r.LatencyP95,
			RevenueUSD:       revenue,
			CostUSD:          cost,
			MarginUSD:        opsutil.SubtractDecimal(revenue, cost),
		})
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

// Errors 返回单模型失败请求按错误码的聚合，最高频在前。
// 错误码种类有限，一次返回全部；占比由前端按总数换算，避免分页后占比失真。
func (s *Service) Errors(ctx context.Context, modelID int64, from, to time.Time) ([]ErrorRow, error) {
	rows, err := s.store.ModelOpsErrors(ctx, sqlc.ModelOpsErrorsParams{
		ModelID:  modelID,
		FromTime: opsutil.TsNarg(from),
		ToTime:   opsutil.TsNarg(to),
	})
	if err != nil {
		return nil, opsutil.StoreFailed(err, "model ops errors")
	}
	out := make([]ErrorRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, ErrorRow{
			ErrorCode:       r.ErrorCode,
			Occurrences:     r.Occurrences,
			LastSeenAt:      r.LastSeenAt.Time,
			SampleRequestID: r.SampleRequestID,
			ChannelsTouched: r.ChannelsTouched,
		})
	}
	return out, nil
}
