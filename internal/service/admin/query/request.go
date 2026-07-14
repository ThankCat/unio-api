package query

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-api/internal/service/admin/opsutil"
)

// RequestStore 定义请求只读查询所需的存储能力。
type RequestStore interface {
	ListRequestRecordsPage(ctx context.Context, arg sqlc.ListRequestRecordsPageParams) ([]sqlc.ListRequestRecordsPageRow, error)
	CountRequestRecords(ctx context.Context, arg sqlc.CountRequestRecordsParams) (int64, error)
	GetRequestRecordByRequestID(ctx context.Context, requestID string) (sqlc.RequestRecord, error)
	ListRequestAttemptsByRequest(ctx context.Context, requestRecordID int64) ([]sqlc.RequestAttempt, error)
	GetUsageRecordByRequest(ctx context.Context, requestRecordID int64) (sqlc.UsageRecord, error)
	ListLedgerEntriesByRequest(ctx context.Context, requestRecordID pgtype.Int8) ([]sqlc.LedgerEntry, error)
	GetLedgerBillingExceptionByRequest(ctx context.Context, requestRecordID int64) (sqlc.LedgerBillingException, error)
	GetCostSnapshotByRequest(ctx context.Context, requestRecordID int64) (sqlc.CostSnapshot, error)
	GetPriceSnapshotByRequest(ctx context.Context, requestRecordID int64) (sqlc.PriceSnapshot, error)
	GetRouteByID(ctx context.Context, id int64) (sqlc.Route, error)
}

// RequestListParams 是分页/过滤/排序列出请求记录的入参；指针/空串/nil 表示该维度不过滤。
type RequestListParams struct {
	UserID    *int64
	APIKeyID  *int64
	Status    string
	Model     string
	From      *time.Time
	To        *time.Time
	SortField string
	SortDesc  bool
	Limit     int32
	Offset    int32
}

