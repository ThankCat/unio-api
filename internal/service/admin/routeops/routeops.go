// Package routeops 提供线路路由作战台（§3.5）的只读运维聚合。
// 归因：每条请求按其 API Key 绑定的 api_keys.route_id 计入线路（线路必填，无默认回落）。
package routeops

import (
	"context"
	"time"

	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-api/internal/service/admin/opsutil"
)

// Store 是线路运维聚合所需的只读存储能力（由 *sqlc.Queries 满足）。
type Store interface {
	RoutesOpsCounts(ctx context.Context) (sqlc.RoutesOpsCountsRow, error)
	RoutesOpsAttributeAggregate(ctx context.Context, arg sqlc.RoutesOpsAttributeAggregateParams) (sqlc.RoutesOpsAttributeAggregateRow, error)
	RoutesOpsTable(ctx context.Context, arg sqlc.RoutesOpsTableParams) ([]sqlc.RoutesOpsTableRow, error)
	RoutesOpsTableCount(ctx context.Context, arg sqlc.RoutesOpsTableCountParams) (int64, error)
	RouteOpsDetail(ctx context.Context, arg sqlc.RouteOpsDetailParams) (sqlc.RouteOpsDetailRow, error)
	RouteOpsChannelPool(ctx context.Context, routeID int64) ([]sqlc.RouteOpsChannelPoolRow, error)
	RouteOpsBoundUsers(ctx context.Context, routeID int64) ([]sqlc.RouteOpsBoundUsersRow, error)
	RouteOpsBoundKeys(ctx context.Context, routeID int64) ([]sqlc.RouteOpsBoundKeysRow, error)
	RouteOpsPerformanceTimeseries(ctx context.Context, arg sqlc.RouteOpsPerformanceTimeseriesParams) ([]sqlc.RouteOpsPerformanceTimeseriesRow, error)
	RouteOpsModels(ctx context.Context, arg sqlc.RouteOpsModelsParams) ([]sqlc.RouteOpsModelsRow, error)
	RouteOpsRequests(ctx context.Context, arg sqlc.RouteOpsRequestsParams) ([]sqlc.RouteOpsRequestsRow, error)
	RouteOpsRequestsCount(ctx context.Context, arg sqlc.RouteOpsRequestsCountParams) (int64, error)
}

// Service 提供线路运维只读聚合。
type Service struct {
	store Store
}

// NewService 创建线路运维聚合服务。
func NewService(store Store) *Service {
	return &Service{store: store}
}

// Summary 是线路总览。
type Summary struct {
	Total         int64
	Enabled       int64
	Disabled      int64
	RequestTotal  int64
	Succeeded     int64
	SuccessRate   float64
	FallbackTotal int64
	FallbackRate  float64
	NoChannel     int64
	LatencyP95    float64
}

// Row 是线路运维主表行。
type Row struct {
	ID          int64
	Name        string
	Mode        string
	PoolKind    string
	Status      string
	Description string
	// PriceRatio 客户售价倍率（DEC-026：客户售价 = 模型基准价 × 倍率），十进制字符串。
	PriceRatio       string
	RequestTotal     int64
	RequestSucceeded int64
	SuccessRate      float64
	FallbackTotal    int64
	FallbackRate     float64
	NoChannelTotal   int64
	LatencyP95       float64
	BoundUsers       int64
	BoundKeys        int64
	PoolChannels     int64
	Serviceable      bool
	Abnormal         bool
}

// Detail 是抽屉概览。
type Detail struct {
	RequestTotal     int64
	RequestSucceeded int64
	SuccessRate      float64
	FallbackTotal    int64
	FallbackRate     float64
	NoChannelTotal   int64
	LatencyP50       float64
	LatencyP95       float64
}

// ChannelPoolRow 是渠道池 Tab 行。
type ChannelPoolRow struct {
	ChannelID     int64
	ChannelName   string
	ChannelStatus string
	Priority      int32
	ProviderName  string
}

// BoundUser / BoundKey 是绑定 Tab 行。
type BoundUser struct {
	ID          int64
	Email       string
	DisplayName string
}

type BoundKey struct {
	ID     int64
	Name   string
	UserID int64
	Status string
}

// PerfPoint 是性能 Tab 时序点。
type PerfPoint struct {
	Bucket           time.Time
	RequestTotal     int64
	RequestSucceeded int64
	LatencyP95       float64
}

// ModelRow 是模型 Tab 行（精简）。
type ModelRow struct {
	ModelID          string
	RequestTotal     int64
	RequestSucceeded int64
	SuccessRate      float64
}

