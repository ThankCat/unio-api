// Package providerops 提供服务商聚合视图（§3.2）的只读运维聚合。
// 轻聚合：provider 维度由 request_attempts.provider_id 归因。
package providerops

import (
	"context"
	"encoding/json"
	"time"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/admin/opsutil"
)

// Store 是服务商运维聚合所需的只读存储能力（由 *sqlc.Queries 满足）。
type Store interface {
	ProvidersOpsTable(ctx context.Context, arg sqlc.ProvidersOpsTableParams) ([]sqlc.ProvidersOpsTableRow, error)
	ProvidersOpsTableCount(ctx context.Context, arg sqlc.ProvidersOpsTableCountParams) (int64, error)
	ProviderOpsDetail(ctx context.Context, arg sqlc.ProviderOpsDetailParams) (sqlc.ProviderOpsDetailRow, error)
	ProviderOpsChannelCatalog(ctx context.Context, providerID int64) ([]sqlc.ProviderOpsChannelCatalogRow, error)
	ProviderOpsModelCatalog(ctx context.Context, providerID int64) ([]sqlc.ProviderOpsModelCatalogRow, error)
	ProviderOpsRouteCatalog(ctx context.Context, providerID int64) ([]sqlc.ProviderOpsRouteCatalogRow, error)
	ProviderOpsChannels(ctx context.Context, arg sqlc.ProviderOpsChannelsParams) ([]sqlc.ProviderOpsChannelsRow, error)
	ProviderOpsPerformanceTimeseries(ctx context.Context, arg sqlc.ProviderOpsPerformanceTimeseriesParams) ([]sqlc.ProviderOpsPerformanceTimeseriesRow, error)
	ProviderOpsErrors(ctx context.Context, arg sqlc.ProviderOpsErrorsParams) ([]sqlc.ProviderOpsErrorsRow, error)
	ProviderOpsErrorsCount(ctx context.Context, arg sqlc.ProviderOpsErrorsCountParams) (int64, error)
}

// Service 提供服务商运维只读聚合。
type Service struct {
	store Store
}

// NewService 创建服务商运维聚合服务。
func NewService(store Store) *Service {
	return &Service{store: store}
}

// Row 是服务商运维主表行（静态元数据；指标在详情页聚合）。
type Row struct {
	ID           int64
	Slug         string
	Name         string
	Status       string
	CreatedAt    time.Time
	Endpoints    []EndpointSummary
	ChannelTotal int64
	ModelsCount  int64
	RoutesCount  int64
}

// EndpointSummary 是 Provider 主表内展示的 Endpoint 业务事实，不包含 Redis 运行态。
type EndpointSummary struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	BaseURL string `json:"base_url"`
	Status  string `json:"status"`
}

// Detail 是详情页概览（含 attempt/延迟/Token/利润/TPS 等运维指标）。
type Detail struct {
	ChannelTotal     int64
	ChannelEnabled   int64
	AttemptTotal     int64
	AttemptSucceeded int64
	SuccessRate      float64
	TimeoutTotal     int64
	Latency          opsutil.LatencyStats
	Tokens           int64
	RevenueUSD       string
	CostUSD          string
	MarginUSD        string
	AvgTPS           float64
}

// ChannelCatalogRow 是列表 Tip 渠道行。
type ChannelCatalogRow struct {
	ID     int64
	Name   string
	Status string
}

// ModelCatalogRow 是列表 Tip 模型行。
type ModelCatalogRow struct {
	ModelID     string
	DisplayName string
}

// RouteCatalogRow 是列表 Tip 线路行。
type RouteCatalogRow struct {
	ID     int64
	Name   string
	Status string
	Mode   string
}

// ChannelRow 是抽屉渠道 Tab 行（精简）。
type ChannelRow struct {
	ID               int64
	Name             string
	BaseURL          string
	Status           string
	AttemptTotal     int64
	AttemptSucceeded int64
	SuccessRate      float64
	Latency          opsutil.LatencyStats
}

// PerfPoint 是抽屉性能 Tab 时序点。
type PerfPoint struct {
	Bucket           time.Time
	AttemptTotal     int64
	AttemptSucceeded int64
	LatencyAvg       float64
}

// ErrorRow 是抽屉错误 Tab 行。
type ErrorRow struct {
	At                 time.Time
	ChannelName        string
	UpstreamModel      string
	ErrorCode          string
	UpstreamStatusCode *int32
	RequestID          string
}

