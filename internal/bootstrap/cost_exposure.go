package bootstrap

import (
	"context"
	"errors"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/lifecycle"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// costExposureStore 分别保存旧 Channel 成本敞口和 Provider 待对账风险。
// 两者都是纯追加事实，写入失败不阻断请求收口。
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
		Reason:               params.ReasonCode,
		EstimatedInputTokens: params.EstimatedInputTokens,
		AssumedOutputTokens:  params.AssumedOutputTokens,
		EstimatedCostAmount:  params.EstimatedCostAmount,
		Currency:             params.Currency,
	})
	return err
}

// RecordProviderCostRisk 记录所有已真实请求上游但没有可靠 usage 的 Provider 待对账风险。
func (s *costExposureStore) RecordProviderCostRisk(ctx context.Context, params lifecycle.CostExposureParams) error {
	_, err := s.queries.CreateProviderCostRisk(ctx, sqlc.CreateProviderCostRiskParams{
		ProviderID:       params.ProviderID,
		RequestRecordID:  pgtype.Int8{Int64: params.RequestRecordID, Valid: true},
		RequestAttemptID: pgtype.Int8{Int64: params.AttemptID, Valid: true},
		SourceType:       "request", EstimatedAmount: params.EstimatedCostAmount,
		Currency:   pgtype.Text{String: params.Currency, Valid: params.Currency != ""},
		ReasonCode: params.ReasonCode, Reason: params.Reason,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	return err
}