// RequestRow 是请求 Tab 行。
type RequestRow struct {
	RequestID      string
	At             time.Time
	Status         string
	ModelID        string
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

// Summary 聚合线路总览。
func (s *Service) Summary(ctx context.Context, from, to time.Time) (Summary, error) {
	counts, err := s.store.RoutesOpsCounts(ctx)
	if err != nil {
		return Summary{}, opsutil.StoreFailed(err, "count routes")
	}
	agg, err := s.store.RoutesOpsAttributeAggregate(ctx, sqlc.RoutesOpsAttributeAggregateParams{FromTime: opsutil.TsNarg(from), ToTime: opsutil.TsNarg(to)})
	if err != nil {
		return Summary{}, opsutil.StoreFailed(err, "aggregate route attribution")
	}
	out := Summary{
		Total:         counts.Total,
		Enabled:       counts.Enabled,
		Disabled:      counts.Disabled,
		RequestTotal:  agg.TerminalTotal,
		Succeeded:     agg.SucceededTotal,
		SuccessRate:   opsutil.SuccessRate(agg.SucceededTotal, agg.TerminalTotal),
		FallbackTotal: agg.FallbackTotal,
		NoChannel:     agg.NoChannelTotal,
		LatencyP95:    agg.LatencyP95,
	}
	if agg.SucceededTotal > 0 {
		out.FallbackRate = float64(agg.FallbackTotal) / float64(agg.SucceededTotal)
	}
	return out, nil
}

// Table 返回线路运维主表（分页）。
func (s *Service) Table(ctx context.Context, p TableParams) ([]Row, int64, error) {
	rows, err := s.store.RoutesOpsTable(ctx, sqlc.RoutesOpsTableParams{
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
		return nil, 0, opsutil.StoreFailed(err, "list route ops table")
	}
	total, err := s.store.RoutesOpsTableCount(ctx, sqlc.RoutesOpsTableCountParams{
		Status: opsutil.TextNarg(p.Status),
		Search: opsutil.TextNarg(p.Search),
	})
	if err != nil {
		return nil, 0, opsutil.StoreFailed(err, "count route ops table")
	}
	out := make([]Row, 0, len(rows))
	for _, r := range rows {
		rate := opsutil.SuccessRate(r.RequestSucceeded, r.RequestTotal)
		abnormal := r.NoChannelTotal > 0 || (r.RequestTotal >= 20 && rate < 0.9)
		row := Row{
			ID:               r.ID,
			Name:             r.Name,
			Mode:             r.Mode,
			PoolKind:         r.PoolKind,
			Status:           r.Status,
			Description:      opsutil.TextValue(r.Description),
			PriceRatio:       opsutil.NumericString(r.PriceRatio),
			RequestTotal:     r.RequestTotal,
			RequestSucceeded: r.RequestSucceeded,
			SuccessRate:      rate,
			FallbackTotal:    r.FallbackTotal,
			NoChannelTotal:   r.NoChannelTotal,
			LatencyP95:       r.LatencyP95,
			BoundUsers:       r.BoundUsers,
			BoundKeys:        r.BoundKeys,
			PoolChannels:     r.PoolChannels,
			Serviceable:      r.Status == "enabled" && !abnormal,
			Abnormal:         abnormal,
		}
		if r.RequestSucceeded > 0 {
			row.FallbackRate = float64(r.FallbackTotal) / float64(r.RequestSucceeded)
		}
		out = append(out, row)
	}
	return out, total, nil
}

// Detail 返回单线路抽屉概览。
func (s *Service) Detail(ctx context.Context, routeID int64, from, to time.Time) (Detail, error) {
	r, err := s.store.RouteOpsDetail(ctx, sqlc.RouteOpsDetailParams{RouteID: routeID, FromTime: opsutil.TsNarg(from), ToTime: opsutil.TsNarg(to)})
	if err != nil {
		return Detail{}, opsutil.StoreFailed(err, "route ops detail")
	}
	d := Detail{
		RequestTotal:     r.RequestTotal,
		RequestSucceeded: r.RequestSucceeded,
		SuccessRate:      opsutil.SuccessRate(r.RequestSucceeded, r.RequestTotal),
		FallbackTotal:    r.FallbackTotal,
		NoChannelTotal:   r.NoChannelTotal,
		LatencyP50:       r.LatencyP50,
		LatencyP95:       r.LatencyP95,
	}
	if r.RequestSucceeded > 0 {
		d.FallbackRate = float64(r.FallbackTotal) / float64(r.RequestSucceeded)
	}
	return d, nil
}

// ChannelPool 返回线路显式渠道池成员。
func (s *Service) ChannelPool(ctx context.Context, routeID int64) ([]ChannelPoolRow, error) {
	rows, err := s.store.RouteOpsChannelPool(ctx, routeID)
	if err != nil {
		return nil, opsutil.StoreFailed(err, "route ops channel pool")
	}
	out := make([]ChannelPoolRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, ChannelPoolRow{ChannelID: r.ChannelID, ChannelName: r.ChannelName, ChannelStatus: r.ChannelStatus, Priority: r.Priority, ProviderName: r.ProviderName})
	}
	return out, nil
}

// Bindings 返回绑定本线路的用户与 API Key（绑定 Tab，P0）。
func (s *Service) Bindings(ctx context.Context, routeID int64) ([]BoundUser, []BoundKey, error) {
	users, err := s.store.RouteOpsBoundUsers(ctx, routeID)
	if err != nil {
		return nil, nil, opsutil.StoreFailed(err, "route ops bound users")
	}
	keys, err := s.store.RouteOpsBoundKeys(ctx, routeID)
	if err != nil {
		return nil, nil, opsutil.StoreFailed(err, "route ops bound keys")
	}
	us := make([]BoundUser, 0, len(users))
	for _, u := range users {
		us = append(us, BoundUser{ID: u.ID, Email: u.Email, DisplayName: u.DisplayName})
	}
	ks := make([]BoundKey, 0, len(keys))
	now := time.Now()
	for _, k := range keys {
		ks = append(ks, BoundKey{ID: k.ID, Name: k.Name, UserID: k.UserID, Status: apiKeyStatus(k, now)})
	}
	return us, ks, nil
}

// PerformanceTimeseries 返回单线路性能趋势。
func (s *Service) PerformanceTimeseries(ctx context.Context, routeID int64, interval string, from, to time.Time) ([]PerfPoint, error) {
	if interval != "hour" && interval != "day" {
		return nil, opsutil.InvalidArgument("interval", "interval must be one of hour|day")
	}
	rows, err := s.store.RouteOpsPerformanceTimeseries(ctx, sqlc.RouteOpsPerformanceTimeseriesParams{Unit: interval, RouteID: routeID, FromTime: opsutil.TsNarg(from), ToTime: opsutil.TsNarg(to)})
	if err != nil {
		return nil, opsutil.StoreFailed(err, "route ops performance timeseries")
	}
	out := make([]PerfPoint, 0, len(rows))
	for _, r := range rows {
		out = append(out, PerfPoint{Bucket: r.Bucket.Time, RequestTotal: r.RequestTotal, RequestSucceeded: r.RequestSucceeded, LatencyP95: r.LatencyP95})
	}
	return out, nil
}

// Models 返回本线路下各模型表现。
func (s *Service) Models(ctx context.Context, routeID int64, from, to time.Time) ([]ModelRow, error) {
	rows, err := s.store.RouteOpsModels(ctx, sqlc.RouteOpsModelsParams{RouteID: routeID, FromTime: opsutil.TsNarg(from), ToTime: opsutil.TsNarg(to)})
	if err != nil {
		return nil, opsutil.StoreFailed(err, "route ops models")
	}
	out := make([]ModelRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, ModelRow{ModelID: r.ModelID, RequestTotal: r.RequestTotal, RequestSucceeded: r.RequestSucceeded, SuccessRate: opsutil.SuccessRate(r.RequestSucceeded, r.RequestTotal)})
	}
	return out, nil
}

