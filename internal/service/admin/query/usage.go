package query

import (
	"context"
	"time"

	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
)

// UsageStore 定义用量只读查询所需的存储能力。
type UsageStore interface {
	ListUsageRecordsPage(ctx context.Context, arg sqlc.ListUsageRecordsPageParams) ([]sqlc.ListUsageRecordsPageRow, error)
	CountUsageRecords(ctx context.Context, arg sqlc.CountUsageRecordsParams) (int64, error)
}

// UsageListParams 是分页/过滤列出用量的入参；指针/空串/nil 表示该维度不过滤。
type UsageListParams struct {
	UserID    *int64
	ProjectID *int64
	Model     string
	From      *time.Time
	To        *time.Time
	SortField string
	SortDesc  bool
	Limit     int32
	Offset    int32
}

// Usage 是协议无关的用量事实（用于请求详情聚合）。
type Usage struct {
	ID                      int64
	RequestRecordID         int64
	UncachedInputTokens     int64
	CacheReadInputTokens    int64
	CacheWrite5mInputTokens int64
	CacheWrite1hInputTokens int64
	OutputTokensTotal       int64
	ReasoningOutputTokens   int64
	UsageSource             string
	UsageMappingVersion     string
	CreatedAt               time.Time
}

// UsageSummary 是用量列表项：用量事实 + 请求归属维度。
type UsageSummary struct {
	ID                      int64
	RequestRecordID         int64
	RequestID               string
	UserID                  int64
	ProjectID               int64
	APIKeyID                int64
	RequestedModelID        string
	ResponseModelID         *string
	Status                  string
	UncachedInputTokens     int64
	CacheReadInputTokens    int64
	CacheWrite5mInputTokens int64
	CacheWrite1hInputTokens int64
	OutputTokensTotal       int64
	ReasoningOutputTokens   int64
	UsageSource             string
	UsageMappingVersion     string
	CreatedAt               time.Time
}

// UsageService 提供用量只读查询。
type UsageService struct {
	store UsageStore
}

// NewUsageService 创建用量只读查询服务。
func NewUsageService(store UsageStore) *UsageService {
	return &UsageService{store: store}
}

// List 按 params 过滤分页倒序列出用量记录，并返回过滤后的总数。
func (s *UsageService) List(ctx context.Context, params UsageListParams) ([]UsageSummary, int64, error) {
	rows, err := s.store.ListUsageRecordsPage(ctx, sqlc.ListUsageRecordsPageParams{
		UserID:     int8Narg(params.UserID),
		ProjectID:  int8Narg(params.ProjectID),
		Model:      textNarg(params.Model),
		FromTime:   tsNarg(params.From),
		ToTime:     tsNarg(params.To),
		SortField:  textNarg(params.SortField),
		SortDesc:   boolNarg(params.SortDesc),
		PageLimit:  params.Limit,
		PageOffset: params.Offset,
	})
	if err != nil {
		return nil, 0, storeFailed(err, "list usage records")
	}

	total, err := s.store.CountUsageRecords(ctx, sqlc.CountUsageRecordsParams{
		UserID:    int8Narg(params.UserID),
		ProjectID: int8Narg(params.ProjectID),
		Model:     textNarg(params.Model),
		FromTime:  tsNarg(params.From),
		ToTime:    tsNarg(params.To),
	})
	if err != nil {
		return nil, 0, storeFailed(err, "count usage records")
	}

	items := make([]UsageSummary, 0, len(rows))
	for _, row := range rows {
		items = append(items, toUsageSummary(row))
	}
	return items, total, nil
}

func toUsage(u sqlc.UsageRecord) Usage {
	return Usage{
		ID:                      u.ID,
		RequestRecordID:         u.RequestRecordID,
		UncachedInputTokens:     u.UncachedInputTokens,
		CacheReadInputTokens:    u.CacheReadInputTokens,
		CacheWrite5mInputTokens: u.CacheWrite5mInputTokens,
		CacheWrite1hInputTokens: u.CacheWrite1hInputTokens,
		OutputTokensTotal:       u.OutputTokensTotal,
		ReasoningOutputTokens:   u.ReasoningOutputTokens,
		UsageSource:             u.UsageSource,
		UsageMappingVersion:     u.UsageMappingVersion,
		CreatedAt:               u.CreatedAt.Time,
	}
}

func toUsageSummary(u sqlc.ListUsageRecordsPageRow) UsageSummary {
	return UsageSummary{
		ID:                      u.ID,
		RequestRecordID:         u.RequestRecordID,
		RequestID:               u.RequestID,
		UserID:                  u.UserID,
		ProjectID:               u.ProjectID,
		APIKeyID:                u.ApiKeyID,
		RequestedModelID:        u.RequestedModelID,
		ResponseModelID:         textPtr(u.ResponseModelID),
		Status:                  u.Status,
		UncachedInputTokens:     u.UncachedInputTokens,
		CacheReadInputTokens:    u.CacheReadInputTokens,
		CacheWrite5mInputTokens: u.CacheWrite5mInputTokens,
		CacheWrite1hInputTokens: u.CacheWrite1hInputTokens,
		OutputTokensTotal:       u.OutputTokensTotal,
		ReasoningOutputTokens:   u.ReasoningOutputTokens,
		UsageSource:             u.UsageSource,
		UsageMappingVersion:     u.UsageMappingVersion,
		CreatedAt:               u.CreatedAt.Time,
	}
}
