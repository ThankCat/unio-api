package ledger

import (
	"errors"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	// ErrInsufficientBalance 表示用户余额不足，不能完成扣费。
	ErrInsufficientBalance = errors.New("ledger: insufficient balance")

	// ErrIdempotencyConflict 表示同一个幂等键被不同账本参数复用。
	ErrIdempotencyConflict = errors.New("ledger: idempotency key conflict")

	// ErrInvalidAmount 表示账本金额参数非法。
	ErrInvalidAmount = errors.New("ledger: invalid amount")

	// ErrReservationNotFound 表示请求没有可结算的余额预授权记录
	ErrReservationNotFound = errors.New("ledger: reservation not found")
)

// isUniqueViolation 判断数据库错误是否是唯一约束冲突。
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func ledgerFailure(code failure.Code, cause error, message string) error {
	return failure.Wrap(code, cause, failure.WithMessage(message))
}