// RequestSummary 是请求列表项（不含 internal_error_detail）。
type RequestSummary struct {
	ID                  int64
	RequestID           string
	UserID              int64
	APIKeyID            int64
	RequestedModelID    string
	IngressProtocol     string
	Operation           string
	ResponseModelID     *string
	ResponseProtocol    *string
	ResponseID          *string
	Stream              bool
	Status              string
	FinalProviderID     *int64
	FinalChannelID      *int64
	ErrorCode           *string
	ErrorMessage        *string
	DeliveryStatus      string
	ResponseStartedAt   *time.Time
	ResponseCompletedAt *time.Time
	StartedAt           time.Time
	CompletedAt         *time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// RequestListItem 是富化后的请求列表项：请求事实 + 用量/成本/扣费 + 线路/渠道链 + 计算出的时延。
// 与详情不同，列表项的这些指标来自单条 JOIN 查询（`ListRequestRecordsPage`）；无结算快照时费用类为 nil。
type RequestListItem struct {
	RequestSummary

	// token 用量（无 usage 记录时为 0）。
	UncachedInputTokens      int64
	CacheReadInputTokens     int64
	CacheWrite5mInputTokens  int64
	CacheWrite1hInputTokens  int64
	CacheWrite30mInputTokens int64
	OutputTokens             int64
	ReasoningOutputTokens    int64

	// 费用金额（USD 十进制字符串；无结算快照 / 账本时为 nil）。UserChargeUSD=用户实际扣费净额，TotalCostUSD=平台成本。
	UserChargeUSD             *string
	TotalCostUSD              *string
	UncachedInputCostUSD      *string
	CacheReadInputCostUSD     *string
	CacheWrite5mInputCostUSD  *string
	CacheWrite1hInputCostUSD  *string
	CacheWrite30mInputCostUSD *string
	OutputCostUSD             *string
	ReasoningOutputCostUSD    *string

	// 计费单价快照（USD 十进制字符串，per_1m_tokens）。平台侧=成本单价，用户侧=售价单价，供「单价 × tokens = 金额」计算过程展示。
	UncachedInputCostUnitUSD       *string
	CacheReadInputCostUnitUSD      *string
	CacheWrite5mInputCostUnitUSD   *string
	CacheWrite1hInputCostUnitUSD   *string
	CacheWrite30mInputCostUnitUSD  *string
	OutputCostUnitUSD              *string
	ReasoningOutputCostUnitUSD     *string
	UncachedInputPriceUnitUSD      *string
	CacheReadInputPriceUnitUSD     *string
	CacheWrite5mInputPriceUnitUSD  *string
	CacheWrite1hInputPriceUnitUSD  *string
	CacheWrite30mInputPriceUnitUSD *string
	OutputPriceUnitUSD             *string
	ReasoningOutputPriceUnitUSD    *string

	// DEC-027 成本来源倍率快照（倍率路径有值，覆盖/旧数据为 nil）：价格倍率 + 充值倍率。
	ChannelCostMultiplier *string
	RechargeFactor        *string

	// 用户/Key 展示（key 名 / 前缀 / 明文——明文供列表点击复制，口径同 api-keys 页）。
	APIKeyName      *string
	APIKeyPrefix    *string
	APIKeyPlaintext *string

	// 线路（请求级快照 route_id → routes.name；历史行回落当前 Key 绑定）/ 倍率 / 策略 / 最终命中渠道 / 经过的渠道链。
	RouteName        *string
	RoutePriceRatio  *string
	RouteMode        *string
	FinalChannelName *string
	ChannelChain     string

	// 模型元信息（按请求模型 id 关联）。
	ModelDisplayName *string
	ModelOwnedBy     *string

	// 推理强度（归一档位）+ 原始预算（Anthropic）/ 客户端 IP（批二埋点，历史行为 nil）。
	ReasoningEffort       *string
	ReasoningBudgetTokens *int32
	ClientIP              *string

	// 时延（由时间戳 + output tokens 计算；缺时间戳时为 nil）。
	LatencyMs *int64   // 总耗时（completed - started）
	TtftMs    *int64   // 首字（response_started - started）
	TPS       *float64 // 输出速率（output / (completed - response_started)）
}

// Attempt 是一次上游 channel 尝试事实；InternalErrorDetail 仅在 includeInternal 时填充。
type Attempt struct {
	ID                    int64
	AttemptIndex          int32
	ProviderID            int64
	ChannelID             int64
	AdapterKey            string
	UpstreamModel         string
	UpstreamProtocol      string
	UpstreamResponseID    *string
	UpstreamResponseModel *string
	UpstreamFinishReason  *string
	FinishClass           *string
	Status                string
	// FaultParty 归因：upstream / client / platform（由 DB 生成列派生；succeeded/running 为 nil）。
	FaultParty          *string
	UpstreamStatusCode  *int32
	UpstreamRequestID   *string
	ErrorCode           *string
	ErrorMessage        *string
	InternalErrorDetail *string
	ResponseStartedAt   *time.Time
	FinalUsageReceived  bool
	StartedAt           time.Time
	CompletedAt         *time.Time
	CreatedAt           time.Time
}

// RequestDetail 是请求详情聚合：请求事实 + 上游尝试链 + usage + 账本流水 + 计费异常。
// InternalErrorDetail 仅在 includeInternal 时填充（请求级与 attempt 级一致）。
type RequestDetail struct {
	RequestSummary
	InternalErrorDetail *string
	// 批二富化：线路快照 id / 归一推理强度 + 原始预算 / 客户端 IP。
	RouteID               *int64
	ReasoningEffort       *string
	ReasoningBudgetTokens *int32
	ClientIP              *string
	// 费用明细快照：平台成本单价+金额 / 用户售价单价 / 线路倍率+策略（供详情费用明细「计算过程」）。
	CostSnapshot     *CostSnapshotView
	PriceSnapshot    *PriceSnapshotView
	RoutePriceRatio  *string
	RouteMode        *string
	Attempts         []Attempt
	Usage            *Usage
	LedgerEntries    []LedgerEntry
	BillingException *BillingException
}

// CostSnapshotView 是平台成本快照的展示视图：每分项成本单价（per_1m_tokens）+ 实际金额 + 总额（USD 字符串）。
type CostSnapshotView struct {
	UncachedInputCostUnit        *string
	CacheReadInputCostUnit       *string
	CacheWrite5mInputCostUnit    *string
	CacheWrite1hInputCostUnit    *string
	CacheWrite30mInputCostUnit   *string
	OutputCostUnit               *string
	ReasoningOutputCostUnit      *string
	UncachedInputCostAmount      *string
	CacheReadInputCostAmount     *string
	CacheWrite5mInputCostAmount  *string
	CacheWrite1hInputCostAmount  *string
	CacheWrite30mInputCostAmount *string
	OutputCostAmount             *string
	ReasoningOutputCostAmount    *string
	TotalCostAmount              *string
	// DEC-027 成本来源倍率（倍率路径有值，覆盖/旧数据为 null）：价格倍率 + 充值倍率，供请求详情费用处展示新旧倍率。
	ChannelCostMultiplier *string
	RechargeFactor        *string
}

// PriceSnapshotView 是客户售价快照的展示视图：每分项售价单价（per_1m_tokens，USD 字符串）。
type PriceSnapshotView struct {
	UncachedInputPrice      *string
	CacheReadInputPrice     *string
	CacheWrite5mInputPrice  *string
	CacheWrite1hInputPrice  *string
	CacheWrite30mInputPrice *string
	OutputPrice             *string
	ReasoningOutputPrice    *string
}

func toCostSnapshotView(c sqlc.CostSnapshot) CostSnapshotView {
	return CostSnapshotView{
		UncachedInputCostUnit:        opsutil.NumericStringPtr(c.UncachedInputCost),
		CacheReadInputCostUnit:       opsutil.NumericStringPtr(c.CacheReadInputCost),
		CacheWrite5mInputCostUnit:    opsutil.NumericStringPtr(c.CacheWrite5mInputCost),
		CacheWrite1hInputCostUnit:    opsutil.NumericStringPtr(c.CacheWrite1hInputCost),
		CacheWrite30mInputCostUnit:   opsutil.NumericStringPtr(c.CacheWrite30mInputCost),
		OutputCostUnit:               opsutil.NumericStringPtr(c.OutputCost),
		ReasoningOutputCostUnit:      opsutil.NumericStringPtr(c.ReasoningOutputCost),
		UncachedInputCostAmount:      opsutil.NumericStringPtr(c.UncachedInputCostAmount),
		CacheReadInputCostAmount:     opsutil.NumericStringPtr(c.CacheReadInputCostAmount),
		CacheWrite5mInputCostAmount:  opsutil.NumericStringPtr(c.CacheWrite5mInputCostAmount),
		CacheWrite1hInputCostAmount:  opsutil.NumericStringPtr(c.CacheWrite1hInputCostAmount),
		CacheWrite30mInputCostAmount: opsutil.NumericStringPtr(c.CacheWrite30mInputCostAmount),
		OutputCostAmount:             opsutil.NumericStringPtr(c.OutputCostAmount),
		ReasoningOutputCostAmount:    opsutil.NumericStringPtr(c.ReasoningOutputCostAmount),
		TotalCostAmount:              opsutil.NumericStringPtr(c.TotalCostAmount),
		ChannelCostMultiplier:        opsutil.NumericStringPtr(c.CostMultiplier),
		RechargeFactor:               opsutil.NumericStringPtr(c.RechargeFactor),
	}
}

func toPriceSnapshotView(p sqlc.PriceSnapshot) PriceSnapshotView {
	return PriceSnapshotView{
		UncachedInputPrice:      opsutil.NumericStringPtr(p.UncachedInputPrice),
		CacheReadInputPrice:     opsutil.NumericStringPtr(p.CacheReadInputPrice),
		CacheWrite5mInputPrice:  opsutil.NumericStringPtr(p.CacheWrite5mInputPrice),
		CacheWrite1hInputPrice:  opsutil.NumericStringPtr(p.CacheWrite1hInputPrice),
		CacheWrite30mInputPrice: opsutil.NumericStringPtr(p.CacheWrite30mInputPrice),
		OutputPrice:             opsutil.NumericStringPtr(p.OutputPrice),
		ReasoningOutputPrice:    opsutil.NumericStringPtr(p.ReasoningOutputPrice),
	}
}

// RequestService 提供请求记录只读查询。
type RequestService struct {
	store RequestStore
}

// NewRequestService 创建请求只读查询服务。
func NewRequestService(store RequestStore) *RequestService {
	return &RequestService{store: store}
}

// List 按 params 过滤分页倒序列出请求记录（富化项），并返回过滤后的总数。
func (s *RequestService) List(ctx context.Context, params RequestListParams) ([]RequestListItem, int64, error) {
	rows, err := s.store.ListRequestRecordsPage(ctx, sqlc.ListRequestRecordsPageParams{
		UserID:     int8Narg(params.UserID),
		ApiKeyID:   int8Narg(params.APIKeyID),
		Status:     textNarg(params.Status),
		Model:      textNarg(params.Model),
		FromTime:   tsNarg(params.From),
		ToTime:     tsNarg(params.To),
		SortField:  textNarg(params.SortField),
		SortDesc:   boolNarg(params.SortDesc),
		PageLimit:  params.Limit,
		PageOffset: params.Offset,
	})
	if err != nil {
		return nil, 0, storeFailed(err, "list request records")
	}

	total, err := s.store.CountRequestRecords(ctx, sqlc.CountRequestRecordsParams{
		UserID:   int8Narg(params.UserID),
		ApiKeyID: int8Narg(params.APIKeyID),
		Status:   textNarg(params.Status),
		Model:    textNarg(params.Model),
		FromTime: tsNarg(params.From),
		ToTime:   tsNarg(params.To),
	})
	if err != nil {
		return nil, 0, storeFailed(err, "count request records")
	}

	items := make([]RequestListItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, toRequestListItem(row))
	}
	return items, total, nil
}

