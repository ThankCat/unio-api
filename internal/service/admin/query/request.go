package query

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
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
}

// RequestListParams 是分页/过滤/排序列出请求记录的入参；指针/空串/nil 表示该维度不过滤。
type RequestListParams struct {
	UserID    *int64
	ProjectID *int64
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
	ID                    int64
	RequestID             string
	UserID                int64
	ProjectID             int64
	APIKeyID              int64
	RequestedModelID      string
	IngressProtocol       string
	Operation             string
	ResponseModelID       *string
	ResponseProtocol      *string
	ResponseID            *string
	Stream                bool
	Status                string
	FinalProviderID       *int64
	FinalChannelID        *int64
	ErrorCode             *string
	ErrorMessage          *string
	DeliveryStatus        string
	ResponseStartedAt     *time.Time
	ResponseCompletedAt   *time.Time
	StartedAt             time.Time
	CompletedAt           *time.Time
	CreatedAt             time.Time
	UpdatedAt             time.Time
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
	UpstreamStatusCode    *int32
	UpstreamRequestID     *string
	ErrorCode             *string
	ErrorMessage          *string
	InternalErrorDetail   *string
	ResponseStartedAt     *time.Time
	FinalUsageReceived    bool
	StartedAt             time.Time
	CompletedAt           *time.Time
	CreatedAt             time.Time
}

// RequestDetail 是请求详情聚合：请求事实 + 上游尝试链 + usage + 账本流水 + 计费异常。
// InternalErrorDetail 仅在 includeInternal 时填充（请求级与 attempt 级一致）。
type RequestDetail struct {
	RequestSummary
	InternalErrorDetail *string
	Attempts            []Attempt
	Usage               *Usage
	LedgerEntries       []LedgerEntry
	BillingException    *BillingException
}

// RequestService 提供请求记录只读查询。
type RequestService struct {
	store RequestStore
}

// NewRequestService 创建请求只读查询服务。
func NewRequestService(store RequestStore) *RequestService {
	return &RequestService{store: store}
}

// List 按 params 过滤分页倒序列出请求记录，并返回过滤后的总数。
func (s *RequestService) List(ctx context.Context, params RequestListParams) ([]RequestSummary, int64, error) {
	rows, err := s.store.ListRequestRecordsPage(ctx, sqlc.ListRequestRecordsPageParams{
		UserID:     int8Narg(params.UserID),
		ProjectID:  int8Narg(params.ProjectID),
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
		UserID:    int8Narg(params.UserID),
		ProjectID: int8Narg(params.ProjectID),
		ApiKeyID:  int8Narg(params.APIKeyID),
		Status:    textNarg(params.Status),
		Model:     textNarg(params.Model),
		FromTime:  tsNarg(params.From),
		ToTime:    tsNarg(params.To),
	})
	if err != nil {
		return nil, 0, storeFailed(err, "count request records")
	}

	items := make([]RequestSummary, 0, len(rows))
	for _, row := range rows {
		items = append(items, toRequestSummary(row))
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

	detail := RequestDetail{RequestSummary: summaryFromRecord(record)}
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

func toRequestSummary(r sqlc.ListRequestRecordsPageRow) RequestSummary {
	return RequestSummary{
		ID:                    r.ID,
		RequestID:             r.RequestID,
		UserID:                r.UserID,
		ProjectID:             r.ProjectID,
		APIKeyID:              r.ApiKeyID,
		RequestedModelID:      r.RequestedModelID,
		IngressProtocol:       r.IngressProtocol,
		Operation:             r.Operation,
		ResponseModelID:       textPtr(r.ResponseModelID),
		ResponseProtocol:      textPtr(r.ResponseProtocol),
		ResponseID:            textPtr(r.ResponseID),
		Stream:                r.Stream,
		Status:                r.Status,
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

func summaryFromRecord(r sqlc.RequestRecord) RequestSummary {
	return RequestSummary{
		ID:                    r.ID,
		RequestID:             r.RequestID,
		UserID:                r.UserID,
		ProjectID:             r.ProjectID,
		APIKeyID:              r.ApiKeyID,
		RequestedModelID:      r.RequestedModelID,
		IngressProtocol:       r.IngressProtocol,
		Operation:             r.Operation,
		ResponseModelID:       textPtr(r.ResponseModelID),
		ResponseProtocol:      textPtr(r.ResponseProtocol),
		ResponseID:            textPtr(r.ResponseID),
		Stream:                r.Stream,
		Status:                r.Status,
		FinalProviderID:     int8Ptr(r.FinalProviderID),
		FinalChannelID:      int8Ptr(r.FinalChannelID),
		ErrorCode:           textPtr(r.ErrorCode),
		ErrorMessage:        textPtr(r.ErrorMessage),
		DeliveryStatus:      r.DeliveryStatus,
		ResponseStartedAt:     timePtr(r.ResponseStartedAt),
		ResponseCompletedAt:   timePtr(r.ResponseCompletedAt),
		StartedAt:             r.StartedAt.Time,
		CompletedAt:           timePtr(r.CompletedAt),
		CreatedAt:             r.CreatedAt.Time,
		UpdatedAt:             r.UpdatedAt.Time,
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
