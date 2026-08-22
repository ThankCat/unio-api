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
	ListConsoleFilterRoutes(context.Context) ([]sqlc.ListConsoleFilterRoutesRow, error)
	ListConsoleFilterAPIKeys(context.Context, int64) ([]sqlc.ListConsoleFilterAPIKeysRow, error)
	ListConsoleBilledRequestEndpoints(context.Context, int64) ([]string, error)
	ListConsoleBilledRequestStreamTypes(context.Context, int64) ([]bool, error)
}

// ListParams 是当前用户实际扣费请求列表的查询条件。
type ListParams struct {
	UserID      int64
	RouteIDs    []int64
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
	ID                        int64
	RequestID                 string
	CreatedAt                 time.Time
	ClientIP                  string
	RouteID                   *int64
	RouteName                 string
	APIKeyID                  int64
	APIKeyName                string
	APIKeyPrefix              string
	APIKeyPlaintext           *string
	Endpoint                  string
	Stream                    bool
	RequestedModelID          string
	ModelDisplayName          string
	IngressProtocol           string
	InputPricePer1M           *string
	OutputPricePer1M          *string
	CacheReadPricePer1M       *string
	CacheWrite5mPricePer1M    *string
	CacheWrite1hPricePer1M    *string
	CacheWrite30mPricePer1M   *string
	ReasoningOutputPricePer1M *string
	PriceServiceTier          *string
	ReasoningEffort           *string
	UncachedInputTokens       int64
	CacheReadInputTokens      int64
	CacheWrite5mInputTokens   int64
	CacheWrite1hInputTokens   int64
	CacheWrite30mInputTokens  int64
	InputTokens               int64
	OutputTokens              int64
	ReasoningOutputTokens     int64
	LatencyMs                 *int64
	FirstTokenMs              *int64
	TPS                       *float64
	UserChargeUSD             string
}

// SummaryParams 是账户累计汇总条件。筛选口径与列表相同；From/To 可空。
type SummaryParams struct {
	UserID      int64
	RouteIDs    []int64
	APIKeyIDs   []int64
	Endpoints   []string
	StreamTypes []string
	Q           string
	From        *time.Time
	To          *time.Time
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
	CacheWriteTokenCount    int64
	ChargeUSD               string
	UncachedInputChargeUSD  string
	OutputChargeUSD         string
	CacheReadChargeUSD      string
	CacheWriteChargeUSD     string
	ListChargeUSD           string
	AverageLatencyMs        float64
	AverageFirstTokenMs     float64
	MedianLatencyMs         float64
	AverageTPS              float64
	TopModels               []SummaryModel
}

