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
	RoutesOpsTable(ctx context.Context, arg sqlc.RoutesOpsTableParams) ([]sqlc.RoutesOpsTableRow, error)
	RoutesOpsTableCount(ctx context.Context, arg sqlc.RoutesOpsTableCountParams) (int64, error)
	RouteOpsDetail(ctx context.Context, arg sqlc.RouteOpsDetailParams) (sqlc.RouteOpsDetailRow, error)
	RouteOpsReachableModels(ctx context.Context, routeID int64) ([]sqlc.RouteOpsReachableModelsRow, error)
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

// Row 是线路运维主表行（静态配置；请求指标在详情页聚合）。
type Row struct {
	ID           int64
	Name         string
	Mode         string
	PoolKind     string
	Status       string
	Description  string
	PriceRatio   string
	RpmLimit     *int32
	TpmLimit     *int32
	RpdLimit     *int32
	CreatedAt    time.Time
	BoundKeys    int64
	PoolChannels int64
	ModelsCount  int64
}

// Detail 是详情页概览（含请求/延迟/可服务等运维指标）。
type Detail struct {
	RequestTotal     int64
	RequestSucceeded int64
	SuccessRate      float64
	FallbackTotal    int64
	FallbackRate     float64
	NoChannelTotal   int64
	LatencyP50       float64
	LatencyP95       float64
	Serviceable      bool
	Abnormal         bool
	RouteStatus      string
}

// ReachableModel 是线路可达模型（列表 Tip / 详情）。
type ReachableModel struct {
	ModelID     string
	DisplayName string
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
	Status    string
	Search    string
	SortField string
	SortDesc  bool
	Limit     int32
	Offset    int32
}

func deriveServiceable(status string, requestTotal, requestSucceeded, noChannelTotal int64) (serviceable, abnormal bool) {
	rate := opsutil.SuccessRate(requestSucceeded, requestTotal)
	abnormal = noChannelTotal > 0 || (requestTotal >= 20 && rate < 0.9)
	serviceable = status == "enabled" && !abnormal
	return serviceable, abnormal
}

// Table 返回线路运维主表（分页）。
func (s *Service) Table(ctx context.Context, p TableParams) ([]Row, int64, error) {
	rows, err := s.store.RoutesOpsTable(ctx, sqlc.RoutesOpsTableParams{
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
		out = append(out, Row{
			ID:           r.ID,
			Name:         r.Name,
			Mode:         r.Mode,
			PoolKind:     r.PoolKind,
			Status:       r.Status,
			Description:  opsutil.TextValue(r.Description),
			PriceRatio:   opsutil.NumericString(r.PriceRatio),
			RpmLimit:     opsutil.Int4Value(r.RpmLimit),
			TpmLimit:     opsutil.Int4Value(r.TpmLimit),
			RpdLimit:     opsutil.Int4Value(r.RpdLimit),
			CreatedAt:    r.CreatedAt.Time,
			BoundKeys:    r.BoundKeys,
			PoolChannels: r.PoolChannels,
			ModelsCount:  r.ModelsCount,
		})
	}
	return out, total, nil
}

// Detail 返回单线路详情概览。
func (s *Service) Detail(ctx context.Context, routeID int64, from, to time.Time) (Detail, error) {
	r, err := s.store.RouteOpsDetail(ctx, sqlc.RouteOpsDetailParams{RouteID: routeID, FromTime: opsutil.TsNarg(from), ToTime: opsutil.TsNarg(to)})
	if err != nil {
		return Detail{}, opsutil.StoreFailed(err, "route ops detail")
	}
	status := r.RouteStatus
	serviceable, abnormal := deriveServiceable(status, r.RequestTotal, r.RequestSucceeded, r.NoChannelTotal)
	d := Detail{
		RequestTotal:     r.RequestTotal,
		RequestSucceeded: r.RequestSucceeded,
		SuccessRate:      opsutil.SuccessRate(r.RequestSucceeded, r.RequestTotal),
		FallbackTotal:    r.FallbackTotal,
		NoChannelTotal:   r.NoChannelTotal,
		LatencyP50:       r.LatencyP50,
		LatencyP95:       r.LatencyP95,
		Serviceable:      serviceable,
		Abnormal:         abnormal,
		RouteStatus:      status,
	}
	if r.RequestSucceeded > 0 {
		d.FallbackRate = float64(r.FallbackTotal) / float64(r.RequestSucceeded)
	}
	return d, nil
}

// ReachableModels 返回线路可达模型列表。
func (s *Service) ReachableModels(ctx context.Context, routeID int64) ([]ReachableModel, error) {
	rows, err := s.store.RouteOpsReachableModels(ctx, routeID)
	if err != nil {
		return nil, opsutil.StoreFailed(err, "route ops reachable models")
	}
	out := make([]ReachableModel, 0, len(rows))
	for _, r := range rows {
		out = append(out, ReachableModel{ModelID: r.ModelID, DisplayName: r.DisplayName})
	}
	return out, nil
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
