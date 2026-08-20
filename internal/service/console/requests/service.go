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
	SummarizeConsoleBilledRequests(context.Context, int64) (sqlc.SummarizeConsoleBilledRequestsRow, error)
	ListConsoleBilledRequestRoutes(context.Context, int64) ([]sqlc.ListConsoleBilledRequestRoutesRow, error)
	ListConsoleBilledRequestAPIKeys(context.Context, int64) ([]sqlc.ListConsoleBilledRequestAPIKeysRow, error)
	ListConsoleBilledRequestEndpoints(context.Context, int64) ([]string, error)
}

// ListParams 是当前用户计费请求列表的查询条件。
type ListParams struct {
	UserID        int64
	RouteIDs      []int64
	APIKeyIDs     []int64
	Endpoints     []string
	StreamTypes   []string
	StatusClasses []string
	Q             string
	From          *time.Time
	To            *time.Time
	SortField     string
	SortDesc      bool
	Limit         int32
	Offset        int32
}

// Item 是客户可见的计费请求列表项。
type Item struct {
	ID               int64
	RequestID        string
	CreatedAt        time.Time
	ClientIP         string
	RouteID          *int64
	RouteName        string
	APIKeyID         int64
	APIKeyName       string
	Endpoint         string
	Stream           bool
	RequestedModelID string
	ReasoningEffort  *string
	InputTokens      int64
	OutputTokens     int64
	LatencyMs        *int64
	UserChargeUSD    string
	Status           string
}