// TableParams 主表入参。
type TableParams struct {
	Status    string
	Search    string
	SortField string
	SortDesc  bool
	Limit     int32
	Offset    int32
}

// Table 返回服务商运维主表（分页）。
func (s *Service) Table(ctx context.Context, p TableParams) ([]Row, int64, error) {
	rows, err := s.store.ProvidersOpsTable(ctx, sqlc.ProvidersOpsTableParams{
		Status:     opsutil.TextNarg(p.Status),
		Search:     opsutil.TextNarg(p.Search),
		SortField:  opsutil.TextNarg(p.SortField),
		SortDesc:   opsutil.BoolNarg(p.SortDesc),
		PageLimit:  p.Limit,
		PageOffset: p.Offset,
	})
	if err != nil {
		return nil, 0, opsutil.StoreFailed(err, "list provider ops table")
	}
	total, err := s.store.ProvidersOpsTableCount(ctx, sqlc.ProvidersOpsTableCountParams{
		Status: opsutil.TextNarg(p.Status),
		Search: opsutil.TextNarg(p.Search),
	})
	if err != nil {
		return nil, 0, opsutil.StoreFailed(err, "count provider ops table")
	}
	out := make([]Row, 0, len(rows))
	for _, r := range rows {
		var endpoints []EndpointSummary
		if err := json.Unmarshal([]byte(r.Endpoints), &endpoints); err != nil {
			return nil, 0, opsutil.StoreFailed(err, "decode provider endpoint summaries")
		}
		if endpoints == nil {
			endpoints = []EndpointSummary{}
		}
		out = append(out, Row{
			ID:           r.ID,
			Slug:         r.Slug,
			Name:         r.Name,
			Status:       r.Status,
			CreatedAt:    r.CreatedAt.Time,
			Endpoints:    endpoints,
			ChannelTotal: r.ChannelTotal,
			ModelsCount:  r.ModelsCount,
			RoutesCount:  r.RoutesCount,
		})
	}
	return out, total, nil
}

// Detail 返回单服务商详情概览。
func (s *Service) Detail(ctx context.Context, providerID int64, from, to time.Time) (Detail, error) {
	r, err := s.store.ProviderOpsDetail(ctx, sqlc.ProviderOpsDetailParams{ProviderID: providerID, FromTime: opsutil.TsNarg(from), ToTime: opsutil.TsNarg(to)})
	if err != nil {
		return Detail{}, opsutil.StoreFailed(err, "provider ops detail")
	}
	revenue := opsutil.NumericString(r.RevenueUsd)
	cost := opsutil.NumericString(r.CostUsd)
	return Detail{
		ChannelTotal:     r.ChannelTotal,
		ChannelEnabled:   r.ChannelEnabled,
		AttemptTotal:     r.AttemptTotal,
		AttemptSucceeded: r.AttemptSucceeded,
		SuccessRate:      opsutil.SuccessRate(r.AttemptSucceeded, r.AttemptTotal),
		TimeoutTotal:     r.TimeoutTotal,
		Latency: opsutil.AttemptLatency(
			r.LatencyAvg, r.LatencyP50, r.LatencyP90, r.LatencyP95, r.LatencyP99,
			r.LatencySample, r.AttemptSucceeded,
		),
		Tokens:     r.TokensTotal,
		RevenueUSD: revenue,
		CostUSD:    cost,
		MarginUSD:  opsutil.SubtractDecimal(revenue, cost),
		AvgTPS:     r.AvgTps,
	}, nil
}

// ChannelCatalog 返回服务商渠道清单（列表 Tip）。
func (s *Service) ChannelCatalog(ctx context.Context, providerID int64) ([]ChannelCatalogRow, error) {
	rows, err := s.store.ProviderOpsChannelCatalog(ctx, providerID)
	if err != nil {
		return nil, opsutil.StoreFailed(err, "provider ops channel catalog")
	}
	out := make([]ChannelCatalogRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, ChannelCatalogRow{ID: r.ID, Name: r.Name, Status: r.Status})
	}
	return out, nil
}

// ModelCatalog 返回服务商绑定模型清单（列表 Tip）。
func (s *Service) ModelCatalog(ctx context.Context, providerID int64) ([]ModelCatalogRow, error) {
	rows, err := s.store.ProviderOpsModelCatalog(ctx, providerID)
	if err != nil {
		return nil, opsutil.StoreFailed(err, "provider ops model catalog")
	}
	out := make([]ModelCatalogRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, ModelCatalogRow{ModelID: r.ModelID, DisplayName: r.DisplayName})
	}
	return out, nil
}

