package query

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
)

// RecoveryJobStore 定义 settlement recovery job 只读查询所需的存储能力（M8 运营任务台）。
type RecoveryJobStore interface {
	ListSettlementRecoveryJobsPage(ctx context.Context, arg sqlc.ListSettlementRecoveryJobsPageParams) ([]sqlc.ListSettlementRecoveryJobsPageRow, error)
	CountSettlementRecoveryJobs(ctx context.Context, arg sqlc.CountSettlementRecoveryJobsParams) (int64, error)
	GetSettlementRecoveryJobByID(ctx context.Context, id int64) (sqlc.SettlementRecoveryJob, error)
}

// RecoveryJobListParams 是分页/过滤列出 recovery job 的入参；指针/空串/nil 表示该维度不过滤。
type RecoveryJobListParams struct {
	Status string
	UserID *int64
	From   *time.Time
	To     *time.Time
	Limit  int32
	Offset int32
}

// RecoveryJobSummary 是 recovery job 列表项：运营关心的归属/状态/重试/金额事实（绝不含内部诊断详情）。
type RecoveryJobSummary struct {
	ID                 int64
	UserID             int64
	RequestRecordID    int64
	AttemptID          int64
	ReservationID      int64
	ResponseProtocol   string
	ResponseID         string
	ResponseModelID    string
	ModelID            int64
	ProviderID         int64
	ChannelID          int64
	UpstreamProtocol   string
	UpstreamModel      string
	FinishClass        string
	UpstreamStatusCode int32
	Currency           string
	EstimatedAmount    string
	AuthorizedAmount   string
	Status             string
	AttemptCount       int32
	MaxAttempts        int32
	NextRunAt          time.Time
	LockedBy           *string
	LockedUntil        *time.Time
	LastErrorCode      *string
	LastErrorMessage   *string
	LastAttemptedAt    *time.Time
	CompletedAt        *time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// RecoveryJobDetail 是 recovery job 详情：摘要 + 审计补充字段 + 受控的内部诊断详情。
type RecoveryJobDetail struct {
	RecoveryJobSummary

	UpstreamResponseID   string
	UpstreamFinishReason string
	UpstreamRequestID    *string
	UsageSource          string
	UsageMappingVersion  string
	FormulaVersion       string
	PricingUnit          string

	// token 用量事实（settlement 重放依据）。
	UncachedInputTokens     int64
	CacheReadInputTokens    int64
	CacheWrite5mInputTokens int64
	CacheWrite1hInputTokens int64
	OutputTokensTotal       int64
	ReasoningOutputTokens   int64

	// LastInternalErrorDetail 默认脱敏；仅 includeInternal=true 时回显（与 M6 请求详情同策）。
	LastInternalErrorDetail *string
}

// RecoveryService 提供 settlement recovery job 只读查询。
type RecoveryService struct {
	store RecoveryJobStore
}

// NewRecoveryService 创建 recovery job 只读查询服务。
func NewRecoveryService(store RecoveryJobStore) *RecoveryService {
	return &RecoveryService{store: store}
}

// List 按 params 过滤分页倒序列出 recovery job，并返回过滤后的总数。
func (s *RecoveryService) List(ctx context.Context, params RecoveryJobListParams) ([]RecoveryJobSummary, int64, error) {
	rows, err := s.store.ListSettlementRecoveryJobsPage(ctx, sqlc.ListSettlementRecoveryJobsPageParams{
		Status:     textNarg(params.Status),
		UserID:     int8Narg(params.UserID),
		FromTime:   tsNarg(params.From),
		ToTime:     tsNarg(params.To),
		PageLimit:  params.Limit,
		PageOffset: params.Offset,
	})
	if err != nil {
		return nil, 0, storeFailed(err, "list settlement recovery jobs")
	}

	total, err := s.store.CountSettlementRecoveryJobs(ctx, sqlc.CountSettlementRecoveryJobsParams{
		Status:   textNarg(params.Status),
		UserID:   int8Narg(params.UserID),
		FromTime: tsNarg(params.From),
		ToTime:   tsNarg(params.To),
	})
	if err != nil {
		return nil, 0, storeFailed(err, "count settlement recovery jobs")
	}

	items := make([]RecoveryJobSummary, 0, len(rows))
	for _, row := range rows {
		items = append(items, toRecoveryJobSummary(row))
	}
	return items, total, nil
}

// Get 按主键读取 recovery job 详情；includeInternal=false 时脱敏 last_internal_error_detail。
func (s *RecoveryService) Get(ctx context.Context, id int64, includeInternal bool) (RecoveryJobDetail, error) {
	if id <= 0 {
		return RecoveryJobDetail{}, invalidArgument("id", "id must be a positive integer")
	}

	job, err := s.store.GetSettlementRecoveryJobByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return RecoveryJobDetail{}, notFound("settlement recovery job not found")
		}
		return RecoveryJobDetail{}, storeFailed(err, "get settlement recovery job")
	}

	detail := RecoveryJobDetail{
		RecoveryJobSummary: RecoveryJobSummary{
			ID:                 job.ID,
			UserID:             job.UserID,
			RequestRecordID:    job.RequestRecordID,
			AttemptID:          job.AttemptID,
			ReservationID:      job.ReservationID,
			ResponseProtocol:   job.ResponseProtocol,
			ResponseID:         job.ResponseID,
			ResponseModelID:    job.ResponseModelID,
			ModelID:            job.ModelID,
			ProviderID:         job.ProviderID,
			ChannelID:          job.ChannelID,
			UpstreamProtocol:   job.UpstreamProtocol,
			UpstreamModel:      job.UpstreamModel,
			FinishClass:        job.FinishClass,
			UpstreamStatusCode: job.UpstreamStatusCode,
			Currency:           job.Currency,
			EstimatedAmount:    numericString(job.EstimatedAmount),
			AuthorizedAmount:   numericString(job.AuthorizedAmount),
			Status:             job.Status,
			AttemptCount:       job.AttemptCount,
			MaxAttempts:        job.MaxAttempts,
			NextRunAt:          job.NextRunAt.Time,
			LockedBy:           textPtr(job.LockedBy),
			LockedUntil:        timePtr(job.LockedUntil),
			LastErrorCode:      textPtr(job.LastErrorCode),
			LastErrorMessage:   textPtr(job.LastErrorMessage),
			LastAttemptedAt:    timePtr(job.LastAttemptedAt),
			CompletedAt:        timePtr(job.CompletedAt),
			CreatedAt:          job.CreatedAt.Time,
			UpdatedAt:          job.UpdatedAt.Time,
		},
		UpstreamResponseID:      job.UpstreamResponseID,
		UpstreamFinishReason:    job.UpstreamFinishReason,
		UpstreamRequestID:       textPtr(job.UpstreamRequestID),
		UsageSource:             job.UsageSource,
		UsageMappingVersion:     job.UsageMappingVersion,
		FormulaVersion:          job.FormulaVersion,
		PricingUnit:             job.PricingUnit,
		UncachedInputTokens:     job.UsageUncachedInputTokens,
		CacheReadInputTokens:    job.UsageCacheReadInputTokens,
		CacheWrite5mInputTokens: job.UsageCacheWrite5mInputTokens,
		CacheWrite1hInputTokens: job.UsageCacheWrite1hInputTokens,
		OutputTokensTotal:       job.UsageOutputTokensTotal,
		ReasoningOutputTokens:   job.UsageReasoningOutputTokens,
	}
	if includeInternal {
		detail.LastInternalErrorDetail = textPtr(job.LastInternalErrorDetail)
	}
	return detail, nil
}