// Summary 是当前用户全部计费请求的累计指标，不受列表时间筛选影响。
type Summary struct {
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

// Filters 是当前用户计费请求上出现过的线路、密钥和端点。
type Filters struct {
	Routes    []FilterOption
	APIKeys   []FilterOption
	Endpoints []string
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

// List 返回当前用户的计费请求分页列表。
func (s *Service) List(ctx context.Context, params ListParams) ([]Item, int64, *consoleservice.Error) {
	listParams := toListSQL(params)
	rows, err := s.store.ListConsoleBilledRequests(ctx, listParams)
	if err != nil {
		return nil, 0, consoleservice.RequestUnavailable("list billed requests", err)
	}
	total := int64(0)
	if len(rows) > 0 {
		total = rows[0].TotalCount
	} else if params.Offset > 0 {
		total, err = s.store.CountConsoleBilledRequests(ctx, toCountSQL(params))
		if err != nil {
			return nil, 0, consoleservice.RequestUnavailable("count billed requests", err)
		}
	}
	items := make([]Item, 0, len(rows))
	for _, row := range rows {
		items = append(items, toItem(row))
	}
	return items, total, nil
}

// Summary 返回当前用户全部计费请求的累计指标。
func (s *Service) Summary(ctx context.Context, userID int64) (Summary, *consoleservice.Error) {
	row, err := s.store.SummarizeConsoleBilledRequests(ctx, userID)
	if err != nil {
		return Summary{}, consoleservice.RequestUnavailable("summarize billed requests", err)
	}
	return Summary{
		RequestCount:     row.RequestCount,
		TokenCount:       row.TokenCount,
		ChargeUSD:        opsutil.NumericString(row.ChargeUsd),
		AverageLatencyMs: row.AverageLatencyMs,
	}, nil
}

// Filters 返回当前用户计费请求上可用于下拉的线路、密钥和端点。
func (s *Service) Filters(ctx context.Context, userID int64) (Filters, *consoleservice.Error) {
	routes, err := s.store.ListConsoleBilledRequestRoutes(ctx, userID)
	if err != nil {
		return Filters{}, consoleservice.RequestUnavailable("list billed request routes", err)
	}
	keys, err := s.store.ListConsoleBilledRequestAPIKeys(ctx, userID)
	if err != nil {
		return Filters{}, consoleservice.RequestUnavailable("list billed request api keys", err)
	}
	endpoints, err := s.store.ListConsoleBilledRequestEndpoints(ctx, userID)
	if err != nil {
		return Filters{}, consoleservice.RequestUnavailable("list billed request endpoints", err)
	}
	out := Filters{
		Routes:    make([]FilterOption, 0, len(routes)),
		APIKeys:   make([]FilterOption, 0, len(keys)),
		Endpoints: make([]string, 0, len(endpoints)),
	}
	for _, route := range routes {
		out.Routes = append(out.Routes, FilterOption{ID: route.ID, Name: route.Name})
	}
	for _, key := range keys {
		out.APIKeys = append(out.APIKeys, FilterOption{ID: key.ID, Name: key.Name})
	}
	for _, endpoint := range endpoints {
		out.Endpoints = append(out.Endpoints, PublicEndpoint(endpoint))
	}
	return out, nil
}

func toItem(row sqlc.ListConsoleBilledRequestsRow) Item {
	item := Item{
		ID:               row.ID,
		RequestID:        row.RequestID,
		CreatedAt:        row.CreatedAt.Time,
		ClientIP:         textValue(row.ClientIp),
		RouteID:          int8Ptr(row.RouteID),
		RouteName:        textValue(row.RouteName),
		APIKeyID:         row.ApiKeyID,
		APIKeyName:       textValue(row.ApiKeyName),
		Endpoint:         PublicEndpoint(row.Endpoint),
		Stream:           row.Stream,
		RequestedModelID: row.RequestedModelID,
		ReasoningEffort:  textPtr(row.ReasoningEffort),
		InputTokens:      row.InputTokens,
		OutputTokens:     row.OutputTokens,
		LatencyMs:        latencyMs(row.StartedAt, row.CompletedAt),
		UserChargeUSD:    opsutil.NumericString(row.UserChargeUsd),
		Status:           row.Status,
	}
	return item
}

func toListSQL(params ListParams) sqlc.ListConsoleBilledRequestsParams {
	limit := params.Limit
	if limit <= 0 {
		limit = 20
	}
	return sqlc.ListConsoleBilledRequestsParams{
		UserID:        params.UserID,
		RouteIds:      emptyInts(params.RouteIDs),
		ApiKeyIds:     emptyInts(params.APIKeyIDs),
		Endpoints:     emptyStrings(InternalEndpoints(params.Endpoints)),
		StreamTypes:   emptyStrings(params.StreamTypes),
		StatusClasses: emptyStrings(params.StatusClasses),
		Q:             textNarg(params.Q),
		FromTime:      tsNarg(params.From),
		ToTime:        tsNarg(params.To),
		SortField:     textNarg(params.SortField),
		SortDesc:      pgtype.Bool{Bool: params.SortDesc, Valid: true},
		PageLimit:     limit,
		PageOffset:    params.Offset,
	}
}

func toCountSQL(params ListParams) sqlc.CountConsoleBilledRequestsParams {
	return sqlc.CountConsoleBilledRequestsParams{
		UserID:        params.UserID,
		RouteIds:      emptyInts(params.RouteIDs),
		ApiKeyIds:     emptyInts(params.APIKeyIDs),
		Endpoints:     emptyStrings(InternalEndpoints(params.Endpoints)),
		StreamTypes:   emptyStrings(params.StreamTypes),
		StatusClasses: emptyStrings(params.StatusClasses),
		Q:             textNarg(params.Q),
		FromTime:      tsNarg(params.From),
		ToTime:        tsNarg(params.To),
	}
}

func latencyMs(started, completed pgtype.Timestamptz) *int64 {
	if !started.Valid || !completed.Valid {
		return nil
	}
	ms := completed.Time.Sub(started.Time).Milliseconds()
	if ms < 0 {
		ms = 0
	}
	return &ms
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

func textPtr(value pgtype.Text) *string {
	if !value.Valid {
		return nil
	}
	s := value.String
	return &s
}

func int8Ptr(value pgtype.Int8) *int64 {
	if !value.Valid {
		return nil
	}
	v := value.Int64
	return &v
}
