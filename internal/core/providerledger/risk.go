package providerledger

import (
	"context"
	"errors"
	"strings"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const (
	RiskSourceRequest    = "request"
	RiskSourceProbe      = "probe"
	RiskStatusUnresolved = "unresolved"
	RiskStatusReconciled = "reconciled"
)

type RequestCostRiskParams struct {
	ProviderID       int64
	RequestRecordID  int64
	RequestAttemptID int64
	EstimatedAmount  pgtype.Numeric
	Currency         string
	ReasonCode       string
	Reason           string
}

type ProbeCostRiskParams struct {
	ProviderID      int64
	ProviderProbeID int64
	EstimatedAmount pgtype.Numeric
	Currency        string
	ReasonCode      string
	Reason          string
}

func (s *Service) RecordRequestCostRisk(ctx context.Context, params RequestCostRiskParams) error {
	if err := validateRequestRisk(params); err != nil {
		return err
	}
	_, err := s.createRequestRisk(ctx, s.queries, params)
	return err
}

func (s *Service) RecordRequestCostRiskWithQueries(ctx context.Context, queries *sqlc.Queries, params RequestCostRiskParams) error {
	if err := validateRequestRisk(params); err != nil {
		return err
	}
	_, err := s.createRequestRisk(ctx, queries, params)
	return err
}

func (s *Service) createRequestRisk(ctx context.Context, queries *sqlc.Queries, p RequestCostRiskParams) (sqlc.ProviderCostRisk, error) {
	row, err := queries.CreateProviderCostRisk(ctx, sqlc.CreateProviderCostRiskParams{
		ProviderID: p.ProviderID, RequestRecordID: pgtype.Int8{Int64: p.RequestRecordID, Valid: true},
		RequestAttemptID: pgtype.Int8{Int64: p.RequestAttemptID, Valid: true}, SourceType: RiskSourceRequest,
		EstimatedAmount: p.EstimatedAmount, Currency: textNarg(p.Currency), ReasonCode: p.ReasonCode, Reason: p.Reason,
	})
	if err == nil {
		return row, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return sqlc.ProviderCostRisk{}, storeFailed(err, "create provider request cost risk")
	}
	existing, lookupErr := queries.GetProviderCostRiskByRequestAttemptID(ctx, pgtype.Int8{Int64: p.RequestAttemptID, Valid: true})
	if lookupErr != nil {
		return sqlc.ProviderCostRisk{}, storeFailed(lookupErr, "lookup provider request cost risk")
	}
	if existing.ProviderID != p.ProviderID || existing.SourceType != RiskSourceRequest ||
		!existing.RequestRecordID.Valid || existing.RequestRecordID.Int64 != p.RequestRecordID ||
		!existing.RequestAttemptID.Valid || existing.RequestAttemptID.Int64 != p.RequestAttemptID {
		return sqlc.ProviderCostRisk{}, failure.New(failure.CodeLedgerIdempotencyConflict, failure.WithMessage("provider request cost risk conflict"))
	}
	return existing, nil
}

func (s *Service) RecordProbeCostRiskWithQueries(ctx context.Context, queries *sqlc.Queries, p ProbeCostRiskParams) error {
	if p.ProviderID <= 0 || p.ProviderProbeID <= 0 || strings.TrimSpace(p.ReasonCode) == "" || strings.TrimSpace(p.Reason) == "" {
		return invalidArgument("provider probe cost risk source is incomplete")
	}
	row, err := queries.CreateProviderCostRisk(ctx, sqlc.CreateProviderCostRiskParams{
		ProviderID: p.ProviderID, ProviderProbeRecordID: pgtype.Int8{Int64: p.ProviderProbeID, Valid: true},
		SourceType: RiskSourceProbe, EstimatedAmount: p.EstimatedAmount, Currency: textNarg(p.Currency),
		ReasonCode: p.ReasonCode, Reason: p.Reason,
	})
	if err == nil {
		_ = row
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return storeFailed(err, "create provider probe cost risk")
	}
	existing, lookupErr := queries.GetProviderCostRiskByProbeRecordID(ctx, pgtype.Int8{Int64: p.ProviderProbeID, Valid: true})
	if lookupErr != nil {
		return storeFailed(lookupErr, "lookup provider probe cost risk")
	}
	if existing.ProviderID != p.ProviderID || existing.SourceType != RiskSourceProbe ||
		!existing.ProviderProbeRecordID.Valid || existing.ProviderProbeRecordID.Int64 != p.ProviderProbeID {
		return failure.New(failure.CodeLedgerIdempotencyConflict, failure.WithMessage("provider probe cost risk conflict"))
	}
	return nil
}

func validateRequestRisk(p RequestCostRiskParams) error {
	if p.ProviderID <= 0 || p.RequestRecordID <= 0 || p.RequestAttemptID <= 0 || strings.TrimSpace(p.ReasonCode) == "" || strings.TrimSpace(p.Reason) == "" {
		return invalidArgument("provider request cost risk source is incomplete")
	}
	if p.Currency != "" && strings.TrimSpace(p.Currency) == "" {
		return invalidArgument("provider request cost risk currency is invalid")
	}
	return nil
}

func textNarg(value string) pgtype.Text {
	if strings.TrimSpace(value) == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: strings.TrimSpace(value), Valid: true}
}
