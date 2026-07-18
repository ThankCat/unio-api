package bootstrap

import (
	"context"

	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/lifecycle"
)

// costExposureStore 把 lifecycle 的成本敞口写入落到 channel_cost_exposures（DESIGN-bill-on-cancel 阶段一）。
// 纯追加写；失败记 warn 日志（敞口是观测事实，不阻断请求收口）。
type costExposureStore struct {
	queries *sqlc.Queries
	logger  *zap.Logger
}

func newCostExposureStore(queries *sqlc.Queries, logger *zap.Logger) *costExposureStore {
	return &costExposureStore{queries: queries, logger: logger}
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
	if err != nil && s.logger != nil {
		fields := []zap.Field{
			zap.Int64("request_record_id", params.RequestRecordID),
			zap.Int64("channel_id", params.ChannelID),
			zap.String("reason", params.Reason),
		}
		fields = append(fields, failure.LogFields(err)...)
		s.logger.Warn("record channel cost exposure failed", fields...)
	}
	return err
}