func toRecoveryJobSummary(r sqlc.ListSettlementRecoveryJobsPageRow) RecoveryJobSummary {
	return RecoveryJobSummary{
		ID:                 r.ID,
		UserID:             r.UserID,
		RequestRecordID:    r.RequestRecordID,
		AttemptID:          r.AttemptID,
		ReservationID:      r.ReservationID,
		ResponseProtocol:   r.ResponseProtocol,
		ResponseID:         r.ResponseID,
		ResponseModelID:    r.ResponseModelID,
		ModelID:            r.ModelID,
		ProviderID:         r.ProviderID,
		ChannelID:          r.ChannelID,
		UpstreamProtocol:   r.UpstreamProtocol,
		UpstreamModel:      r.UpstreamModel,
		FinishClass:        r.FinishClass,
		UpstreamStatusCode: r.UpstreamStatusCode,
		Currency:           r.Currency,
		EstimatedAmount:    numericString(r.EstimatedAmount),
		AuthorizedAmount:   numericString(r.AuthorizedAmount),
		Status:             r.Status,
		AttemptCount:       r.AttemptCount,
		MaxAttempts:        r.MaxAttempts,
		NextRunAt:          r.NextRunAt.Time,
		LockedBy:           textPtr(r.LockedBy),
		LockedUntil:        timePtr(r.LockedUntil),
		LastErrorCode:      textPtr(r.LastErrorCode),
		LastErrorMessage:   textPtr(r.LastErrorMessage),
		LastAttemptedAt:    timePtr(r.LastAttemptedAt),
		CompletedAt:        timePtr(r.CompletedAt),
		CreatedAt:          r.CreatedAt.Time,
		UpdatedAt:          r.UpdatedAt.Time,
	}
}