// FilterOption 是下拉筛选项。
type FilterOption struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// Filters 是线路目录全量、当前用户的 API Key，以及扣费请求上出现过的端点和类型。
type Filters struct {
	Routes      []FilterOption
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
	bounds := toSummarySQL(params)
	row, err := s.store.SummarizeConsoleBilledRequests(ctx, bounds)
	if err != nil {
		return Summary{}, consoleservice.RequestUnavailable("summarize charged requests", err)
	}
	models, err := s.store.ListConsoleBilledRequestTopModels(ctx, sqlc.ListConsoleBilledRequestTopModelsParams{
		UserID:      params.UserID,
		RouteIds:    bounds.RouteIds,
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
	return Summary{
		RequestCount:            row.RequestCount,
		StreamCount:             row.StreamCount,
		TokenCount:              row.TokenCount,
		InputTokenCount:         row.InputTokenCount,
		OutputTokenCount:        row.OutputTokenCount,
		UncachedInputTokenCount: row.UncachedInputTokenCount,
		CacheReadTokenCount:     row.CacheReadTokenCount,
		CacheWriteTokenCount:    row.CacheWriteTokenCount,
		ChargeUSD:               opsutil.NumericString(row.ChargeUsd),
		UncachedInputChargeUSD:  opsutil.NumericString(row.UncachedInputChargeUsd),
		OutputChargeUSD:         opsutil.NumericString(row.OutputChargeUsd),
		CacheReadChargeUSD:      opsutil.NumericString(row.CacheReadChargeUsd),
		CacheWriteChargeUSD:     opsutil.NumericString(row.CacheWriteChargeUsd),
		ListChargeUSD:           opsutil.NumericString(row.ListChargeUsd),
		AverageLatencyMs:        row.AverageLatencyMs,
		AverageFirstTokenMs:     row.AverageFirstTokenMs,
		MedianLatencyMs:         row.MedianLatencyMs,
		AverageTPS:              row.AverageTps,
		TopModels:               topModels,
	}, nil
}

// Filters 返回线路目录全量、当前用户的 API Key，以及扣费请求上出现过的端点和类型。
func (s *Service) Filters(ctx context.Context, userID int64) (Filters, *consoleservice.Error) {
	routes, err := s.store.ListConsoleFilterRoutes(ctx)
	if err != nil {
		return Filters{}, consoleservice.RequestUnavailable("list filter routes", err)
	}
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
		Routes:      make([]FilterOption, 0, len(routes)),
		APIKeys:     make([]FilterOption, 0, len(keys)),
		Endpoints:   make([]string, 0, len(endpoints)),
		StreamTypes: make([]string, 0, len(streams)),
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
		ID:                        row.ID,
		RequestID:                 row.RequestID,
		CreatedAt:                 row.CreatedAt.Time,
		ClientIP:                  textValue(row.ClientIp),
		RouteID:                   int8Ptr(row.RouteID),
		RouteName:                 textValue(row.RouteName),
		APIKeyID:                  row.ApiKeyID,
		APIKeyName:                textValue(row.ApiKeyName),
		APIKeyPrefix:              textValue(row.ApiKeyPrefix),
		APIKeyPlaintext:           textPtr(row.ApiKeyPlaintext),
		Endpoint:                  PublicEndpoint(row.Endpoint),
		Stream:                    row.Stream,
		RequestedModelID:          row.RequestedModelID,
		ModelDisplayName:          modelDisplayName(row.ModelDisplayName, row.RequestedModelID),
		IngressProtocol:           row.IngressProtocol,
		InputPricePer1M:           opsutil.NumericStringPtr(row.InputPricePer1m),
		OutputPricePer1M:          opsutil.NumericStringPtr(row.OutputPricePer1m),
		CacheReadPricePer1M:       opsutil.NumericStringPtr(row.CacheReadPricePer1m),
		CacheWrite5mPricePer1M:    opsutil.NumericStringPtr(row.CacheWrite5mPricePer1m),
		CacheWrite1hPricePer1M:    opsutil.NumericStringPtr(row.CacheWrite1hPricePer1m),
		CacheWrite30mPricePer1M:   opsutil.NumericStringPtr(row.CacheWrite30mPricePer1m),
		ReasoningOutputPricePer1M: opsutil.NumericStringPtr(row.ReasoningOutputPricePer1m),
		PriceServiceTier:          textPtr(row.PriceServiceTier),
		ReasoningEffort:           textPtr(row.ReasoningEffort),
		UncachedInputTokens:       row.UncachedInputTokens,
		CacheReadInputTokens:      row.CacheReadInputTokens,
		CacheWrite5mInputTokens:   row.CacheWrite5mInputTokens,
		CacheWrite1hInputTokens:   row.CacheWrite1hInputTokens,
		CacheWrite30mInputTokens:  row.CacheWrite30mInputTokens,
		InputTokens:               row.InputTokens,
		OutputTokens:              row.OutputTokens,
		ReasoningOutputTokens:     row.ReasoningOutputTokens,
		UserChargeUSD:             opsutil.NumericString(row.UserChargeUsd),
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
		RouteIds:    emptyInts(params.RouteIDs),
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
		RouteIds:    emptyInts(params.RouteIDs),
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
		RouteIds:    emptyInts(params.RouteIDs),
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

func int8Ptr(value pgtype.Int8) *int64 {
	if !value.Valid {
		return nil
	}
	v := value.Int64
	return &v
}
