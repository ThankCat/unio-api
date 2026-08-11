package providerledger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/core/billing"
	"github.com/ThankCat/unio-gateway/internal/core/usage"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// ProbeParams 是一次真实 Provider 探测的账务输入；不包含客户 request id。
type ProbeParams struct {
	ProviderID     int64
	ChannelID      int64
	ModelID        int64
	Protocol       string
	Source         string
	UpstreamModel  string
	Success        bool
	HTTPStatus     int
	ErrorCode      string
	Message        string
	LatencyMs      int64
	StartedAt      time.Time
	Facts          *adapter.ResponseFacts
	IdempotencyKey string
}

// AccountProbe 先保存不可变探测事实，再在同一事务中按可靠 usage 和明确价格写 probe_debit。
func (s *Service) AccountProbe(ctx context.Context, p ProbeParams) error {
	if err := validateProbe(p); err != nil {
		return err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return storeFailed(err, "begin provider probe accounting transaction")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.queries.WithTx(tx)
	if err := lockIdempotency(ctx, q, p.IdempotencyKey); err != nil {
		return err
	}
	if existing, err := q.GetProviderProbeRecordByIdempotencyKey(ctx, p.IdempotencyKey); err == nil {
		return s.ensureProbeAccountingIdempotent(ctx, q, existing, p)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return storeFailed(err, "lookup provider probe idempotency key")
	}

	pricingAt := p.StartedAt.UTC()
	if p.StartedAt.IsZero() {
		pricingAt = time.Now().UTC()
	}
	var cost billing.ProviderCost
	snapshot, pricingErr := s.resolveProbeCost(ctx, q, p.ChannelID, p.ModelID, pricingAt)
	usageReliable := p.Success && p.Facts != nil && p.Facts.UsageSource != usage.SourcePartialStreamEstimate && p.Facts.Usage.Valid()
	costKnown := false
	if usageReliable && pricingErr == nil {
		calculated, calculateErr := (billing.Service{}).CalculateProviderCost(p.Facts.Usage, snapshot)
		if calculateErr == nil {
			cost = calculated
			costKnown = true
		}
	}
	var usageJSON []byte
	if p.Facts != nil {
		usageJSON, _ = json.Marshal(p.Facts)
	}
	probe, err := q.CreateProviderProbeRecord(ctx, sqlc.CreateProviderProbeRecordParams{
		ProviderID: p.ProviderID, ChannelID: p.ChannelID, ModelID: pgtype.Int8{Int64: p.ModelID, Valid: p.ModelID > 0},
		Protocol: p.Protocol, Source: p.Source, UpstreamModel: p.UpstreamModel, Success: p.Success,
		HttpStatus: int32(clampStatus(p.HTTPStatus)), ErrorCode: textNarg(p.ErrorCode), Message: textNarg(p.Message),
		LatencyMs: pgtype.Int8{Int64: p.LatencyMs, Valid: p.LatencyMs >= 0}, UsageSource: usageSource(p.Facts), UsageFacts: usageJSON,
		UsageReliable: usageReliable, CostAmount: costAmount(cost, costKnown), Currency: costText(snapshot.Currency, costKnown),
		FormulaVersion: costText(snapshot.FormulaVersion, costKnown), IdempotencyKey: p.IdempotencyKey,
	})
	if err != nil {
		return storeFailed(err, "create provider probe record")
	}
	if costKnown && !numericIsZero(cost.TotalCostAmount) {
		entry, err := s.debitProbeWithQueries(ctx, q, p.ProviderID, probe.ID, p.Facts.UsageSource, cost.TotalCostAmount, snapshot.Currency,
			fmt.Sprintf("provider:probe:%d", probe.ID), "模型探测产生的服务商成本")
		if err != nil {
			return err
		}
		_ = entry
	}
	if err := tx.Commit(ctx); err != nil {
		return storeFailed(err, "commit provider probe accounting transaction")
	}
	return nil
}

func (s *Service) ensureProbeAccountingIdempotent(ctx context.Context, q *sqlc.Queries, row sqlc.ProviderProbeRecord, p ProbeParams) error {
	if row.ProviderID != p.ProviderID || row.ChannelID != p.ChannelID || row.Source != p.Source || row.UpstreamModel != p.UpstreamModel || row.Success != p.Success {
		return failure.New(failure.CodeLedgerIdempotencyConflict, failure.WithMessage("provider probe idempotency key conflict"))
	}
	_, ledgerErr := q.GetProviderLedgerEntryByProbeRecordID(ctx, pgtype.Int8{Int64: row.ID, Valid: true})
	expectsDebit := row.CostAmount.Valid && isPositive(row.CostAmount)
	if expectsDebit && errors.Is(ledgerErr, pgx.ErrNoRows) {
		return failure.New(failure.CodeLedgerIdempotencyConflict, failure.WithMessage("provider probe debit is missing"))
	}
	if !expectsDebit && ledgerErr == nil {
		return failure.New(failure.CodeLedgerIdempotencyConflict, failure.WithMessage("provider probe has unexpected debit"))
	}
	if ledgerErr != nil && !errors.Is(ledgerErr, pgx.ErrNoRows) {
		return storeFailed(ledgerErr, "lookup provider probe ledger entry")
	}
	return nil
}

func validateProbe(p ProbeParams) error {
	if p.ProviderID <= 0 || p.ChannelID <= 0 || p.ModelID <= 0 || p.Protocol == "" || p.Source == "" || p.UpstreamModel == "" || p.IdempotencyKey == "" {
		return invalidArgument("provider probe source is incomplete")
	}
	if p.HTTPStatus < 0 || p.HTTPStatus > 599 || p.LatencyMs < 0 {
		return invalidArgument("provider probe status or latency is invalid")
	}
	return nil
}

func usageSource(facts *adapter.ResponseFacts) pgtype.Text {
	if facts == nil || facts.UsageSource == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: string(facts.UsageSource), Valid: true}
}

