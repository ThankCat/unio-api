// Package channelops 提供渠道作战台（§3.3）的只读运维聚合。
// 全部只读、复用既有事实表（request_attempts / request_records / usage_records /
// channel_models / channel_prices / route_channels）。性能/成功率按 attempt 粒度，
// TPS 按最终成功渠道归因（无 per-attempt usage）。健康分桶阈值与概览一致。
package channelops

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-api/internal/service/admin/opsutil"
)

const (
	healthyThreshold  = 0.95
	degradedThreshold = 0.80
)

// Store 是渠道运维聚合所需的只读存储能力（由 *sqlc.Queries 满足）。
type Store interface {
	ChannelsOpsCounts(ctx context.Context) (sqlc.ChannelsOpsCountsRow, error)
	ChannelsOpsAttemptAggregate(ctx context.Context, arg sqlc.ChannelsOpsAttemptAggregateParams) (sqlc.ChannelsOpsAttemptAggregateRow, error)
	ChannelsOpsThroughput(ctx context.Context, arg sqlc.ChannelsOpsThroughputParams) (sqlc.ChannelsOpsThroughputRow, error)
	ChannelsOpsHealthDistribution(ctx context.Context, arg sqlc.ChannelsOpsHealthDistributionParams) (sqlc.ChannelsOpsHealthDistributionRow, error)
	ChannelsOpsRecentError(ctx context.Context, arg sqlc.ChannelsOpsRecentErrorParams) ([]sqlc.ChannelsOpsRecentErrorRow, error)
	ChannelsOpsPriceCoverage(ctx context.Context) (sqlc.ChannelsOpsPriceCoverageRow, error)
	ChannelsOpsTable(ctx context.Context, arg sqlc.ChannelsOpsTableParams) ([]sqlc.ChannelsOpsTableRow, error)
	ChannelsOpsTableCount(ctx context.Context, arg sqlc.ChannelsOpsTableCountParams) (int64, error)
	ChannelOpsDetail(ctx context.Context, arg sqlc.ChannelOpsDetailParams) (sqlc.ChannelOpsDetailRow, error)
	ChannelOpsPerformanceTimeseries(ctx context.Context, arg sqlc.ChannelOpsPerformanceTimeseriesParams) ([]sqlc.ChannelOpsPerformanceTimeseriesRow, error)
	ChannelOpsSuccessBuckets(ctx context.Context, arg sqlc.ChannelOpsSuccessBucketsParams) ([]sqlc.ChannelOpsSuccessBucketsRow, error)
	ChannelOpsErrors(ctx context.Context, arg sqlc.ChannelOpsErrorsParams) ([]sqlc.ChannelOpsErrorsRow, error)
	ChannelOpsErrorsCount(ctx context.Context, arg sqlc.ChannelOpsErrorsCountParams) (int64, error)
	ChannelOpsModels(ctx context.Context, arg sqlc.ChannelOpsModelsParams) ([]sqlc.ChannelOpsModelsRow, error)
	ChannelOpsRoutes(ctx context.Context, channelID int64) ([]sqlc.ChannelOpsRoutesRow, error)
}

// Service 提供渠道运维只读聚合。
type Service struct {
	store Store
}

// NewService 创建渠道运维聚合服务。
func NewService(store Store) *Service {
	return &Service{store: store}
}

// HealthCounts 是健康四档计数。
type HealthCounts struct {
	Healthy   int64
	Degraded  int64
	Unhealthy int64
	NoData    int64
}

// RecentError 是最近一条渠道错误摘要。
type RecentError struct {
	Code        string
	ChannelName string
	At          *time.Time
}

// Summary 是渠道总览雷达（§1.8 11 卡）所需聚合。
type Summary struct {
	Total            int64
	Enabled          int64
	Disabled         int64
	Health           HealthCounts
	AttemptTotal     int64
	AttemptSucceeded int64
	SuccessRate      float64
	TimeoutTotal     int64
	Latency          opsutil.LatencyStats
	TPS              float64
	RecentError      RecentError
	PriceTotal       int64
	PriceWithPrice   int64
	PriceWithCost    int64
}

// Row 是渠道运维主表行。
type Row struct {
	ID               int64
	Name             string
	Status           string
	CreatedAt        time.Time
	Protocol         string
	AdapterKey       string
	BaseURL          string
	Priority         int32
	ProviderName     string
	AttemptTotal     int64
	AttemptSucceeded int64
	SuccessRate      float64
	TimeoutTotal     int64
	Latency          opsutil.LatencyStats
	HealthBucket     string
	LastSuccessAt    *time.Time
	BoundModels      int64
	RecentErrorCode  string
}

// Detail 是抽屉概览 attempt 指标。
type Detail struct {
	AttemptTotal     int64
	AttemptSucceeded int64
	SuccessRate      float64
	TimeoutTotal     int64
	Latency          opsutil.LatencyStats
	LastSuccessAt    *time.Time
	LastFailureAt    *time.Time
}

