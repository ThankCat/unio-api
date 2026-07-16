// Package channelops 提供渠道作战台（§3.3）的只读运维聚合。
// 全部只读、复用既有事实表（request_attempts / request_records / usage_records /
// channel_models / channel_prices / route_channels）。性能/成功率按 attempt 粒度，
// TPS 按最终成功渠道归因（无 per-attempt usage）。健康分桶阈值与概览一致。
package channelops

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/admin/opsutil"
	"github.com/ThankCat/unio-gateway/internal/service/appsettings"
)

// Store 是渠道运维聚合所需的只读存储能力（由 *sqlc.Queries 满足）。
type Store interface {
	ChannelsOpsTable(ctx context.Context, arg sqlc.ChannelsOpsTableParams) ([]sqlc.ChannelsOpsTableRow, error)
	ChannelsOpsTableCount(ctx context.Context, arg sqlc.ChannelsOpsTableCountParams) (int64, error)
	ChannelOpsDetail(ctx context.Context, arg sqlc.ChannelOpsDetailParams) (sqlc.ChannelOpsDetailRow, error)
	ChannelOpsPerformanceTimeseries(ctx context.Context, arg sqlc.ChannelOpsPerformanceTimeseriesParams) ([]sqlc.ChannelOpsPerformanceTimeseriesRow, error)
	ChannelOpsErrors(ctx context.Context, arg sqlc.ChannelOpsErrorsParams) ([]sqlc.ChannelOpsErrorsRow, error)
	ChannelOpsErrorsCount(ctx context.Context, arg sqlc.ChannelOpsErrorsCountParams) (int64, error)
	ChannelOpsModels(ctx context.Context, arg sqlc.ChannelOpsModelsParams) ([]sqlc.ChannelOpsModelsRow, error)
	ChannelOpsRoutes(ctx context.Context, channelID int64) ([]sqlc.ChannelOpsRoutesRow, error)
}

// Service 提供渠道运维只读聚合。
type Service struct {
	store Store
	// settings 供每请求现读健康分桶阈值(admin_backend.channel_health_thresholds);
	// nil(单测)回代码默认。
	settings *appsettings.SettingsStore
}

// NewService 创建渠道运维聚合服务。
func NewService(store Store, settings *appsettings.SettingsStore) *Service {
	return &Service{store: store, settings: settings}
}

// healthThresholds 读取当前生效的分桶阈值。
func (s *Service) healthThresholds(ctx context.Context) appsettings.ChannelHealthThresholds {
	return appsettings.AdminBackendChannelHealthThresholds(ctx, s.settings)
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
	TimeoutMs        *int32
	ProviderName     string
	Credential       string
	AttemptTotal     int64
	AttemptSucceeded int64
	SuccessRate      float64
	TimeoutTotal     int64
	Latency          opsutil.LatencyStats
	HealthBucket     string
	BoundModels      int64
	BoundRoutes      int64
	RecentErrorCode  string
	// 渠道级限流上限（P2-8）：nil=继承全局默认，0=不限，>0=具体上限。
	RpmLimit          *int32
	TpmLimit          *int32
	RpdLimit          *int32
	LastTestedAt      *time.Time
	LastTestOK        *bool
	LastTestLatencyMs *int32
	LastTestError     string
	CredentialValid   bool
	// 当前生效的渠道默认价格倍率（model_id=null）；nil=未配置。
	CostMultiplier *string
	// 当前生效的逐模型价格倍率覆盖条数。
	CostMultiplierOverrides int64
	// 当前生效的充值倍率；nil=未配置（结算按 1.0）。
	RechargeFactor *string
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
	ID         int64
	Name       string
	Mode       string
	PoolKind   string
	Status     string
	PriceRatio string
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

	th := s.healthThresholds(ctx)
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
			TimeoutMs:        int4Value(r.TimeoutMs),
			ProviderName:     r.ProviderName,
			Credential:       r.Credential,
			AttemptTotal:     r.AttemptTotal,
			AttemptSucceeded: r.AttemptSucceeded,
			TimeoutTotal:     r.TimeoutTotal,
			Latency: opsutil.AttemptLatency(
				r.LatencyAvg, r.LatencyP50, r.LatencyP90, r.LatencyP95, r.LatencyP99,
				r.LatencySample, r.AttemptSucceeded,
			),
			HealthBucket:            opsutil.HealthBucket(r.AttemptSucceeded, r.AttemptTotal, th.HealthyRate, th.DegradedRate),
			BoundModels:             r.BoundModels,
			BoundRoutes:             r.BoundRoutes,
			RecentErrorCode:         textValue(r.RecentErrorCode),
			RpmLimit:                int4Value(r.RpmLimit),
			TpmLimit:                int4Value(r.TpmLimit),
			RpdLimit:                int4Value(r.RpdLimit),
			LastTestedAt:            timeValue(r.LastTestedAt),
			LastTestOK:              boolValue(r.LastTestOk),
			LastTestLatencyMs:       int4Value(r.LastTestLatencyMs),
			LastTestError:           textValue(r.LastTestError),
			CredentialValid:         r.CredentialValid,
			CostMultiplier:          opsutil.NumericStringPtr(r.CostMultiplier),
			CostMultiplierOverrides: r.CostMultiplierOverrides,
			RechargeFactor:          opsutil.NumericStringPtr(r.RechargeFactor),
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
		out = append(out, RouteRow{
			ID:         r.ID,
			Name:       r.Name,
			Mode:       r.Mode,
			PoolKind:   r.PoolKind,
			Status:     r.Status,
			PriceRatio: opsutil.NumericString(r.PriceRatio),
		})
	}
	return out, nil
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

func boolValue(v pgtype.Bool) *bool {
	if !v.Valid {
		return nil
	}
	b := v.Bool
	return &b
}

func invalidArgument(field, message string) error {
	return failure.New(failure.CodeAdminInvalidArgument, failure.WithMessage(message), failure.WithField("field", field))
}

func storeFailed(cause error, message string) error {
	return failure.Wrap(failure.CodeAdminStoreFailed, cause, failure.WithMessage(message))
}
