// Package cdkey implements the authenticated Console CDKEY redemption flow.
package cdkey

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	corecdkey "github.com/ThankCat/unio-gateway/internal/core/cdkey"
	"github.com/ThankCat/unio-gateway/internal/core/ledger"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/admin/opsutil"
	consoleservice "github.com/ThankCat/unio-gateway/internal/service/console"
)

const (
	CodeCDKeyInvalid   = "cdkey_invalid"
	CodeCDKeyRedeemed  = "cdkey_already_redeemed"
	CodeCDKeyRevoked   = "cdkey_revoked"
	CodeCDKeyConflict  = "cdkey_redemption_conflict"
	CodeCDKeyRateLimit = "cdkey_rate_limited"
)

// TxBeginner is the transaction capability required by redemption.
type TxBeginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

// Store is satisfied by generated sqlc queries.
type Store interface {
	GetCDKeyForRedemptionByHashForUpdate(context.Context, string) (sqlc.GetCDKeyForRedemptionByHashForUpdateRow, error)
	GetCDKeyRedemptionByCDKeyID(context.Context, int64) (sqlc.GetCDKeyRedemptionByCDKeyIDRow, error)
	CreateCDKeyRedemption(context.Context, sqlc.CreateCDKeyRedemptionParams) (sqlc.CdkeyRedemption, error)
	UpdateConsoleCDKeyRedeemed(context.Context, sqlc.UpdateConsoleCDKeyRedeemedParams) (int64, error)
}

// Ledger is the transaction-aware credit capability. The concrete ledger
// service validates entry type and performs balance + ledger writes atomically.
type Ledger interface {
	CreditWithQueriesType(context.Context, *sqlc.Queries, ledger.CreditParams, ledger.EntryType) (ledger.Entry, error)
}

type Service struct {
	db      TxBeginner
	store   Store
	ledger  Ledger
	limiter *RateLimiter
}

func NewService(db TxBeginner, store Store, ledgerSvc Ledger) *Service {
	if db == nil || store == nil || ledgerSvc == nil {
		panic("console cdkey service requires db, store and ledger")
	}
	return &Service{db: db, store: store, ledger: ledgerSvc}
}

// WithRateLimiter enables the optional Redis-backed failed-attempt limiter.
// Keeping this as an option preserves the service's small constructor for
// unit tests and for read-only tooling that does not have Redis.
func (s *Service) WithRateLimiter(limiter *RateLimiter) *Service {
	s.limiter = limiter
	return s
}

// Redemption is the safe response returned after a successful exchange.
type Redemption struct {
	ID            int64
	CDKeyID       int64
	Amount        string
	Currency      string
	LedgerEntryID int64
	BalanceAfter  string
	RedeemedAt    time.Time
}

// Redeem normalizes and exchanges one key. A redeemed key submitted again by
// the same user returns the original fact; a different user gets a conflict.
func (s *Service) Redeem(ctx context.Context, userID int64, rawCode string) (Redemption, *consoleservice.Error) {
	return s.redeemWithIP(ctx, userID, rawCode, "")
}

// RedeemWithIP is the HTTP-facing variant that includes the trusted source IP
// in failed-attempt limiting. The plain Redeem method remains for callers that
// do not have request-network context.
func (s *Service) RedeemWithIP(ctx context.Context, userID int64, rawCode, ip string) (Redemption, *consoleservice.Error) {
	return s.redeemWithIP(ctx, userID, rawCode, ip)
}

func (s *Service) redeemWithIP(ctx context.Context, userID int64, rawCode, ip string) (Redemption, *consoleservice.Error) {
	if userID <= 0 {
		return Redemption{}, consoleservice.InvalidArgument("code", "The current session is invalid.")
	}
	if s.limiter != nil {
		if limitErr := s.limiter.Check(ctx, userID, ip); limitErr != nil {
			return Redemption{}, limitErr
		}
	}
	result, serviceErr := s.redeemCore(ctx, userID, rawCode)
	if s.limiter == nil {
		return result, serviceErr
	}
	if serviceErr == nil {
		// The balance transaction has already committed. A reset failure must
		// not turn a successful financial operation into an apparent failure.
		_ = s.limiter.Reset(ctx, userID, ip)
		return result, nil
	}
	if limitErr := s.limiter.RecordFailure(ctx, userID, ip); limitErr != nil && limitErr.Code == CodeCDKeyRateLimit {
		return Redemption{}, limitErr
	}
	return result, serviceErr
}