// PerfPoint 是抽屉性能 Tab 时序点。
type PerfPoint struct {
	Bucket           time.Time
	AttemptTotal     int64
	AttemptSucceeded int64
	LatencyAvg       float64
}

// SuccessBucket 是最近 10 分钟 attempt 成功率桶（与概览渠道表现一致）。
type SuccessBucket struct {
	Bucket      time.Time
	Terminal    int64
	Succeeded   int64
	SuccessRate float64
}

// ErrorRow 是抽屉错误 Tab 行。
type ErrorRow struct {
	At                 time.Time
	UpstreamModel      string
	ErrorCode          string
	UpstreamStatusCode *int32
	ErrorMessage       string
	RequestID          string
}

// ModelRow 是抽屉模型 Tab 行（完整列）。
type ModelRow struct {
	ModelID          int64
	ModelRef         string
	DisplayName      string
	UpstreamModel    string
	Status           string
	AttemptTotal     int64
	AttemptSucceeded int64
	SuccessRate      float64
	Latency          opsutil.LatencyStats
	HasPrice         bool
}

// RouteRow 是抽屉线路 Tab 行。
type RouteRow struct {
	ID       int64
	Name     string
	Mode     string
	PoolKind string
	Status   string
}

// TableParams 是主表查询入参。
type TableParams struct {
	From       time.Time
	To         time.Time
	Status     string
	ProviderID *int64
	Search     string
	SortField  string
	SortDesc   bool
	Limit      int32
	Offset     int32
}

// Summary 聚合渠道总览雷达。
func (s *Service) Summary(ctx context.Context, from, to time.Time) (Summary, error) {
	fromTS, toTS := tsNarg(from), tsNarg(to)

	counts, err := s.store.ChannelsOpsCounts(ctx)
	if err != nil {
		return Summary{}, storeFailed(err, "count channels")
	}
	agg, err := s.store.ChannelsOpsAttemptAggregate(ctx, sqlc.ChannelsOpsAttemptAggregateParams{FromTime: fromTS, ToTime: toTS})
	if err != nil {
		return Summary{}, storeFailed(err, "aggregate channel attempts")
	}
	tp, err := s.store.ChannelsOpsThroughput(ctx, sqlc.ChannelsOpsThroughputParams{FromTime: fromTS, ToTime: toTS})
	if err != nil {
		return Summary{}, storeFailed(err, "aggregate channel throughput")
	}
	health, err := s.store.ChannelsOpsHealthDistribution(ctx, sqlc.ChannelsOpsHealthDistributionParams{FromTime: fromTS, ToTime: toTS})
	if err != nil {
		return Summary{}, storeFailed(err, "aggregate channel health")
	}
	price, err := s.store.ChannelsOpsPriceCoverage(ctx)
	if err != nil {
		return Summary{}, storeFailed(err, "aggregate price coverage")
	}
	recentRows, err := s.store.ChannelsOpsRecentError(ctx, sqlc.ChannelsOpsRecentErrorParams{FromTime: fromTS, ToTime: toTS})
	if err != nil {
		return Summary{}, storeFailed(err, "fetch recent error")
	}

	out := Summary{
		Total:            counts.Total,
		Enabled:          counts.Enabled,
		Disabled:         counts.Disabled,
		Health:           HealthCounts{Healthy: health.Healthy, Degraded: health.Degraded, Unhealthy: health.Unhealthy, NoData: health.NoData},
		AttemptTotal:     agg.AttemptTotal,
		AttemptSucceeded: agg.AttemptSucceeded,
		TimeoutTotal:     agg.TimeoutTotal,
		Latency: opsutil.AttemptLatency(
			agg.LatencyAvg, agg.LatencyP50, agg.LatencyP90, agg.LatencyP95, agg.LatencyP99,
			agg.LatencySample, agg.AttemptSucceeded,
		),
		PriceTotal:     price.Total,
		PriceWithPrice: price.WithPrice,
		PriceWithCost:  price.WithCost,
	}
	if agg.AttemptTotal > 0 {
		out.SuccessRate = float64(agg.AttemptSucceeded) / float64(agg.AttemptTotal)
	}
	if tp.GenerationSeconds > 0 {
		out.TPS = float64(tp.OutputTokens) / tp.GenerationSeconds
	}
	if len(recentRows) > 0 {
		out.RecentError = RecentError{
			Code:        textValue(recentRows[0].ErrorCode),
			ChannelName: recentRows[0].ChannelName,
			At:          timeValue(recentRows[0].CreatedAt),
		}
	}
	return out, nil
}

