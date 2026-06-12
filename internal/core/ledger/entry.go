package ledger

import (
	"context"
	"errors"

	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// Entry 表示 ledger service 返回给调用方的账本流水。
type Entry struct {
	ID              int64
	UserID          int64
	RequestRecordID *int64
	EntryType       EntryType
	Amount          pgtype.Numeric
	Currency        string
	BalanceBefore   pgtype.Numeric
	BalanceAfter    pgtype.Numeric
	IdempotencyKey  string
	Reason          string
}

// CreditParams 表示加款类账本操作参数。
type CreditParams struct {
	UserID          int64
	RequestRecordID *int64
	Amount          pgtype.Numeric
	Currency        string
	IdempotencyKey  string
	Reason          string
}

// DebitParams 表示扣款类账本操作参数。
type DebitParams struct {
	UserID          int64
	RequestRecordID *int64
	Amount          pgtype.Numeric
	Currency        string
	IdempotencyKey  string
	Reason          string
}

// AdjustParams 表示 admin 手工调额参数（M7）。
// 调额是用户级动作，不挂请求；request_record_id 恒为 NULL，靠 IdempotencyKey 幂等。
type AdjustParams struct {
	UserID         int64
	Amount         pgtype.Numeric
	Currency       string
	IdempotencyKey string
	Reason         string
}

// Credit 增加用户余额，并在同一个事务里写入 credit 账本流水。
func (s *Service) Credit(ctx context.Context, params CreditParams) (Entry, error) {
	return s.creditWithType(ctx, params, EntryTypeCredit)
}

// AdjustCredit 由 admin 手工给用户加款，写入 adjustment_credit 账本流水（M7）。
func (s *Service) AdjustCredit(ctx context.Context, params AdjustParams) (Entry, error) {
	return s.creditWithType(ctx, CreditParams{
		UserID:         params.UserID,
		Amount:         params.Amount,
		Currency:       params.Currency,
		IdempotencyKey: params.IdempotencyKey,
		Reason:         params.Reason,
	}, EntryTypeAdjustmentCredit)
}

// creditWithType 执行加款类账本写入；entryType 决定流水类型（credit / adjustment_credit）。
func (s *Service) creditWithType(ctx context.Context, params CreditParams, entryType EntryType) (Entry, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Entry{}, ledgerFailure(failure.CodeLedgerStoreFailed, err, "begin ledger transaction")
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	txQueries := s.queries.WithTx(tx)

	// 幂等命中表示这笔加款已经完成，直接返回已有流水，避免重复加余额。
	existing, err := txQueries.GetLedgerEntryByIdempotencyKey(ctx, params.IdempotencyKey)
	if err == nil {
		if err := ensureIdempotentEntryMatches(existing, params.UserID, params.RequestRecordID, entryType, params.Amount, params.Currency); err != nil {
			return Entry{}, err
		}

		return entryFromSQLC(existing), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Entry{}, ledgerFailure(failure.CodeLedgerStoreFailed, err, "lookup ledger idempotency key")
	}

	// Credit 可以为新用户创建 0 余额行，再在同一事务中加款。
	if err := txQueries.EnsureUserBalance(ctx, sqlc.EnsureUserBalanceParams{
		UserID:   params.UserID,
		Currency: params.Currency,
	}); err != nil {
		return Entry{}, ledgerFailure(failure.CodeLedgerStoreFailed, err, "ensure user balance")
	}

	// 锁定用户余额行，确保并发充值/扣费不会基于同一个旧余额计算。
	before, err := txQueries.GetUserBalanceForUpdate(ctx, sqlc.GetUserBalanceForUpdateParams{
		UserID:   params.UserID,
		Currency: params.Currency,
	})
	if err != nil {
		return Entry{}, ledgerFailure(failure.CodeLedgerStoreFailed, err, "lock user balance")
	}

	// 让 PostgreSQL 执行 NUMERIC 加法，避免在 Go 中用 float 或手写 decimal 计算金额。
	after, err := txQueries.AddUserBalance(ctx, sqlc.AddUserBalanceParams{
		Amount:   params.Amount,
		UserID:   params.UserID,
		Currency: params.Currency,
	})
	if err != nil {
		return Entry{}, ledgerFailure(failure.CodeLedgerStoreFailed, err, "add user balance")
	}

	// 写入账本事实；balance_before/after 必须和余额更新结果一致。
	created, err := txQueries.CreateLedgerEntry(ctx, sqlc.CreateLedgerEntryParams{
		UserID:          params.UserID,
		RequestRecordID: int64PtrToPgtypeInt8(params.RequestRecordID),
		EntryType:       string(entryType),
		Amount:          params.Amount,
		Currency:        params.Currency,
		BalanceBefore:   before.Balance,
		BalanceAfter:    after.Balance,
		IdempotencyKey:  params.IdempotencyKey,
		Reason:          params.Reason,
	})
	if err != nil {
		if isUniqueViolation(err) {
			return s.resolveIdempotentCreateConflict(ctx, tx, params.IdempotencyKey, params.UserID, params.RequestRecordID, entryType, params.Amount, params.Currency)
		}

		return Entry{}, ledgerFailure(failure.CodeLedgerStoreFailed, err, "create ledger entry")
	}

	if err := tx.Commit(ctx); err != nil {
		return Entry{}, ledgerFailure(failure.CodeLedgerStoreFailed, err, "commit ledger transaction")
	}

	return entryFromSQLC(created), nil
}

// Debit 减少用户余额，并在同一个事务里写入 debit 账本流水。
func (s *Service) Debit(ctx context.Context, params DebitParams) (Entry, error) {
	return s.debitTx(ctx, params, EntryTypeDebit)
}

// AdjustDebit 由 admin 手工给用户扣款，写入 adjustment_debit 账本流水（M7）。
// 余额不足时返回 CodeLedgerInsufficientBalance（不会把余额扣成负数）。
func (s *Service) AdjustDebit(ctx context.Context, params AdjustParams) (Entry, error) {
	return s.debitTx(ctx, DebitParams{
		UserID:         params.UserID,
		Amount:         params.Amount,
		Currency:       params.Currency,
		IdempotencyKey: params.IdempotencyKey,
		Reason:         params.Reason,
	}, EntryTypeAdjustmentDebit)
}

// debitTx 执行扣款类账本写入；entryType 决定流水类型（debit / adjustment_debit）。
func (s *Service) debitTx(ctx context.Context, params DebitParams, entryType EntryType) (Entry, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Entry{}, ledgerFailure(failure.CodeLedgerStoreFailed, err, "begin ledger transaction")
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	txQueries := s.queries.WithTx(tx)

	entry, err := s.debitWithQueriesType(ctx, txQueries, params, entryType)
	if err != nil {
		if isUniqueViolation(err) {
			return s.resolveIdempotentCreateConflict(ctx, tx, params.IdempotencyKey, params.UserID, params.RequestRecordID, entryType, params.Amount, params.Currency)
		}

		return Entry{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return Entry{}, ledgerFailure(failure.CodeLedgerStoreFailed, err, "commit ledger transaction")
	}

	return entry, nil
}

// DebitWithQueries 使用调用方传入的事务内 queries 执行扣款。
// 调用方必须传入 queries.WithTx(tx)，确保 advisory lock、余额变化和 ledger entry 同事务提交。
func (s *Service) DebitWithQueries(ctx context.Context, queries *sqlc.Queries, params DebitParams) (Entry, error) {
	return s.debitWithQueriesType(ctx, queries, params, EntryTypeDebit)
}

func (s *Service) debitWithQueriesType(ctx context.Context, queries *sqlc.Queries, params DebitParams, entryType EntryType) (Entry, error) {
	if err := lockLedgerEntryIdempotencyKey(ctx, queries, params.IdempotencyKey); err != nil {
		return Entry{}, err
	}

	// 幂等命中表示这笔扣费已经完成，直接返回已有流水，避免重复扣余额。
	existing, err := queries.GetLedgerEntryByIdempotencyKey(ctx, params.IdempotencyKey)
	if err == nil {
		if err := ensureIdempotentEntryMatches(existing, params.UserID, params.RequestRecordID, entryType, params.Amount, params.Currency); err != nil {
			return Entry{}, err
		}

		return entryFromSQLC(existing), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Entry{}, ledgerFailure(failure.CodeLedgerStoreFailed, err, "lookup ledger idempotency key")
	}

	// 扣费不能自动创建余额行；不存在余额行时应视为余额不足。
	before, err := queries.GetUserBalanceForUpdate(ctx, sqlc.GetUserBalanceForUpdateParams{
		UserID:   params.UserID,
		Currency: params.Currency,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Entry{}, ledgerFailure(failure.CodeLedgerInsufficientBalance, ErrInsufficientBalance, ErrInsufficientBalance.Error())
		}
		return Entry{}, ledgerFailure(failure.CodeLedgerStoreFailed, err, "lock user balance")
	}

	// 让 PostgreSQL 执行 NUMERIC 减法，并通过 WHERE balance >= amount 防止扣成负数。
	after, err := queries.SubtractUserBalance(ctx, sqlc.SubtractUserBalanceParams{
		Amount:   params.Amount,
		UserID:   params.UserID,
		Currency: params.Currency,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Entry{}, ledgerFailure(failure.CodeLedgerInsufficientBalance, ErrInsufficientBalance, ErrInsufficientBalance.Error())
		}
		return Entry{}, ledgerFailure(failure.CodeLedgerStoreFailed, err, "subtract user balance")
	}

	created, err := queries.CreateLedgerEntry(ctx, sqlc.CreateLedgerEntryParams{
		UserID:          params.UserID,
		RequestRecordID: int64PtrToPgtypeInt8(params.RequestRecordID),
		EntryType:       string(entryType),
		Amount:          params.Amount,
		Currency:        params.Currency,
		BalanceBefore:   before.Balance,
		BalanceAfter:    after.Balance,
		IdempotencyKey:  params.IdempotencyKey,
		Reason:          params.Reason,
	})
	if err != nil {
		return Entry{}, ledgerFailure(failure.CodeLedgerStoreFailed, err, "create ledger entry")
	}

	return entryFromSQLC(created), nil
}

// lockLedgerEntryIdempotencyKey 在当前事务内按 ledger entry 幂等键串行化写入。
// 它必须运行在事务内 queries 上；否则 PostgreSQL 会在单条语句结束后释放 advisory lock。
func lockLedgerEntryIdempotencyKey(ctx context.Context, queries *sqlc.Queries, idempotencyKey string) error {
	if err := queries.LockLedgerEntryIdempotencyKey(ctx, idempotencyKey); err != nil {
		return ledgerFailure(failure.CodeLedgerStoreFailed, err, "lock ledger idempotency key")
	}

	return nil
}

// resolveIdempotentCreateConflict 在并发请求同时创建同一个幂等键时返回已提交流水。
func (s *Service) resolveIdempotentCreateConflict(ctx context.Context, tx pgx.Tx, idempotencyKey string, userID int64, requestRecordID *int64, entryType EntryType, amount pgtype.Numeric, currency string) (Entry, error) {
	// PostgreSQL 写入出错后当前事务已不可继续使用，先回滚再查询已提交的幂等流水。
	_ = tx.Rollback(ctx)

	existing, err := s.queries.GetLedgerEntryByIdempotencyKey(ctx, idempotencyKey)
	if err != nil {
		return Entry{}, ledgerFailure(failure.CodeLedgerStoreFailed, err, "lookup committed ledger idempotency key")
	}
	if err := ensureIdempotentEntryMatches(existing, userID, requestRecordID, entryType, amount, currency); err != nil {
		return Entry{}, err
	}

	return entryFromSQLC(existing), nil
}

// entryFromSQLC 将 sqlc 账本流水转换为 ledger 领域 DTO。
func entryFromSQLC(row sqlc.LedgerEntry) Entry {
	return Entry{
		ID:              row.ID,
		UserID:          row.UserID,
		RequestRecordID: pgtypeInt8ToInt64Ptr(row.RequestRecordID),
		EntryType:       EntryType(row.EntryType),
		Amount:          row.Amount,
		Currency:        row.Currency,
		BalanceBefore:   row.BalanceBefore,
		BalanceAfter:    row.BalanceAfter,
		IdempotencyKey:  row.IdempotencyKey,
		Reason:          row.Reason,
	}
}

// ensureIdempotentEntryMatches 校验已有幂等流水是否和本次请求语义一致。
func ensureIdempotentEntryMatches(row sqlc.LedgerEntry, userID int64, requestRecordID *int64, entryType EntryType, amount pgtype.Numeric, currency string) error {
	if row.UserID != userID {
		return ledgerFailure(failure.CodeLedgerIdempotencyConflict, ErrIdempotencyConflict, ErrIdempotencyConflict.Error())
	}
	if row.EntryType != string(entryType) {
		return ledgerFailure(failure.CodeLedgerIdempotencyConflict, ErrIdempotencyConflict, ErrIdempotencyConflict.Error())
	}
	if row.Currency != currency {
		return ledgerFailure(failure.CodeLedgerIdempotencyConflict, ErrIdempotencyConflict, ErrIdempotencyConflict.Error())
	}
	if !sameOptionalInt64(row.RequestRecordID, requestRecordID) {
		return ledgerFailure(failure.CodeLedgerIdempotencyConflict, ErrIdempotencyConflict, ErrIdempotencyConflict.Error())
	}
	if !sameNumeric(row.Amount, amount) {
		return ledgerFailure(failure.CodeLedgerIdempotencyConflict, ErrIdempotencyConflict, ErrIdempotencyConflict.Error())
	}

	return nil
}
