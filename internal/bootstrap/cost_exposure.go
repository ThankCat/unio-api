package bootstrap

import (
	"context"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/lifecycle"
)

// costExposureStore 把 lifecycle 的成本敞口写入 channel_cost_exposures。
// 纯追加写；失败记 warn 日志（敞口是观测事实，不阻断请求收口）。
type costExposureStore struct {
	queries *sqlc.Queries
}

func newCostExposureStore(queries *sqlc.Queries) *costExposureStore {
	return &costExposureStore{queries: queries}
}

// RecordChannelCostExposure 实现 lifecycle.CostExposureRecorder。
func (s *costExposureStore) RecordChannelCostExposure(ctx context.Context, params lifecycle.CostExposureParams) error {
	_, err := s.queries.CreateChannelCostExposure(ctx, sqlc.CreateChannelCostExposureParams{
		RequestRecordID:      params.RequestRecordID,
		AttemptID:            params.AttemptID,
		ChannelID:            params.ChannelID,
		ProviderID:           params.ProviderID,
		Reason:               params.Reason,
		EstimatedInputTokens: params.EstimatedInputTokens,
		AssumedOutputTokens:  params.AssumedOutputTokens,
		EstimatedCostAmount:  params.EstimatedCostAmount,
		Currency:             params.Currency,
	})
	return err
}
