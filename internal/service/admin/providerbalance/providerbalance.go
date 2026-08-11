// Package providerbalance 编排 Admin 的 Provider 调额和账本查询。
package providerbalance

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/core/providerledger"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/admin/opsutil"
)

var moneyPattern = regexp.MustCompile(`^\d+(\.\d+)?$`)
var signedMoneyPattern = regexp.MustCompile(`^-?\d+(\.\d{1,10})?$`)

const CurrencyUSD = "USD"

type Store interface {
	GetProvider(ctx context.Context, id int64) (sqlc.Provider, error)
	GetProviderBalance(ctx context.Context, arg sqlc.GetProviderBalanceParams) (sqlc.ProviderBalance, error)
	ListProviderLedgerEntriesPage(ctx context.Context, arg sqlc.ListProviderLedgerEntriesPageParams) ([]sqlc.ListProviderLedgerEntriesPageRow, error)
	CountProviderLedgerEntries(ctx context.Context, arg sqlc.CountProviderLedgerEntriesParams) (int64, error)
}

type Ledger interface {
	AdjustCredit(ctx context.Context, params providerledger.AdjustParams) (providerledger.Entry, error)
	AdjustDebit(ctx context.Context, params providerledger.AdjustParams) (providerledger.Entry, error)
	SetTargetBalance(ctx context.Context, params providerledger.TargetParams) (providerledger.Entry, error)
}

type Service struct {
	store  Store
	ledger Ledger
}

func NewService(store Store, ledger Ledger) *Service {
	if store == nil || ledger == nil {
		panic("providerbalance: store and ledger are required")
	}
	return &Service{store: store, ledger: ledger}
}

type AdjustParams struct {
	ProviderID     int64
	Direction      string
	Amount         string
	TargetBalance  string
	Currency       string
	Reason         string
	IdempotencyKey string
}

type Adjustment struct {
	EntryID      int64
	ProviderID   int64
	EntryType    string
	Amount       string
	Currency     string
	BalanceAfter string
	Reason       string
}

func (s *Service) Adjust(ctx context.Context, params AdjustParams) (Adjustment, error) {
	if params.ProviderID <= 0 {
		return Adjustment{}, invalidArgument("provider_id", "provider_id must be greater than zero")
	}
	if err := s.ensureProvider(ctx, params.ProviderID); err != nil {
		return Adjustment{}, err
	}
	currency := strings.ToUpper(strings.TrimSpace(params.Currency))
	if currency != CurrencyUSD {
		return Adjustment{}, invalidArgument("currency", "currency must be USD")
	}
	reason := strings.TrimSpace(params.Reason)
	if reason == "" {
		return Adjustment{}, invalidArgument("reason", "reason must not be empty")
	}
	idempotencyKey := strings.TrimSpace(params.IdempotencyKey)
	if idempotencyKey == "" {
		idempotencyKey = fmt.Sprintf("admin:provider-adjust:%d:%s", params.ProviderID, uuid.NewString())
	}
	var entry providerledger.Entry
	var err error
	if strings.TrimSpace(params.TargetBalance) != "" {
		target, parseErr := parseTargetMoney(params.TargetBalance)
		if parseErr != nil {
			return Adjustment{}, parseErr
		}
		entry, err = s.ledger.SetTargetBalance(ctx, providerledger.TargetParams{
			ProviderID: params.ProviderID, TargetBalance: target, Currency: currency,
			IdempotencyKey: idempotencyKey, Reason: reason,
		})
	} else {
		amount, parseErr := parseMoney(params.Amount)
		if parseErr != nil {
			return Adjustment{}, parseErr
		}
		ledgerParams := providerledger.AdjustParams{
			ProviderID: params.ProviderID, Amount: amount, Currency: currency,
			IdempotencyKey: idempotencyKey, Reason: reason,
		}
		switch params.Direction {
		case providerledger.DirectionCredit:
			entry, err = s.ledger.AdjustCredit(ctx, ledgerParams)
		case providerledger.DirectionDebit:
			entry, err = s.ledger.AdjustDebit(ctx, ledgerParams)
		default:
			return Adjustment{}, invalidArgument("target_balance", "target_balance must not be empty")
		}
	}
	if err != nil {
		return Adjustment{}, err
	}
	return Adjustment{
		EntryID: entry.ID, ProviderID: entry.ProviderID, EntryType: entry.EntryType,
		Amount: opsutil.NumericString(entry.Amount), Currency: entry.Currency,
		BalanceAfter: opsutil.NumericString(entry.BalanceAfter), Reason: entry.Reason,
	}, nil
}