func costAmount(cost billing.ProviderCost, known bool) pgtype.Numeric {
	if !known {
		return pgtype.Numeric{}
	}
	return cost.TotalCostAmount
}

func costText(value string, known bool) pgtype.Text {
	if !known {
		return pgtype.Text{}
	}
	return textNarg(value)
}

func textNarg(value string) pgtype.Text {
	value = strings.TrimSpace(value)
	if value == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: value, Valid: true}
}

func clampStatus(v int) int {
	if v < 0 {
		return 0
	}
	if v > 599 {
		return 599
	}
	return v
}

func numericIsZero(v pgtype.Numeric) bool {
	return v.Valid && v.Int != nil && numericRat(v).Sign() == 0
}

func (s *Service) resolveProbeCost(ctx context.Context, q *sqlc.Queries, channelID, modelID int64, at time.Time) (billing.ProviderCostSnapshot, error) {
	t := pgtype.Timestamptz{Time: at, Valid: true}
	price, err := q.FindActiveChannelPrice(ctx, sqlc.FindActiveChannelPriceParams{ChannelID: channelID, ModelID: modelID, AtTime: t})
	if err == nil {
		return billing.ProviderCostSnapshot{Currency: price.Currency, PricingUnit: price.PricingUnit,
			UncachedInputCost: numericOrZero(price.UncachedInputCost), CacheReadInputCost: price.CacheReadInputCost,
			CacheWrite5mInputCost: price.CacheWrite5mInputCost, CacheWrite1hInputCost: price.CacheWrite1hInputCost,
			CacheWrite30mInputCost: price.CacheWrite30mInputCost, OutputCost: numericOrZero(price.OutputCost),
			ReasoningOutputCost: price.ReasoningOutputCost, FormulaVersion: billing.FormulaVersionV1}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return billing.ProviderCostSnapshot{}, err
	}
	base, err := q.FindActiveModelPrice(ctx, sqlc.FindActiveModelPriceParams{ModelID: modelID, AtTime: t})
	if err != nil {
		return billing.ProviderCostSnapshot{}, err
	}
	mult, err := q.FindActiveChannelCostMultiplier(ctx, sqlc.FindActiveChannelCostMultiplierParams{ChannelID: channelID, ModelID: pgtype.Int8{Int64: modelID, Valid: true}, AtTime: t})
	if err != nil {
		return billing.ProviderCostSnapshot{}, err
	}
	recharge := pgtype.Numeric{Int: big.NewInt(1), Exp: 0, Valid: true}
	if factor, factorErr := q.FindActiveChannelRechargeFactor(ctx, sqlc.FindActiveChannelRechargeFactorParams{ChannelID: channelID, AtTime: t}); factorErr == nil {
		recharge = factor.Factor
	} else if !errors.Is(factorErr, pgx.ErrNoRows) {
		return billing.ProviderCostSnapshot{}, factorErr
	}
	baseSnapshot := billing.ModelPriceToProviderCost(billing.CustomerPriceSnapshot{
		Currency: base.Currency, PricingUnit: base.PricingUnit, UncachedInputPrice: base.UncachedInputPrice,
		CacheReadInputPrice: base.CacheReadInputPrice, CacheWrite5mInputPrice: base.CacheWrite5mInputPrice,
		CacheWrite1hInputPrice: base.CacheWrite1hInputPrice, CacheWrite30mInputPrice: base.CacheWrite30mInputPrice,
		OutputPrice: base.OutputPrice, ReasoningOutputPrice: base.ReasoningOutputPrice, FormulaVersion: billing.FormulaVersionV1,
	})
	scaled, err := billing.ScaleProviderCostByFactors(baseSnapshot, mult.Multiplier, recharge)
	if err != nil {
		return billing.ProviderCostSnapshot{}, err
	}
	scaled.UncachedInputCost = numericOrZero(scaled.UncachedInputCost)
	scaled.OutputCost = numericOrZero(scaled.OutputCost)
	return scaled, nil
}

func numericOrZero(v pgtype.Numeric) pgtype.Numeric {
	if v.Valid {
		return v
	}
	return pgtype.Numeric{Int: big.NewInt(0), Exp: 0, Valid: true}
}
