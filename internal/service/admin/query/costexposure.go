package query

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/admin/opsutil"
)

// CostExposureStore 定义成本敞口只读查询所需的存储能力（DESIGN-bill-on-cancel 阶段一）。
type CostExposureStore interface {
	SummarizeChannelCostExposures(ctx context.Context, arg sqlc.SummarizeChannelCostExposuresParams) ([]sqlc.SummarizeChannelCostExposuresRow, error)
	ListChannelCostExposuresPage(ctx context.Context, arg sqlc.ListChannelCostExposuresPageParams) ([]sqlc.ListChannelCostExposuresPageRow, error)
	CountChannelCostExposures(ctx context.Context, arg sqlc.CountChannelCostExposuresParams) (int64, error)
}

// CostExposureSummary 是一个渠道在时间范围内的成本敞口聚合（金额为十进制字符串上界估算）。
type CostExposureSummary struct {
	ChannelID          int64
	ChannelName        string
	ProviderID         int64
	Currency           string
	Exposures          int64
	TotalEstimatedCost string
}

// CostExposureItem 是一条成本敞口明细。
type CostExposureItem struct {
	ID                   int64
	RequestRecordID      int64
	RequestID            string
	AttemptID            int64
	ChannelID            int64
	ProviderID           int64
	Reason               string
	EstimatedInputTokens int64
	AssumedOutputTokens  int64
	EstimatedCostAmount  string
	Currency             string
	CreatedAt            time.Time
}

// CostExposureListParams 是按渠道分页列出敞口明细的入参。
type CostExposureListParams struct {
	ChannelID int64
	From      time.Time
	To        time.Time
	Limit     int32
	Offset    int32
}

// CostExposureService 提供成本敞口只读查询。
type CostExposureService struct {
	store CostExposureStore
}

// NewCostExposureService 创建成本敞口只读查询服务。
func NewCostExposureService(store CostExposureStore) *CostExposureService {
	return &CostExposureService{store: store}
}

// Summarize 按渠道聚合时间范围内的成本敞口。
func (s *CostExposureService) Summarize(ctx context.Context, from, to time.Time) ([]CostExposureSummary, error) {
	rows, err := s.store.SummarizeChannelCostExposures(ctx, sqlc.SummarizeChannelCostExposuresParams{
		FromTime: pgtype.Timestamptz{Time: from, Valid: true},
		ToTime:   pgtype.Timestamptz{Time: to, Valid: true},
	})
	if err != nil {
		return nil, storeFailed(err, "summarize channel cost exposures")
	}

	out := make([]CostExposureSummary, 0, len(rows))
	for _, row := range rows {
		out = append(out, CostExposureSummary{
			ChannelID:          row.ChannelID,
			ChannelName:        row.ChannelName,
			ProviderID:         row.ProviderID,
			Currency:           row.Currency,
			Exposures:          row.Exposures,
			TotalEstimatedCost: numericStringOrZero(row.TotalEstimatedCost),
		})
	}
	return out, nil
}

// List 按渠道分页倒序列出成本敞口明细，并返回过滤后的总数。
func (s *CostExposureService) List(ctx context.Context, params CostExposureListParams) ([]CostExposureItem, int64, error) {
	if params.ChannelID <= 0 {
		return nil, 0, invalidArgument("channel_id", "channel id must be positive")
	}

	rows, err := s.store.ListChannelCostExposuresPage(ctx, sqlc.ListChannelCostExposuresPageParams{
		ChannelID:  params.ChannelID,
		FromTime:   pgtype.Timestamptz{Time: params.From, Valid: true},
		ToTime:     pgtype.Timestamptz{Time: params.To, Valid: true},
		PageLimit:  params.Limit,
		PageOffset: params.Offset,
	})
	if err != nil {
		return nil, 0, storeFailed(err, "list channel cost exposures")
	}

	total, err := s.store.CountChannelCostExposures(ctx, sqlc.CountChannelCostExposuresParams{
		ChannelID: params.ChannelID,
		FromTime:  pgtype.Timestamptz{Time: params.From, Valid: true},
		ToTime:    pgtype.Timestamptz{Time: params.To, Valid: true},
	})
	if err != nil {
		return nil, 0, storeFailed(err, "count channel cost exposures")
	}

	items := make([]CostExposureItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, CostExposureItem{
			ID:                   row.ID,
			RequestRecordID:      row.RequestRecordID,
			RequestID:            row.RequestID,
			AttemptID:            row.AttemptID,
			ChannelID:            row.ChannelID,
			ProviderID:           row.ProviderID,
			Reason:               row.Reason,
			EstimatedInputTokens: row.EstimatedInputTokens,
			AssumedOutputTokens:  row.AssumedOutputTokens,
			EstimatedCostAmount:  numericStringOrZero(row.EstimatedCostAmount),
			Currency:             row.Currency,
			CreatedAt:            row.CreatedAt.Time,
		})
	}
	return items, total, nil
}

// numericStringOrZero 把 NUMERIC 转成十进制字符串；无效值回 "0"（敞口金额列 NOT NULL，理论不可达）。
func numericStringOrZero(n pgtype.Numeric) string {
	if s := opsutil.NumericStringPtr(n); s != nil {
		return *s
	}
	return "0"
}
