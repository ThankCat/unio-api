// Package providerledger 负责 Provider 内部余额和不可变账本。
// 该账本与用户预授权账本分离：余额允许为负，也不参与请求准入。
package providerledger

import (
	"context"
	"errors"
	"math/big"
	"strings"

	"github.com/ThankCat/unio-gateway/internal/core/usage"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const (
	EntryTypeUsageDebit       = "usage_debit"
	EntryTypeProbeDebit       = "probe_debit"
	EntryTypeAdjustmentCredit = "adjustment_credit"
	EntryTypeAdjustmentDebit  = "adjustment_debit"
)

const (
	DirectionCredit = "credit"
	DirectionDebit  = "debit"
)

// TxBeginner 是 Provider 账本开启独立调额事务所需的最小能力。
type TxBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Entry 是 Provider 账本流水的领域视图。
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
	UsageSource           usage.Source
	EntryType             string
	Amount                pgtype.Numeric
	Currency              string
	BalanceBefore         pgtype.Numeric
	BalanceAfter          pgtype.Numeric
	IdempotencyKey        string
	Reason                string
}

// UsageDebitParams 描述一笔由可靠成本快照产生的 Provider 消费。
type UsageDebitParams struct {
	ProviderID       int64
	RequestRecordID  int64
	RequestAttemptID int64
	CostSnapshotID   int64
	ChannelID        int64
	RequestID        string
	ChannelName      string
	UpstreamModel    string
	UsageSource      usage.Source
	Amount           pgtype.Numeric
	Currency         string
	IdempotencyKey   string
	Reason           string
}

// AdjustParams 描述一次手工加款或扣款。amount 始终为正数，方向由方法决定。
type AdjustParams struct {
	ProviderID     int64
	Amount         pgtype.Numeric
	Currency       string
	IdempotencyKey string
	Reason         string
}

// TargetParams 描述一次把 Provider 余额调整到指定最终值的手工操作。
type TargetParams struct {
	ProviderID     int64
	TargetBalance  pgtype.Numeric
	Currency       string
	IdempotencyKey string
	Reason         string
}

// Service 提供 Provider 余额与账本操作。
type Service struct {
	db      TxBeginner
	queries *sqlc.Queries
}

func NewService(db TxBeginner, queries *sqlc.Queries) *Service {
	if db == nil {
		panic("providerledger: transaction beginner is required")
	}
	if queries == nil {
		panic("providerledger: queries is required")
	}
	return &Service{db: db, queries: queries}
}

// DebitUsageWithQueries 在调用方结算事务中扣减 Provider 余额并写入消费流水。
func (s *Service) DebitUsageWithQueries(ctx context.Context, queries *sqlc.Queries, params UsageDebitParams) (Entry, error) {
	if queries == nil {
		panic("providerledger: transaction queries are required")
	}
	if err := validateUsage(params); err != nil {
		return Entry{}, err
	}
	return s.debitWithQueries(ctx, queries, EntryTypeUsageDebit, params.ProviderID, params.Amount, params.Currency, params.IdempotencyKey, params.Reason, &source{
		requestRecordID:  params.RequestRecordID,
		requestAttemptID: params.RequestAttemptID,
		costSnapshotID:   params.CostSnapshotID,
		channelID:        params.ChannelID,
		requestID:        params.RequestID,
		channelName:      params.ChannelName,
		upstreamModel:    params.UpstreamModel,
		usageSource:      params.UsageSource,
	})
}

// AdjustCredit 由 Admin 手工增加 Provider 余额。
func (s *Service) AdjustCredit(ctx context.Context, params AdjustParams) (Entry, error) {
	return s.adjust(ctx, params, EntryTypeAdjustmentCredit)
}

// AdjustDebit 由 Admin 手工扣减 Provider 余额，允许余额变为负数。
func (s *Service) AdjustDebit(ctx context.Context, params AdjustParams) (Entry, error) {
	return s.adjust(ctx, params, EntryTypeAdjustmentDebit)
}