// Table 返回渠道运维主表（分页）。
func (s *Service) Table(ctx context.Context, p TableParams) ([]Row, int64, error) {
	rows, err := s.store.ChannelsOpsTable(ctx, sqlc.ChannelsOpsTableParams{
		FromTime:   tsNarg(p.From),
		ToTime:     tsNarg(p.To),
		Status:     textNarg(p.Status),
		ProviderID: int8Narg(p.ProviderID),
		Search:     textNarg(p.Search),
		SortField:  textNarg(p.SortField),
		SortDesc:   boolNarg(p.SortDesc),
		PageLimit:  p.Limit,
		PageOffset: p.Offset,
	})
	if err != nil {
		return nil, 0, storeFailed(err, "list channel ops table")
	}
	total, err := s.store.ChannelsOpsTableCount(ctx, sqlc.ChannelsOpsTableCountParams{
		Status:     textNarg(p.Status),
		ProviderID: int8Narg(p.ProviderID),
		Search:     textNarg(p.Search),
	})
	if err != nil {
		return nil, 0, storeFailed(err, "count channel ops table")
	}

	out := make([]Row, 0, len(rows))
	for _, r := range rows {
		row := Row{
			ID:               r.ID,
			Name:             r.Name,
			Status:           r.Status,
			CreatedAt:        r.CreatedAt.Time,
			Protocol:         r.Protocol,
			AdapterKey:       r.AdapterKey,
			BaseURL:          r.BaseUrl,
			Priority:         r.Priority,
			ProviderName:     r.ProviderName,
			AttemptTotal:     r.AttemptTotal,
			AttemptSucceeded: r.AttemptSucceeded,
			TimeoutTotal:     r.TimeoutTotal,
			Latency: opsutil.AttemptLatency(
				r.LatencyAvg, r.LatencyP50, r.LatencyP90, r.LatencyP95, r.LatencyP99,
				r.LatencySample, r.AttemptSucceeded,
			),
			HealthBucket:    healthBucket(r.AttemptSucceeded, r.AttemptTotal),
			LastSuccessAt:   timeValue(r.LastSuccessAt),
			BoundModels:     r.BoundModels,
			RecentErrorCode: textValue(r.RecentErrorCode),
		}
		if r.AttemptTotal > 0 {
			row.SuccessRate = float64(r.AttemptSucceeded) / float64(r.AttemptTotal)
		}
		out = append(out, row)
	}
	return out, total, nil
}

// Detail 返回单渠道抽屉概览 attempt 指标。
func (s *Service) Detail(ctx context.Context, channelID int64, from, to time.Time) (Detail, error) {
	r, err := s.store.ChannelOpsDetail(ctx, sqlc.ChannelOpsDetailParams{ChannelID: channelID, FromTime: tsNarg(from), ToTime: tsNarg(to)})
	if err != nil {
		return Detail{}, storeFailed(err, "channel ops detail")
	}
	d := Detail{
		AttemptTotal:     r.AttemptTotal,
		AttemptSucceeded: r.AttemptSucceeded,
		TimeoutTotal:     r.TimeoutTotal,
		Latency: opsutil.AttemptLatency(
			r.LatencyAvg, r.LatencyP50, r.LatencyP90, r.LatencyP95, r.LatencyP99,
			r.LatencySample, r.AttemptSucceeded,
		),
		LastSuccessAt: timeValue(r.LastSuccessAt),
		LastFailureAt: timeValue(r.LastFailureAt),
	}
	if r.AttemptTotal > 0 {
		d.SuccessRate = float64(r.AttemptSucceeded) / float64(r.AttemptTotal)
	}
	return d, nil
}

// SuccessBuckets 返回单渠道最近 10 分钟 attempt 成功率桶。
func (s *Service) SuccessBuckets(ctx context.Context, channelID int64, from, to time.Time) ([]SuccessBucket, error) {
	rows, err := s.store.ChannelOpsSuccessBuckets(ctx, sqlc.ChannelOpsSuccessBucketsParams{
		ChannelID: channelID, FromTime: tsNarg(from), ToTime: tsNarg(to),
	})
	if err != nil {
		return nil, storeFailed(err, "channel ops success buckets")
	}
	out := make([]SuccessBucket, 0, len(rows))
	for _, r := range rows {
		out = append(out, SuccessBucket{
			Bucket:      r.Bucket.Time,
			Terminal:    r.TerminalTotal,
			Succeeded:   r.SucceededTotal,
			SuccessRate: r.SuccessRate,
		})
	}
	return out, nil
}