// Get 按对外 request_id 聚合返回请求详情；includeInternal=true 时附带内部错误详情。
// usage / billing exception 缺失视为正常（nil），不视为错误。
func (s *RequestService) Get(ctx context.Context, requestID string, includeInternal bool) (RequestDetail, error) {
	if requestID == "" {
		return RequestDetail{}, invalidArgument("request_id", "request_id is required")
	}

	record, err := s.store.GetRequestRecordByRequestID(ctx, requestID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return RequestDetail{}, notFound("request not found")
		}
		return RequestDetail{}, storeFailed(err, "get request record")
	}

	detail := RequestDetail{
		RequestSummary:        summaryFromRecord(record),
		RouteID:               int8Ptr(record.RouteID),
		ReasoningEffort:       textPtr(record.ReasoningEffort),
		ReasoningBudgetTokens: int4Ptr(record.ReasoningBudgetTokens),
		ClientIP:              textPtr(record.ClientIp),
	}
	if includeInternal {
		detail.InternalErrorDetail = textPtr(record.InternalErrorDetail)
	}

	attemptRows, err := s.store.ListRequestAttemptsByRequest(ctx, record.ID)
	if err != nil {
		return RequestDetail{}, storeFailed(err, "list request attempts")
	}
	detail.Attempts = make([]Attempt, 0, len(attemptRows))
	for _, a := range attemptRows {
		detail.Attempts = append(detail.Attempts, toAttempt(a, includeInternal))
	}

	usageRow, err := s.store.GetUsageRecordByRequest(ctx, record.ID)
	switch {
	case err == nil:
		u := toUsage(usageRow)
		detail.Usage = &u
	case errors.Is(err, pgx.ErrNoRows):
		// 无 usage（如失败请求）属正常情形。
	default:
		return RequestDetail{}, storeFailed(err, "get usage record")
	}

	// 费用明细快照（成本/售价）：缺快照（如失败请求）属正常，置 nil。
	costRow, err := s.store.GetCostSnapshotByRequest(ctx, record.ID)
	switch {
	case err == nil:
		v := toCostSnapshotView(costRow)
		detail.CostSnapshot = &v
	case errors.Is(err, pgx.ErrNoRows):
	default:
		return RequestDetail{}, storeFailed(err, "get cost snapshot")
	}

	priceRow, err := s.store.GetPriceSnapshotByRequest(ctx, record.ID)
	switch {
	case err == nil:
		v := toPriceSnapshotView(priceRow)
		detail.PriceSnapshot = &v
		// 线路倍率取结算当时的快照（供费用汇总倒推基准价 = 售价 ÷ 倍率）；历史无快照行为 NULL，展示端回落「—」。
		// 不再实时读 routes.price_ratio，避免管理员改倍率污染历史请求的倍率与基准价展示。
		detail.RoutePriceRatio = opsutil.NumericStringPtr(priceRow.PriceRatio)
	case errors.Is(err, pgx.ErrNoRows):
	default:
		return RequestDetail{}, storeFailed(err, "get price snapshot")
	}

	// 线路策略（route mode）仍取当前线路配置（仅展示标签，非计费口径）；线路已删则忽略。
	if record.RouteID.Valid {
		routeRow, err := s.store.GetRouteByID(ctx, record.RouteID.Int64)
		switch {
		case err == nil:
			mode := routeRow.Mode
			detail.RouteMode = &mode
		case errors.Is(err, pgx.ErrNoRows):
		default:
			return RequestDetail{}, storeFailed(err, "get route")
		}
	}

	entryRows, err := s.store.ListLedgerEntriesByRequest(ctx, pgtype.Int8{Int64: record.ID, Valid: true})
	if err != nil {
		return RequestDetail{}, storeFailed(err, "list ledger entries")
	}
	detail.LedgerEntries = make([]LedgerEntry, 0, len(entryRows))
	for _, e := range entryRows {
		detail.LedgerEntries = append(detail.LedgerEntries, toLedgerEntry(e))
	}

	exceptionRow, err := s.store.GetLedgerBillingExceptionByRequest(ctx, record.ID)
	switch {
	case err == nil:
		be := toBillingException(exceptionRow)
		detail.BillingException = &be
	case errors.Is(err, pgx.ErrNoRows):
		// 无计费异常属常态。
	default:
		return RequestDetail{}, storeFailed(err, "get billing exception")
	}

	return detail, nil
}