// RouteCatalog 返回引用本服务商渠道的线路清单（列表 Tip）。
func (s *Service) RouteCatalog(ctx context.Context, providerID int64) ([]RouteCatalogRow, error) {
	rows, err := s.store.ProviderOpsRouteCatalog(ctx, providerID)
	if err != nil {
		return nil, opsutil.StoreFailed(err, "provider ops route catalog")
	}
	out := make([]RouteCatalogRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, RouteCatalogRow{ID: r.ID, Name: r.Name, Status: r.Status, Mode: r.Mode})
	}
	return out, nil
}

// Channels 返回单服务商下渠道精简子列表。
func (s *Service) Channels(ctx context.Context, providerID int64, from, to time.Time) ([]ChannelRow, error) {
	rows, err := s.store.ProviderOpsChannels(ctx, sqlc.ProviderOpsChannelsParams{ProviderID: providerID, FromTime: opsutil.TsNarg(from), ToTime: opsutil.TsNarg(to)})
	if err != nil {
		return nil, opsutil.StoreFailed(err, "provider ops channels")
	}
	out := make([]ChannelRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, ChannelRow{
			ID:               r.ID,
			Name:             r.Name,
			BaseURL:          r.BaseUrl,
			Status:           r.Status,
			AttemptTotal:     r.AttemptTotal,
			AttemptSucceeded: r.AttemptSucceeded,
			SuccessRate:      opsutil.SuccessRate(r.AttemptSucceeded, r.AttemptTotal),
			Latency: opsutil.AttemptLatency(
				r.LatencyAvg, r.LatencyP50, r.LatencyP90, r.LatencyP95, r.LatencyP99,
				r.LatencySample, r.AttemptSucceeded,
			),
		})
	}
	return out, nil
}

// PerformanceTimeseries 返回单服务商性能趋势。
func (s *Service) PerformanceTimeseries(ctx context.Context, providerID int64, interval string, from, to time.Time) ([]PerfPoint, error) {
	if interval != "hour" && interval != "day" {
		return nil, opsutil.InvalidArgument("interval", "interval must be one of hour|day")
	}
	rows, err := s.store.ProviderOpsPerformanceTimeseries(ctx, sqlc.ProviderOpsPerformanceTimeseriesParams{Unit: interval, ProviderID: providerID, FromTime: opsutil.TsNarg(from), ToTime: opsutil.TsNarg(to)})
	if err != nil {
		return nil, opsutil.StoreFailed(err, "provider ops performance timeseries")
	}
	out := make([]PerfPoint, 0, len(rows))
	for _, r := range rows {
		out = append(out, PerfPoint{Bucket: r.Bucket.Time, AttemptTotal: r.AttemptTotal, AttemptSucceeded: r.AttemptSucceeded, LatencyAvg: r.LatencyAvg})
	}
	return out, nil
}

// Errors 返回单服务商错误明细（分页）。
func (s *Service) Errors(ctx context.Context, providerID int64, from, to time.Time, limit, offset int32) ([]ErrorRow, int64, error) {
	rows, err := s.store.ProviderOpsErrors(ctx, sqlc.ProviderOpsErrorsParams{ProviderID: providerID, FromTime: opsutil.TsNarg(from), ToTime: opsutil.TsNarg(to), PageLimit: limit, PageOffset: offset})
	if err != nil {
		return nil, 0, opsutil.StoreFailed(err, "provider ops errors")
	}
	total, err := s.store.ProviderOpsErrorsCount(ctx, sqlc.ProviderOpsErrorsCountParams{ProviderID: providerID, FromTime: opsutil.TsNarg(from), ToTime: opsutil.TsNarg(to)})
	if err != nil {
		return nil, 0, opsutil.StoreFailed(err, "provider ops errors count")
	}
	out := make([]ErrorRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, ErrorRow{
			At:                 r.CreatedAt.Time,
			ChannelName:        r.ChannelName,
			UpstreamModel:      r.UpstreamModel,
			ErrorCode:          opsutil.TextValue(r.ErrorCode),
			UpstreamStatusCode: opsutil.Int4Value(r.UpstreamStatusCode),
			RequestID:          r.RequestID,
		})
	}
	return out, total, nil
}