// SetTargetBalance 在余额行锁内计算差额，再通过正常账本流水达到目标值。
func (s *Service) SetTargetBalance(ctx context.Context, params TargetParams) (Entry, error) {
	if err := validateTarget(params); err != nil {
		return Entry{}, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Entry{}, storeFailed(err, "begin provider target balance transaction")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	txQueries := s.queries.WithTx(tx)
	if err := lockIdempotency(ctx, txQueries, params.IdempotencyKey); err != nil {
		return Entry{}, err
	}
	if existing, err := txQueries.GetProviderLedgerEntryByIdempotencyKey(ctx, params.IdempotencyKey); err == nil {
		if existing.ProviderID != params.ProviderID || existing.Currency != params.Currency ||
			!sameNumeric(existing.BalanceAfter, params.TargetBalance) || existing.Reason != params.Reason {
			return Entry{}, failure.New(failure.CodeLedgerIdempotencyConflict, failure.WithMessage("provider target balance idempotency key conflict"))
		}
		return entryFromSQLC(existing), nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return Entry{}, storeFailed(err, "lookup provider target balance idempotency key")
	}
	if err := txQueries.EnsureProviderBalance(ctx, sqlc.EnsureProviderBalanceParams{ProviderID: params.ProviderID, Currency: params.Currency}); err != nil {
		return Entry{}, storeFailed(err, "ensure provider balance")
	}
	before, err := txQueries.GetProviderBalanceForUpdate(ctx, sqlc.GetProviderBalanceForUpdateParams{ProviderID: params.ProviderID, Currency: params.Currency})
	if err != nil {
		return Entry{}, storeFailed(err, "lock provider balance")
	}
	delta := new(big.Rat).Sub(numericRat(params.TargetBalance), numericRat(before.Balance))
	if delta.Sign() == 0 {
		return Entry{}, failure.New(failure.CodeAdminInvalidArgument, failure.WithMessage("target balance is the same as current balance"))
	}
	amount := ratToNumeric(new(big.Rat).Abs(delta), 10)
	entryType := EntryTypeAdjustmentCredit
	var after sqlc.ProviderBalance
	if delta.Sign() > 0 {
		after, err = txQueries.AddProviderBalance(ctx, sqlc.AddProviderBalanceParams{Amount: amount, ProviderID: params.ProviderID, Currency: params.Currency})
	} else {
		entryType = EntryTypeAdjustmentDebit
		after, err = txQueries.SubtractProviderBalance(ctx, sqlc.SubtractProviderBalanceParams{Amount: amount, ProviderID: params.ProviderID, Currency: params.Currency})
	}
	if err != nil {
		return Entry{}, storeFailed(err, "apply provider target balance")
	}
	created, err := txQueries.CreateProviderLedgerEntry(ctx, sqlc.CreateProviderLedgerEntryParams{
		ProviderID: params.ProviderID, EntryType: entryType, Amount: amount, Currency: params.Currency,
		BalanceBefore: before.Balance, BalanceAfter: after.Balance, IdempotencyKey: params.IdempotencyKey, Reason: params.Reason,
	})
	if err != nil {
		return Entry{}, storeFailed(err, "create provider target adjustment ledger entry")
	}
	if err := tx.Commit(ctx); err != nil {
		return Entry{}, storeFailed(err, "commit provider target balance transaction")
	}
	return entryFromSQLC(created), nil
}

func (s *Service) adjust(ctx context.Context, params AdjustParams, entryType string) (Entry, error) {
	if err := validateAdjustment(params); err != nil {
		return Entry{}, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Entry{}, storeFailed(err, "begin provider ledger transaction")
	}
	defer func() { _ = tx.Rollback(ctx) }()

	txQueries := s.queries.WithTx(tx)
	var entry Entry
	if entryType == EntryTypeAdjustmentCredit {
		entry, err = s.creditWithQueries(ctx, txQueries, entryType, params.ProviderID, params.Amount, params.Currency, params.IdempotencyKey, params.Reason)
	} else {
		entry, err = s.debitWithQueries(ctx, txQueries, entryType, params.ProviderID, params.Amount, params.Currency, params.IdempotencyKey, params.Reason, nil)
	}
	if err != nil {
		return Entry{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Entry{}, storeFailed(err, "commit provider ledger transaction")
	}
	return entry, nil
}

func (s *Service) creditWithQueries(ctx context.Context, queries *sqlc.Queries, entryType string, providerID int64, amount pgtype.Numeric, currency, idempotencyKey, reason string) (Entry, error) {
	if err := lockIdempotency(ctx, queries, idempotencyKey); err != nil {
		return Entry{}, err
	}
	if existing, err := queries.GetProviderLedgerEntryByIdempotencyKey(ctx, idempotencyKey); err == nil {
		return idempotentEntry(existing, providerID, entryType, amount, currency, reason, nil)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return Entry{}, storeFailed(err, "lookup provider ledger idempotency key")
	}

	if err := queries.EnsureProviderBalance(ctx, sqlc.EnsureProviderBalanceParams{ProviderID: providerID, Currency: currency}); err != nil {
		return Entry{}, storeFailed(err, "ensure provider balance")
	}
	before, err := queries.GetProviderBalanceForUpdate(ctx, sqlc.GetProviderBalanceForUpdateParams{ProviderID: providerID, Currency: currency})
	if err != nil {
		return Entry{}, storeFailed(err, "lock provider balance")
	}
	after, err := queries.AddProviderBalance(ctx, sqlc.AddProviderBalanceParams{Amount: amount, ProviderID: providerID, Currency: currency})
	if err != nil {
		return Entry{}, storeFailed(err, "add provider balance")
	}
	created, err := queries.CreateProviderLedgerEntry(ctx, sqlc.CreateProviderLedgerEntryParams{
		ProviderID: providerID, EntryType: entryType, Amount: amount, Currency: currency,
		BalanceBefore: before.Balance, BalanceAfter: after.Balance, IdempotencyKey: idempotencyKey, Reason: reason,
	})
	if err != nil {
		return Entry{}, storeFailed(err, "create provider credit ledger entry")
	}
	return entryFromSQLC(created), nil
}

func (s *Service) debitWithQueries(ctx context.Context, queries *sqlc.Queries, entryType string, providerID int64, amount pgtype.Numeric, currency, idempotencyKey, reason string, src *source) (Entry, error) {
	if err := lockIdempotency(ctx, queries, idempotencyKey); err != nil {
		return Entry{}, err
	}
	if existing, err := queries.GetProviderLedgerEntryByIdempotencyKey(ctx, idempotencyKey); err == nil {
		return idempotentEntry(existing, providerID, entryType, amount, currency, reason, src)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return Entry{}, storeFailed(err, "lookup provider ledger idempotency key")
	}

	if err := queries.EnsureProviderBalance(ctx, sqlc.EnsureProviderBalanceParams{ProviderID: providerID, Currency: currency}); err != nil {
		return Entry{}, storeFailed(err, "ensure provider balance")
	}
	before, err := queries.GetProviderBalanceForUpdate(ctx, sqlc.GetProviderBalanceForUpdateParams{ProviderID: providerID, Currency: currency})
	if err != nil {
		return Entry{}, storeFailed(err, "lock provider balance")
	}
	after, err := queries.SubtractProviderBalance(ctx, sqlc.SubtractProviderBalanceParams{Amount: amount, ProviderID: providerID, Currency: currency})
	if err != nil {
		return Entry{}, storeFailed(err, "subtract provider balance")
	}
	arg := sqlc.CreateProviderLedgerEntryParams{
		ProviderID: providerID, EntryType: entryType, Amount: amount, Currency: currency,
		BalanceBefore: before.Balance, BalanceAfter: after.Balance, IdempotencyKey: idempotencyKey, Reason: reason,
	}
	if src != nil {
		arg.RequestRecordID = pgtype.Int8{Int64: src.requestRecordID, Valid: true}
		arg.RequestAttemptID = pgtype.Int8{Int64: src.requestAttemptID, Valid: true}
		arg.CostSnapshotID = pgtype.Int8{Int64: src.costSnapshotID, Valid: true}
		arg.ChannelID = pgtype.Int8{Int64: src.channelID, Valid: true}
		arg.RequestID = pgtype.Text{String: src.requestID, Valid: true}
		arg.ChannelName = pgtype.Text{String: src.channelName, Valid: true}
		arg.UpstreamModel = pgtype.Text{String: src.upstreamModel, Valid: true}
		arg.UsageSource = pgtype.Text{String: string(src.usageSource), Valid: true}
	}
	created, err := queries.CreateProviderLedgerEntry(ctx, arg)
	if err != nil {
		return Entry{}, storeFailed(err, "create provider debit ledger entry")
	}
	return entryFromSQLC(created), nil
}

func (s *Service) debitProbeWithQueries(ctx context.Context, queries *sqlc.Queries, providerID, probeRecordID int64, usageSource usage.Source, amount pgtype.Numeric, currency, idempotencyKey, reason string) (Entry, error) {
	if err := lockIdempotency(ctx, queries, idempotencyKey); err != nil {
		return Entry{}, err
	}
	if existing, err := queries.GetProviderLedgerEntryByIdempotencyKey(ctx, idempotencyKey); err == nil {
		if existing.ProviderID != providerID || existing.EntryType != EntryTypeProbeDebit || existing.Currency != currency || !sameNumeric(existing.Amount, amount) || !existing.ProviderProbeRecordID.Valid || existing.ProviderProbeRecordID.Int64 != probeRecordID || existing.UsageSource.String != string(usageSource) || existing.Reason != reason {
			return Entry{}, failure.New(failure.CodeLedgerIdempotencyConflict, failure.WithMessage("provider probe ledger idempotency key conflict"))
		}
		return entryFromSQLC(existing), nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return Entry{}, storeFailed(err, "lookup provider probe ledger idempotency key")
	}
	if err := queries.EnsureProviderBalance(ctx, sqlc.EnsureProviderBalanceParams{ProviderID: providerID, Currency: currency}); err != nil {
		return Entry{}, storeFailed(err, "ensure provider balance")
	}
	before, err := queries.GetProviderBalanceForUpdate(ctx, sqlc.GetProviderBalanceForUpdateParams{ProviderID: providerID, Currency: currency})
	if err != nil {
		return Entry{}, storeFailed(err, "lock provider balance")
	}
	after, err := queries.SubtractProviderBalance(ctx, sqlc.SubtractProviderBalanceParams{Amount: amount, ProviderID: providerID, Currency: currency})
	if err != nil {
		return Entry{}, storeFailed(err, "subtract provider balance for probe")
	}
	created, err := queries.CreateProviderLedgerEntry(ctx, sqlc.CreateProviderLedgerEntryParams{
		ProviderID: providerID, ProviderProbeRecordID: pgtype.Int8{Int64: probeRecordID, Valid: true},
		UsageSource: pgtype.Text{String: string(usageSource), Valid: true},
		EntryType:   EntryTypeProbeDebit, Amount: amount, Currency: currency,
		BalanceBefore: before.Balance, BalanceAfter: after.Balance, IdempotencyKey: idempotencyKey, Reason: reason,
	})
	if err != nil {
		return Entry{}, storeFailed(err, "create provider probe debit ledger entry")
	}
	return entryFromSQLC(created), nil
}

type source struct {
	requestRecordID, requestAttemptID, costSnapshotID, channelID int64
	requestID, channelName, upstreamModel                        string
	usageSource                                                  usage.Source
}

func validateUsage(p UsageDebitParams) error {
	if p.ProviderID <= 0 || p.RequestRecordID <= 0 || p.RequestAttemptID <= 0 || p.CostSnapshotID <= 0 || p.ChannelID <= 0 {
		return invalidArgument("usage source is incomplete")
	}
	if strings.TrimSpace(p.RequestID) == "" || strings.TrimSpace(p.ChannelName) == "" || strings.TrimSpace(p.UpstreamModel) == "" {
		return invalidArgument("usage source labels are incomplete")
	}
	if !p.UsageSource.Valid() {
		return invalidArgument("usage source is invalid")
	}
	return validateCommon(p.Amount, p.Currency, p.IdempotencyKey, p.Reason)
}

func validateAdjustment(p AdjustParams) error {
	if p.ProviderID <= 0 {
		return invalidArgument("provider_id must be greater than zero")
	}
	return validateCommon(p.Amount, p.Currency, p.IdempotencyKey, p.Reason)
}

func validateTarget(p TargetParams) error {
	if p.ProviderID <= 0 {
		return invalidArgument("provider_id must be greater than zero")
	}
	if !p.TargetBalance.Valid || p.TargetBalance.NaN || p.TargetBalance.InfinityModifier != pgtype.Finite || p.TargetBalance.Int == nil {
		return invalidArgument("target balance must be a finite decimal")
	}
	if strings.TrimSpace(p.Currency) == "" || strings.TrimSpace(p.IdempotencyKey) == "" || strings.TrimSpace(p.Reason) == "" {
		return invalidArgument("provider target balance currency, idempotency key and reason are required")
	}
	return nil
}

func validateCommon(amount pgtype.Numeric, currency, idempotencyKey, reason string) error {
	if !isPositive(amount) {
		return failure.New(failure.CodeLedgerInvalidAmount, failure.WithMessage("provider ledger amount must be greater than zero"))
	}
	if strings.TrimSpace(currency) == "" || strings.TrimSpace(idempotencyKey) == "" || strings.TrimSpace(reason) == "" {
		return invalidArgument("provider ledger currency, idempotency key and reason are required")
	}
	return nil
}

func lockIdempotency(ctx context.Context, queries *sqlc.Queries, key string) error {
	if err := queries.LockProviderLedgerIdempotencyKey(ctx, key); err != nil {
		return storeFailed(err, "lock provider ledger idempotency key")
	}
	return nil
}

func idempotentEntry(row sqlc.ProviderLedgerEntry, providerID int64, entryType string, amount pgtype.Numeric, currency, reason string, src *source) (Entry, error) {
	if row.ProviderID != providerID || row.EntryType != entryType || row.Currency != currency || !sameNumeric(row.Amount, amount) || row.Reason != reason {
		return Entry{}, failure.New(failure.CodeLedgerIdempotencyConflict, failure.WithMessage("provider ledger idempotency key conflict"))
	}
	if src != nil && (row.RequestRecordID.Int64 != src.requestRecordID || row.RequestAttemptID.Int64 != src.requestAttemptID || row.CostSnapshotID.Int64 != src.costSnapshotID || row.ChannelID.Int64 != src.channelID || row.RequestID.String != src.requestID || row.ChannelName.String != src.channelName || row.UpstreamModel.String != src.upstreamModel || row.UsageSource.String != string(src.usageSource)) {
		return Entry{}, failure.New(failure.CodeLedgerIdempotencyConflict, failure.WithMessage("provider ledger source conflict"))
	}
	return entryFromSQLC(row), nil
}

func entryFromSQLC(row sqlc.ProviderLedgerEntry) Entry {
	return Entry{
		ID: row.ID, ProviderID: row.ProviderID,
		RequestRecordID: optionalInt64(row.RequestRecordID), RequestAttemptID: optionalInt64(row.RequestAttemptID),
		CostSnapshotID: optionalInt64(row.CostSnapshotID), ChannelID: optionalInt64(row.ChannelID),
		RequestID: optionalString(row.RequestID), ChannelName: optionalString(row.ChannelName), UpstreamModel: optionalString(row.UpstreamModel),
		ProviderProbeRecordID: optionalInt64(row.ProviderProbeRecordID),
		UsageSource:           usage.Source(row.UsageSource.String),
		EntryType:             row.EntryType, Amount: row.Amount, Currency: row.Currency, BalanceBefore: row.BalanceBefore, BalanceAfter: row.BalanceAfter,
		IdempotencyKey: row.IdempotencyKey, Reason: row.Reason,
	}
}

func optionalInt64(v pgtype.Int8) *int64 {
	if !v.Valid {
		return nil
	}
	n := v.Int64
	return &n
}
func optionalString(v pgtype.Text) *string {
	if !v.Valid {
		return nil
	}
	s := v.String
	return &s
}

func isPositive(v pgtype.Numeric) bool {
	if !v.Valid || v.NaN || v.InfinityModifier != pgtype.Finite || v.Int == nil {
		return false
	}
	r := new(big.Rat).SetInt(v.Int)
	if v.Exp > 0 {
		r.Mul(r, new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(v.Exp)), nil)))
	}
	if v.Exp < 0 {
		r.Quo(r, new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(-v.Exp)), nil)))
	}
	return r.Sign() > 0
}

func sameNumeric(left, right pgtype.Numeric) bool {
	if !left.Valid || !right.Valid || left.Int == nil || right.Int == nil {
		return false
	}
	return numericRat(left).Cmp(numericRat(right)) == 0
}

func numericRat(v pgtype.Numeric) *big.Rat {
	r := new(big.Rat).SetInt(v.Int)
	if v.Exp > 0 {
		r.Mul(r, new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(v.Exp)), nil)))
	}
	if v.Exp < 0 {
		r.Quo(r, new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(-v.Exp)), nil)))
	}
	return r
}

func ratToNumeric(v *big.Rat, scale int) pgtype.Numeric {
	var out pgtype.Numeric
	_ = out.Scan(v.FloatString(scale))
	return out
}

func invalidArgument(message string) error {
	return failure.New(failure.CodeAdminInvalidArgument, failure.WithMessage(message))
}
func storeFailed(err error, message string) error {
	return failure.Wrap(failure.CodeLedgerStoreFailed, err, failure.WithMessage(message))
}