func toRequestListItem(r sqlc.ListRequestRecordsPageRow) RequestListItem {
	item := RequestListItem{
		RequestSummary: RequestSummary{
			ID:                  r.ID,
			RequestID:           r.RequestID,
			UserID:              r.UserID,
			APIKeyID:            r.ApiKeyID,
			RequestedModelID:    r.RequestedModelID,
			IngressProtocol:     r.IngressProtocol,
			Operation:           r.Operation,
			ResponseModelID:     textPtr(r.ResponseModelID),
			ResponseProtocol:    textPtr(r.ResponseProtocol),
			ResponseID:          textPtr(r.ResponseID),
			Stream:              r.Stream,
			Status:              r.Status,
			FinalProviderID:     int8Ptr(r.FinalProviderID),
			FinalChannelID:      int8Ptr(r.FinalChannelID),
			ErrorCode:           textPtr(r.ErrorCode),
			ErrorMessage:        textPtr(r.ErrorMessage),
			DeliveryStatus:      r.DeliveryStatus,
			ResponseStartedAt:   timePtr(r.ResponseStartedAt),
			ResponseCompletedAt: timePtr(r.ResponseCompletedAt),
			StartedAt:           r.StartedAt.Time,
			CompletedAt:         timePtr(r.CompletedAt),
			CreatedAt:           r.CreatedAt.Time,
			UpdatedAt:           r.UpdatedAt.Time,
		},
		UncachedInputTokens:      r.UncachedInputTokens,
		CacheReadInputTokens:     r.CacheReadInputTokens,
		CacheWrite5mInputTokens:  r.CacheWrite5mInputTokens,
		CacheWrite1hInputTokens:  r.CacheWrite1hInputTokens,
		CacheWrite30mInputTokens: r.CacheWrite30mInputTokens,
		OutputTokens:             r.OutputTokensTotal,
		ReasoningOutputTokens:    r.ReasoningOutputTokens,

		UserChargeUSD:             opsutil.NumericStringPtr(r.UserChargeAmount),
		TotalCostUSD:              opsutil.NumericStringPtr(r.TotalCostAmount),
		UncachedInputCostUSD:      opsutil.NumericStringPtr(r.UncachedInputCostAmount),
		CacheReadInputCostUSD:     opsutil.NumericStringPtr(r.CacheReadInputCostAmount),
		CacheWrite5mInputCostUSD:  opsutil.NumericStringPtr(r.CacheWrite5mInputCostAmount),
		CacheWrite1hInputCostUSD:  opsutil.NumericStringPtr(r.CacheWrite1hInputCostAmount),
		CacheWrite30mInputCostUSD: opsutil.NumericStringPtr(r.CacheWrite30mInputCostAmount),
		OutputCostUSD:             opsutil.NumericStringPtr(r.OutputCostAmount),
		ReasoningOutputCostUSD:    opsutil.NumericStringPtr(r.ReasoningOutputCostAmount),

		UncachedInputCostUnitUSD:       opsutil.NumericStringPtr(r.UncachedInputCost),
		CacheReadInputCostUnitUSD:      opsutil.NumericStringPtr(r.CacheReadInputCost),
		CacheWrite5mInputCostUnitUSD:   opsutil.NumericStringPtr(r.CacheWrite5mInputCost),
		CacheWrite1hInputCostUnitUSD:   opsutil.NumericStringPtr(r.CacheWrite1hInputCost),
		CacheWrite30mInputCostUnitUSD:  opsutil.NumericStringPtr(r.CacheWrite30mInputCost),
		OutputCostUnitUSD:              opsutil.NumericStringPtr(r.OutputCost),
		ReasoningOutputCostUnitUSD:     opsutil.NumericStringPtr(r.ReasoningOutputCost),
		UncachedInputPriceUnitUSD:      opsutil.NumericStringPtr(r.UncachedInputPrice),
		CacheReadInputPriceUnitUSD:     opsutil.NumericStringPtr(r.CacheReadInputPrice),
		CacheWrite5mInputPriceUnitUSD:  opsutil.NumericStringPtr(r.CacheWrite5mInputPrice),
		CacheWrite1hInputPriceUnitUSD:  opsutil.NumericStringPtr(r.CacheWrite1hInputPrice),
		CacheWrite30mInputPriceUnitUSD: opsutil.NumericStringPtr(r.CacheWrite30mInputPrice),
		OutputPriceUnitUSD:             opsutil.NumericStringPtr(r.OutputPrice),
		ReasoningOutputPriceUnitUSD:    opsutil.NumericStringPtr(r.ReasoningOutputPrice),

		ChannelCostMultiplier: opsutil.NumericStringPtr(r.ChannelCostMultiplier),
		RechargeFactor:        opsutil.NumericStringPtr(r.RechargeFactor),

		APIKeyName:      textPtr(r.ApiKeyName),
		APIKeyPrefix:    textPtr(r.ApiKeyPrefix),
		APIKeyPlaintext: textPtr(r.ApiKeyPlaintext),

		RouteName:        textPtr(r.RouteName),
		RoutePriceRatio:  opsutil.NumericStringPtr(r.RoutePriceRatio),
		RouteMode:        textPtr(r.RouteMode),
		FinalChannelName: textPtr(r.FinalChannelName),
		ChannelChain:     r.ChannelChain,

		ModelDisplayName: textPtr(r.ModelDisplayName),
		ModelOwnedBy:     textPtr(r.ModelOwnedBy),

		ReasoningEffort:       textPtr(r.ReasoningEffort),
		ReasoningBudgetTokens: int4Ptr(r.ReasoningBudgetTokens),
		ClientIP:              textPtr(r.ClientIp),
	}

	// 时延计算：均由已返回的时间戳派生。started_at 恒有值。
	started := r.StartedAt.Time
	if r.CompletedAt.Valid {
		ms := r.CompletedAt.Time.Sub(started).Milliseconds()
		if ms >= 0 {
			item.LatencyMs = &ms
		}
	}
	if r.ResponseStartedAt.Valid {
		ttft := r.ResponseStartedAt.Time.Sub(started).Milliseconds()
		if ttft >= 0 {
			item.TtftMs = &ttft
		}
	}
	// TPS = 输出 token / 生成时长（completed - response_started）。
	if r.CompletedAt.Valid && r.ResponseStartedAt.Valid && r.OutputTokensTotal > 0 {
		genSec := r.CompletedAt.Time.Sub(r.ResponseStartedAt.Time).Seconds()
		if genSec > 0 {
			tps := float64(r.OutputTokensTotal) / genSec
			item.TPS = &tps
		}
	}

	return item
}

