// Package providerops 提供服务商聚合视图（§3.2）的只读运维聚合。
// 轻聚合：provider 维度由 request_attempts.provider_id 归因。
package providerops

import (
	"context"
	"time"

	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-api/internal/service/admin/opsutil"
)

// Store 是服务商运维聚合所需的只读存储能力（由 *sqlc.Queries 满足）。
type Store interface {
	ProvidersOpsTable(ctx context.Context, arg sqlc.ProvidersOpsTableParams) ([]sqlc.ProvidersOpsTableRow, error)
	ProvidersOpsTableCount(ctx context.Context, arg sqlc.ProvidersOpsTableCountParams) (int64, error)
	ProviderOpsDetail(ctx context.Context, arg sqlc.ProviderOpsDetailParams) (sqlc.ProviderOpsDetailRow, error)
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

// Row 是服务商运维主表行。
type Row struct {
	ID               int64
	Slug             string
	Name             string
	Status           string
	CreatedAt        time.Time
	ChannelTotal     int64
	ChannelEnabled   int64
	AttemptTotal     int64
	AttemptSucceeded int64
	SuccessRate      float64
	TimeoutTotal     int64
	Latency          opsutil.LatencyStats
	HealthBucket     string
	LastSuccessAt    *time.Time
	Tokens           int64
	RevenueUSD       string
	CostUSD          string
	MarginUSD        string
	AvgTPS           float64
}

// Detail 是抽屉概览。
type Detail struct {
	ChannelTotal     int64
	ChannelEnabled   int64
	AttemptTotal     int64
	AttemptSucceeded int64
	SuccessRate      float64
	TimeoutTotal     int64
	Latency          opsutil.LatencyStats
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
	HealthBucket     string
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
	From      time.Time
	To        time.Time
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
		FromTime:   opsutil.TsNarg(p.From),
		ToTime:     opsutil.TsNarg(p.To),
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
		out = append(out, Row{
			ID:               r.ID,
			Slug:             r.Slug,
			Name:             r.Name,
			Status:           r.Status,
			CreatedAt:        r.CreatedAt.Time,
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
			HealthBucket:  opsutil.HealthBucket(r.AttemptSucceeded, r.AttemptTotal),
			LastSuccessAt: opsutil.TimeValue(r.LastSuccessAt),
			Tokens:        r.TokensTotal,
			RevenueUSD:    opsutil.NumericString(r.RevenueUsd),
			CostUSD:       opsutil.NumericString(r.CostUsd),
			MarginUSD: opsutil.SubtractDecimal(
				opsutil.NumericString(r.RevenueUsd),
				opsutil.NumericString(r.CostUsd),
			),
			AvgTPS: r.AvgTps,
		})
	}
	return out, total, nil
}

// Detail 返回单服务商抽屉概览。
func (s *Service) Detail(ctx context.Context, providerID int64, from, to time.Time) (Detail, error) {
	r, err := s.store.ProviderOpsDetail(ctx, sqlc.ProviderOpsDetailParams{ProviderID: providerID, FromTime: opsutil.TsNarg(from), ToTime: opsutil.TsNarg(to)})
	if err != nil {
		return Detail{}, opsutil.StoreFailed(err, "provider ops detail")
	}
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
	}, nil
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
			HealthBucket:     opsutil.HealthBucket(r.AttemptSucceeded, r.AttemptTotal),
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