// Requests 返回本线路最近请求（分页）。
func (s *Service) Requests(ctx context.Context, routeID int64, from, to time.Time, limit, offset int32) ([]RequestRow, int64, error) {
	rows, err := s.store.RouteOpsRequests(ctx, sqlc.RouteOpsRequestsParams{RouteID: routeID, FromTime: opsutil.TsNarg(from), ToTime: opsutil.TsNarg(to), PageLimit: limit, PageOffset: offset})
	if err != nil {
		return nil, 0, opsutil.StoreFailed(err, "route ops requests")
	}
	total, err := s.store.RouteOpsRequestsCount(ctx, sqlc.RouteOpsRequestsCountParams{RouteID: routeID, FromTime: opsutil.TsNarg(from), ToTime: opsutil.TsNarg(to)})
	if err != nil {
		return nil, 0, opsutil.StoreFailed(err, "route ops requests count")
	}
	out := make([]RequestRow, 0, len(rows))
	for _, r := range rows {
		row := RequestRow{RequestID: r.RequestID, At: r.CreatedAt.Time, Status: r.Status, ModelID: r.RequestedModelID, FinalChannelID: opsutil.Int8Value(r.FinalChannelID)}
		if v, ok := r.LatencyMs.(float64); ok {
			row.LatencyMs = &v
		}
		out = append(out, row)
	}
	return out, total, nil
}

func apiKeyStatus(k sqlc.RouteOpsBoundKeysRow, now time.Time) string {
	switch {
	case k.RevokedAt.Valid:
		return "revoked"
	case k.DisabledAt.Valid:
		return "disabled"
	case k.ExpiresAt.Valid && k.ExpiresAt.Time.Before(now):
		return "expired"
	default:
		return "active"
	}
}