func summaryFromRecord(r sqlc.RequestRecord) RequestSummary {
	return RequestSummary{
		ID:                  r.ID,
		RequestID:           r.RequestID,
		UserID:              r.UserID,
		APIKeyID:            r.ApiKeyID,
		RequestedModelID:    r.RequestedModelID,
		IngressProtocol:     r.IngressProtocol,
		Operation:           r.Operation,
		ResponseModelID:     textPtr(r.ResponseModelID),
		ResponseProtocol:    textPtr(r.ResponseProtocol),
		ResponseID:          textPtr(r.ResponseID),
		Stream:              r.Stream,
		Status:              r.Status,
		FinalProviderID:     int8Ptr(r.FinalProviderID),
		FinalChannelID:      int8Ptr(r.FinalChannelID),
		ErrorCode:           textPtr(r.ErrorCode),
		ErrorMessage:        textPtr(r.ErrorMessage),
		DeliveryStatus:      r.DeliveryStatus,
		ResponseStartedAt:   timePtr(r.ResponseStartedAt),
		ResponseCompletedAt: timePtr(r.ResponseCompletedAt),
		StartedAt:           r.StartedAt.Time,
		CompletedAt:         timePtr(r.CompletedAt),
		CreatedAt:           r.CreatedAt.Time,
		UpdatedAt:           r.UpdatedAt.Time,
	}
}