// PerformanceTimeseries 返回单渠道性能时序。
func (s *Service) PerformanceTimeseries(ctx context.Context, channelID int64, interval string, from, to time.Time) ([]PerfPoint, error) {
	if interval != "hour" && interval != "day" {
		return nil, invalidArgument("interval", "interval must be one of hour|day")
	}
	rows, err := s.store.ChannelOpsPerformanceTimeseries(ctx, sqlc.ChannelOpsPerformanceTimeseriesParams{
		Unit: interval, ChannelID: channelID, FromTime: tsNarg(from), ToTime: tsNarg(to),
	})
	if err != nil {
		return nil, storeFailed(err, "channel ops performance timeseries")
	}
	out := make([]PerfPoint, 0, len(rows))
	for _, r := range rows {
		out = append(out, PerfPoint{Bucket: r.Bucket.Time, AttemptTotal: r.AttemptTotal, AttemptSucceeded: r.AttemptSucceeded, LatencyAvg: r.LatencyAvg})
	}
	return out, nil
}

// Errors 返回单渠道错误明细（分页）。
func (s *Service) Errors(ctx context.Context, channelID int64, from, to time.Time, limit, offset int32) ([]ErrorRow, int64, error) {
	rows, err := s.store.ChannelOpsErrors(ctx, sqlc.ChannelOpsErrorsParams{
		ChannelID: channelID, FromTime: tsNarg(from), ToTime: tsNarg(to), PageLimit: limit, PageOffset: offset,
	})
	if err != nil {
		return nil, 0, storeFailed(err, "channel ops errors")
	}
	total, err := s.store.ChannelOpsErrorsCount(ctx, sqlc.ChannelOpsErrorsCountParams{ChannelID: channelID, FromTime: tsNarg(from), ToTime: tsNarg(to)})
	if err != nil {
		return nil, 0, storeFailed(err, "channel ops errors count")
	}
	out := make([]ErrorRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, ErrorRow{
			At:                 r.CreatedAt.Time,
			UpstreamModel:      r.UpstreamModel,
			ErrorCode:          textValue(r.ErrorCode),
			UpstreamStatusCode: int4Value(r.UpstreamStatusCode),
			ErrorMessage:       textValue(r.ErrorMessage),
			RequestID:          r.RequestID,
		})
	}
	return out, total, nil
}

// Models 返回单渠道绑定模型 attempt 指标。
func (s *Service) Models(ctx context.Context, channelID int64, from, to time.Time) ([]ModelRow, error) {
	rows, err := s.store.ChannelOpsModels(ctx, sqlc.ChannelOpsModelsParams{ChannelID: channelID, FromTime: tsNarg(from), ToTime: tsNarg(to)})
	if err != nil {
		return nil, storeFailed(err, "channel ops models")
	}
	out := make([]ModelRow, 0, len(rows))
	for _, r := range rows {
		row := ModelRow{
			ModelID:          r.ModelID,
			ModelRef:         r.ModelRef,
			DisplayName:      r.DisplayName,
			UpstreamModel:    r.UpstreamModel,
			Status:           r.Status,
			AttemptTotal:     r.AttemptTotal,
			AttemptSucceeded: r.AttemptSucceeded,
			Latency: opsutil.AttemptLatency(
				r.LatencyAvg, r.LatencyP50, r.LatencyP90, r.LatencyP95, r.LatencyP99,
				r.LatencySample, r.AttemptSucceeded,
			),
			HasPrice: r.HasPrice,
		}
		if r.AttemptTotal > 0 {
			row.SuccessRate = float64(r.AttemptSucceeded) / float64(r.AttemptTotal)
		}
		out = append(out, row)
	}
	return out, nil
}

// Routes 返回引用该渠道的显式线路池。
func (s *Service) Routes(ctx context.Context, channelID int64) ([]RouteRow, error) {
	rows, err := s.store.ChannelOpsRoutes(ctx, channelID)
	if err != nil {
		return nil, storeFailed(err, "channel ops routes")
	}
	out := make([]RouteRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, RouteRow{ID: r.ID, Name: r.Name, Mode: r.Mode, PoolKind: r.PoolKind, Status: r.Status})
	}
	return out, nil
}

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

func tsNarg(t time.Time) pgtype.Timestamptz {
	if t.IsZero() {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: t, Valid: true}
}

func textNarg(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}

func boolNarg(v bool) pgtype.Bool {
	return pgtype.Bool{Bool: v, Valid: true}
}

func int8Narg(p *int64) pgtype.Int8 {
	if p == nil {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: *p, Valid: true}
}

func textValue(t pgtype.Text) string {
	if !t.Valid {
		return ""
	}
	return t.String
}

func timeValue(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	tt := t.Time
	return &tt
}

func int4Value(v pgtype.Int4) *int32 {
	if !v.Valid {
		return nil
	}
	n := v.Int32
	return &n
}

func invalidArgument(field, message string) error {
	return failure.New(failure.CodeAdminInvalidArgument, failure.WithMessage(message), failure.WithField("field", field))
}

func storeFailed(cause error, message string) error {
	return failure.Wrap(failure.CodeAdminStoreFailed, cause, failure.WithMessage(message))
}