func (s *Service) redeemCore(ctx context.Context, userID int64, rawCode string) (Redemption, *consoleservice.Error) {
	canonical, err := corecdkey.Normalize(rawCode)
	if err != nil {
		return Redemption{}, cdkeyError(CodeCDKeyInvalid, "The CDKEY is invalid.", httpStatusBadRequest, err)
	}
	hash := corecdkey.Hash(canonical)
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Redemption{}, consoleservice.RequestUnavailable("begin CDKEY redemption", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q, ok := s.store.(*sqlc.Queries)
	if !ok {
		return Redemption{}, consoleservice.RequestUnavailable("prepare CDKEY redemption transaction", errors.New("CDKEY store is not transaction capable"))
	}
	txq := q.WithTx(tx)
	key, err := txq.GetCDKeyForRedemptionByHashForUpdate(ctx, hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return Redemption{}, cdkeyError(CodeCDKeyInvalid, "The CDKEY is invalid.", httpStatusBadRequest, err)
	}
	if err != nil {
		return Redemption{}, consoleservice.RequestUnavailable("lookup CDKEY", err)
	}

	if key.Status == corecdkey.StatusRedeemed {
		row, redemptionErr := txq.GetCDKeyRedemptionByCDKeyID(ctx, key.ID)
		if errors.Is(redemptionErr, pgx.ErrNoRows) {
			return Redemption{}, consoleservice.RequestUnavailable("read CDKEY redemption", redemptionErr)
		}
		if redemptionErr != nil {
			return Redemption{}, consoleservice.RequestUnavailable("read CDKEY redemption", redemptionErr)
		}
		if row.UserID != userID {
			return Redemption{}, cdkeyError(CodeCDKeyRedeemed, "The CDKEY has already been redeemed.", httpStatusConflict, nil)
		}
		result := redemptionFromRow(row)
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return Redemption{}, consoleservice.RequestUnavailable("commit CDKEY redemption lookup", commitErr)
		}
		return result, nil
	}
	if key.Status == corecdkey.StatusRevoked {
		return Redemption{}, cdkeyError(CodeCDKeyRevoked, "The CDKEY is no longer available.", httpStatusConflict, nil)
	}
	if key.Status != corecdkey.StatusUnused {
		return Redemption{}, cdkeyError(CodeCDKeyInvalid, "The CDKEY is invalid.", httpStatusBadRequest, nil)
	}

	// The CDKEY row is locked above. The deterministic idempotency key is tied
	// to that row, so retries cannot create a second balance entry.
	idempotencyKey := "cdkey:" + formatInt64(key.ID)
	entry, err := s.ledger.CreditWithQueriesType(ctx, txq, ledger.CreditParams{
		UserID: userID, Amount: key.Amount, Currency: key.Currency,
		IdempotencyKey: idempotencyKey, Reason: "CDKEY redemption",
	}, ledger.EntryTypeCDKeyCredit)
	if err != nil {
		return Redemption{}, mapLedgerError(err)
	}
	now := pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	created, err := txq.CreateCDKeyRedemption(ctx, sqlc.CreateCDKeyRedemptionParams{
		CdkeyID: key.ID, UserID: userID, Amount: key.Amount, Currency: key.Currency,
		LedgerEntryID: entry.ID, IdempotencyKey: idempotencyKey, RedeemedAt: now,
	})
	if err != nil {
		return Redemption{}, consoleservice.RequestUnavailable("record CDKEY redemption", err)
	}
	updated, err := txq.UpdateConsoleCDKeyRedeemed(ctx, sqlc.UpdateConsoleCDKeyRedeemedParams{ID: key.ID, RedeemedAt: now})
	if err != nil {
		return Redemption{}, consoleservice.RequestUnavailable("mark CDKEY redeemed", err)
	}
	if updated != 1 {
		return Redemption{}, cdkeyError(CodeCDKeyConflict, "The CDKEY redemption could not be completed.", httpStatusConflict, errors.New("CDKEY state changed before redemption was finalized"))
	}
	if err := tx.Commit(ctx); err != nil {
		return Redemption{}, consoleservice.RequestUnavailable("commit CDKEY redemption", err)
	}
	return Redemption{ID: created.ID, CDKeyID: created.CdkeyID, Amount: opsutil.NumericString(created.Amount), Currency: created.Currency, LedgerEntryID: created.LedgerEntryID, BalanceAfter: opsutil.NumericString(entry.BalanceAfter), RedeemedAt: created.RedeemedAt.Time}, nil
}

func redemptionFromRow(row sqlc.GetCDKeyRedemptionByCDKeyIDRow) Redemption {
	return Redemption{ID: row.ID, CDKeyID: row.CdkeyID, Amount: opsutil.NumericString(row.Amount), Currency: row.Currency, LedgerEntryID: row.LedgerEntryID, BalanceAfter: opsutil.NumericString(row.BalanceAfter), RedeemedAt: row.RedeemedAt.Time}
}

func mapLedgerError(err error) *consoleservice.Error {
	if failure.CodeOf(err) == failure.CodeLedgerIdempotencyConflict {
		return cdkeyError(CodeCDKeyConflict, "The CDKEY redemption could not be completed.", httpStatusConflict, err)
	}
	return consoleservice.RequestUnavailable("credit CDKEY wallet", err)
}

func cdkeyError(code, message string, status int, cause error) *consoleservice.Error {
	return &consoleservice.Error{Code: code, Message: message, Status: status, Cause: cause}
}

func formatInt64(value int64) string {
	// Avoid fmt in this hot path and keep the idempotency key allocation small.
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	var buf [24]byte
	i := len(buf)
	for value > 0 {
		i--
		buf[i] = byte('0' + value%10)
		value /= 10
	}
	if negative {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func normalizeCodeForTest(raw string) string {
	return strings.ToUpper(strings.TrimSpace(raw))
}

const (
	httpStatusBadRequest = 400
	httpStatusConflict   = 409
)

var _ Store = (*sqlc.Queries)(nil)
var _ Ledger = (*ledger.Service)(nil)