func toAttempt(a sqlc.RequestAttempt, includeInternal bool) Attempt {
	out := Attempt{
		ID:                    a.ID,
		AttemptIndex:          a.AttemptIndex,
		ProviderID:            a.ProviderID,
		ChannelID:             a.ChannelID,
		AdapterKey:            a.AdapterKey,
		UpstreamModel:         a.UpstreamModel,
		UpstreamProtocol:      a.UpstreamProtocol,
		UpstreamResponseID:    textPtr(a.UpstreamResponseID),
		UpstreamResponseModel: textPtr(a.UpstreamResponseModel),
		UpstreamFinishReason:  textPtr(a.UpstreamFinishReason),
		FinishClass:           textPtr(a.FinishClass),
		Status:                a.Status,
		FaultParty:            textPtr(a.FaultParty),
		UpstreamStatusCode:    int4Ptr(a.UpstreamStatusCode),
		UpstreamRequestID:     textPtr(a.UpstreamRequestID),
		ErrorCode:             textPtr(a.ErrorCode),
		ErrorMessage:          textPtr(a.ErrorMessage),
		ResponseStartedAt:     timePtr(a.ResponseStartedAt),
		FinalUsageReceived:    a.FinalUsageReceived,
		StartedAt:             a.StartedAt.Time,
		CompletedAt:           timePtr(a.CompletedAt),
		CreatedAt:             a.CreatedAt.Time,
	}
	if includeInternal {
		out.InternalErrorDetail = textPtr(a.InternalErrorDetail)
	}
	return out
}