type Balance struct {
	Amount *string
	Status string
}

func (s *Service) BalanceUSD(ctx context.Context, providerID int64) (Balance, error) {
	if err := s.ensureProvider(ctx, providerID); err != nil {
		return Balance{}, err
	}
	row, err := s.store.GetProviderBalance(ctx, sqlc.GetProviderBalanceParams{ProviderID: providerID, Currency: CurrencyUSD})
	if errors.Is(err, pgx.ErrNoRows) {
		return Balance{Status: "unconfigured"}, nil
	}
	if err != nil {
		return Balance{}, storeFailed(err, "get provider balance")
	}
	amount := opsutil.NumericString(row.Balance)
	return Balance{Amount: &amount, Status: balanceStatus(row.Balance)}, nil
}

type ListParams struct {
	ProviderID int64
	EntryType  string
	RequestID  string
	From       time.Time
	To         time.Time
	Limit      int32
	Offset     int32
}

type Entry struct {
	ID                    int64
	ProviderID            int64
	RequestRecordID       *int64
	RequestAttemptID      *int64
	CostSnapshotID        *int64
	ChannelID             *int64
	RequestID             *string
	ChannelName           *string
	UpstreamModel         *string
	ProviderProbeRecordID *int64
	UsageSource           *string
	EntryType             string
	Amount                string
	Currency              string
	BalanceBefore         string
	BalanceAfter          string
	IdempotencyKey        string
	Reason                string
	CreatedAt             time.Time
}

func (s *Service) List(ctx context.Context, params ListParams) ([]Entry, int64, error) {
	if params.ProviderID <= 0 {
		return nil, 0, invalidArgument("provider_id", "provider_id must be greater than zero")
	}
	if err := s.ensureProvider(ctx, params.ProviderID); err != nil {
		return nil, 0, err
	}
	params.EntryType = strings.TrimSpace(params.EntryType)
	switch params.EntryType {
	case "", providerledger.EntryTypeUsageDebit, providerledger.EntryTypeProbeDebit, providerledger.EntryTypeAdjustmentCredit, providerledger.EntryTypeAdjustmentDebit:
	default:
		return nil, 0, invalidArgument("entry_type", "entry_type is invalid")
	}
	params.RequestID = strings.TrimSpace(params.RequestID)
	if !params.From.IsZero() && !params.To.IsZero() && !params.From.Before(params.To) {
		return nil, 0, invalidArgument("from", "from must be earlier than to")
	}
	if params.Limit <= 0 {
		params.Limit = 20
	}
	query := sqlc.ListProviderLedgerEntriesPageParams{
		ProviderID: params.ProviderID, EntryType: opsutil.TextNarg(params.EntryType), RequestID: opsutil.TextNarg(params.RequestID),
		FromTime: opsutil.TsNarg(params.From), ToTime: opsutil.TsNarg(params.To), PageLimit: params.Limit, PageOffset: params.Offset,
	}
	rows, err := s.store.ListProviderLedgerEntriesPage(ctx, query)
	if err != nil {
		return nil, 0, storeFailed(err, "list provider ledger entries")
	}
	total, err := s.store.CountProviderLedgerEntries(ctx, sqlc.CountProviderLedgerEntriesParams{
		ProviderID: params.ProviderID, EntryType: query.EntryType, RequestID: query.RequestID,
		FromTime: query.FromTime, ToTime: query.ToTime,
	})
	if err != nil {
		return nil, 0, storeFailed(err, "count provider ledger entries")
	}
	out := make([]Entry, 0, len(rows))
	for _, row := range rows {
		channelID := row.ChannelID
		if !channelID.Valid {
			channelID = row.ProbeChannelID
		}
		channelName := row.ChannelName
		if !channelName.Valid {
			channelName = row.ProbeChannelName
		}
		upstreamModel := row.UpstreamModel
		if !upstreamModel.Valid {
			upstreamModel = row.ProbeUpstreamModel
		}
		out = append(out, Entry{
			ID: row.ID, ProviderID: row.ProviderID,
			RequestRecordID: opsutil.Int8Value(row.RequestRecordID), RequestAttemptID: opsutil.Int8Value(row.RequestAttemptID),
			CostSnapshotID: opsutil.Int8Value(row.CostSnapshotID), ChannelID: opsutil.Int8Value(channelID),
			RequestID: textPtr(row.RequestID), ChannelName: textPtr(channelName), UpstreamModel: textPtr(upstreamModel),
			ProviderProbeRecordID: opsutil.Int8Value(row.ProviderProbeRecordID),
			UsageSource:           textPtr(row.UsageSource),
			EntryType:             row.EntryType, Amount: opsutil.NumericString(row.Amount), Currency: row.Currency,
			BalanceBefore: opsutil.NumericString(row.BalanceBefore), BalanceAfter: opsutil.NumericString(row.BalanceAfter),
			IdempotencyKey: row.IdempotencyKey, Reason: row.Reason, CreatedAt: row.CreatedAt.Time,
		})
	}
	return out, total, nil
}

func (s *Service) ensureProvider(ctx context.Context, providerID int64) error {
	_, err := s.store.GetProvider(ctx, providerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return failure.New(failure.CodeAdminNotFound, failure.WithMessage("provider not found"))
	}
	if err != nil {
		return storeFailed(err, "get provider")
	}
	return nil
}

func parseMoney(raw string) (pgtype.Numeric, error) {
	value := strings.TrimSpace(raw)
	if !moneyPattern.MatchString(value) {
		return pgtype.Numeric{}, invalidArgument("amount", "amount must be a positive decimal")
	}
	var amount pgtype.Numeric
	if err := amount.Scan(value); err != nil || amount.Int == nil || amount.Int.Sign() <= 0 {
		return pgtype.Numeric{}, invalidArgument("amount", "amount must be greater than zero")
	}
	return amount, nil
}

func parseTargetMoney(raw string) (pgtype.Numeric, error) {
	value := strings.TrimSpace(raw)
	if !signedMoneyPattern.MatchString(value) {
		return pgtype.Numeric{}, invalidArgument("target_balance", "target_balance must be a decimal")
	}
	var amount pgtype.Numeric
	if err := amount.Scan(value); err != nil || amount.Int == nil {
		return pgtype.Numeric{}, invalidArgument("target_balance", "target_balance must be a decimal")
	}
	return amount, nil
}

func balanceStatus(amount pgtype.Numeric) string {
	value := opsutil.NumericString(amount)
	rat, ok := new(big.Rat).SetString(value)
	if !ok {
		return "unconfigured"
	}
	if rat.Sign() < 0 {
		return "negative"
	}
	if rat.Cmp(big.NewRat(10, 1)) < 0 {
		return "low"
	}
	return "normal"
}

func textPtr(v pgtype.Text) *string {
	if !v.Valid {
		return nil
	}
	s := v.String
	return &s
}
func invalidArgument(field, message string) error {
	return failure.New(failure.CodeAdminInvalidArgument, failure.WithMessage(message), failure.WithField("field", field))
}
func storeFailed(err error, message string) error {
	return failure.Wrap(failure.CodeAdminStoreFailed, err, failure.WithMessage(message))
}
