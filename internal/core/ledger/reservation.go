package ledger

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

// Reservation 表示一次请求的余额预授权事实。
// 它记录冻结金额、最终扣费金额、释放金额，以及关联的扣费流水。
type Reservation struct {
	ID                   int64
	UserID               int64
	RequestRecordID      int64
	Currency             string
	Status               ReservationStatus
	AuthorizedAmount     pgtype.Numeric
	CapturedAmount       pgtype.Numeric
	ReleasedAmount       pgtype.Numeric
	EstimatedAmount      pgtype.Numeric
	CaptureLedgerEntryID *int64
	IdempotencyKey       string
	Reason               string

	// OverageCapturedAmount 为本次 capture 在「真实费用超过授权金额」时，从用户未冻结可用余额
	// 二次补扣的金额（实扣到清空可用余额为止）。它是内存态结算事实，不持久化在 reservation 行上：
	// 超额扣费另写独立 debit ledger entry，平台核销只承担补扣后仍不可回收的残差。
	// 调用方据此把超额实扣计入费用上限（spent_total）等累计口径。
	OverageCapturedAmount pgtype.Numeric
}

// PreAuthorizeParams 表示创建余额预授权所需参数。
type PreAuthorizeParams struct {
	UserID          int64
	RequestRecordID int64
	EstimatedAmount pgtype.Numeric
	Currency        string
	IdempotencyKey  string
	Reason          string
}

// CaptureParams 表示把预授权转换为真实扣费所需参数。
// ReservationID 可选；传入时用于校验调用方正在结算的就是这笔冻结记录。
type CaptureParams struct {
	RequestRecordID int64
	ReservationID   *int64
	ActualAmount    pgtype.Numeric
	IdempotencyKey  string
	Reason          string
}

// ReleaseParams 表示释放预授权资金所需参数。
// ReservationID 可选；传入时用于避免释放错请求的冻结记录。
type ReleaseParams struct {
	RequestRecordID int64
	ReservationID   *int64
}

// ReleaseWithBillingExceptionParams 表示释放冻结余额并记录账务异常事实所需参数。
// 它用于没有可靠 usage、不能扣用户钱，但平台可能已有成本或风险敞口的场景。
type ReleaseWithBillingExceptionParams struct {
	RequestRecordID int64
	ReservationID   *int64
	ReasonCode      string
	Reason          string
}

// PreAuthorize 为一次请求冻结用户余额，并创建 reservation 事实。
func (s *Service) PreAuthorize(ctx context.Context, params PreAuthorizeParams) (Reservation, error) {
	if !isPositiveNumeric(params.EstimatedAmount) {
		return Reservation{}, ledgerFailure(failure.CodeLedgerInvalidAmount, ErrInvalidAmount, ErrInvalidAmount.Error())
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Reservation{}, ledgerFailure(failure.CodeLedgerStoreFailed, err, "begin ledger transaction")
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	txQueries := s.queries.WithTx(tx)

	existing, err := txQueries.GetLedgerReservationByIdempotencyKey(ctx, params.IdempotencyKey)
	if err == nil {
		if err := ensureIdempotentReservationMatches(existing, params.UserID, params.RequestRecordID, params.EstimatedAmount, params.Currency); err != nil {
			return Reservation{}, err
		}
		return reservationFromSQLC(existing), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Reservation{}, ledgerFailure(failure.CodeLedgerStoreFailed, err, "lookup reservation idempotency key")
	}

	reserved, err := txQueries.ReserveAvailableUserBalance(ctx, sqlc.ReserveAvailableUserBalanceParams{
		UserID:          params.UserID,
		Currency:        params.Currency,
		EstimatedAmount: params.EstimatedAmount,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return s.resolveReservationUnavailable(ctx, tx, params)
		}
		return Reservation{}, ledgerFailure(failure.CodeLedgerStoreFailed, err, "reserve available user balance")
	}

	created, err := txQueries.CreateLedgerReservation(ctx, sqlc.CreateLedgerReservationParams{
		UserID:           params.UserID,
		RequestRecordID:  params.RequestRecordID,
		Currency:         params.Currency,
		EstimatedAmount:  params.EstimatedAmount,
		AuthorizedAmount: reserved.AuthorizedAmount,
		IdempotencyKey:   params.IdempotencyKey,
		Reason:           params.Reason,
	})
	if err != nil {
		if isUniqueViolation(err) {
			return s.resolveReservationCreateConflict(ctx, tx, params.IdempotencyKey, params.UserID, params.RequestRecordID, params.EstimatedAmount, params.Currency)
		}
		return Reservation{}, ledgerFailure(failure.CodeLedgerStoreFailed, err, "create ledger reservation")
	}

	if err := tx.Commit(ctx); err != nil {
		return Reservation{}, ledgerFailure(failure.CodeLedgerStoreFailed, err, "commit ledger transaction")
	}

	return reservationFromSQLC(created), nil
}

// Capture 将已预授权请求结算为真实扣费，并自行管理事务。
// 需要和 usage、price snapshot、request 状态同事务提交时，应使用 CaptureWithQueries。
func (s *Service) Capture(ctx context.Context, params CaptureParams) (Reservation, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Reservation{}, ledgerFailure(failure.CodeLedgerStoreFailed, err, "begin ledger transaction")
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	txQueries := s.queries.WithTx(tx)

	captured, err := s.captureWithQueries(ctx, txQueries, params)
	if err != nil {
		return Reservation{}, err
	}

	// 只有余额、流水和 reservation 终态都更新成功，才提交本次 capture。
	if err := tx.Commit(ctx); err != nil {
		return Reservation{}, ledgerFailure(failure.CodeLedgerStoreFailed, err, "commit ledger transaction")
	}

	return captured, nil
}

// CaptureWithQueries 使用调用方传入的 queries 执行预授权结算扣费。
// 调用方必须传入事务内 queries，确保账务事实和 request 状态一致提交。
func (s *Service) CaptureWithQueries(ctx context.Context, queries *sqlc.Queries, params CaptureParams) (Reservation, error) {
	return s.captureWithQueries(ctx, queries, params)
}

// captureWithQueries 执行 authorized -> captured 状态转移。
// 它在调用方事务内扣真实余额、释放冻结余额，并写入 debit ledger entry。
func (s *Service) captureWithQueries(ctx context.Context, queries *sqlc.Queries, params CaptureParams) (Reservation, error) {
	// 0 金额请求应该 release reservation，不允许写 0 金额扣费流水。
	if !isPositiveNumeric(params.ActualAmount) {
		return Reservation{}, ledgerFailure(failure.CodeLedgerInvalidAmount, ErrInvalidAmount, ErrInvalidAmount.Error())
	}

	// 锁住当前 reservation 行，避免同一请求的 capture/release 并发竞争。
	reservation, err := queries.GetLedgerReservationByRequestRecordIDForUpdate(ctx, params.RequestRecordID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Reservation{}, ledgerFailure(failure.CodeLedgerReservationNotFound, ErrReservationNotFound, ErrReservationNotFound.Error())
		}

		return Reservation{}, ledgerFailure(failure.CodeLedgerStoreFailed, err, "lock ledger reservation")
	}

	// ReservationID 是调用方持有的冻结事实 ID，用于防止参数错乱。
	if params.ReservationID != nil && reservation.ID != *params.ReservationID {
		return Reservation{}, ledgerFailure(failure.CodeLedgerIdempotencyConflict, ErrIdempotencyConflict, ErrIdempotencyConflict.Error())
	}

	// 用户在冻结额度内最多扣已冻结金额；真实金额超出冻结额度时，先尝试从未冻结可用余额二次补扣
	// （扣到清空可用余额为止，余额永不为负），补扣后仍不可回收的残差才进入平台核销。
	capturedAmount := numericMin(params.ActualAmount, reservation.AuthorizedAmount)
	overageRequired := numericGreaterThan(params.ActualAmount, reservation.AuthorizedAmount)

	switch ReservationStatus(reservation.Status) {
	case ReservationStatusCaptured:
		// 已 capture 且冻结额度内扣费金额一致，视为幂等重放。
		if !sameNumeric(reservation.CapturedAmount, capturedAmount) {
			return Reservation{}, ledgerFailure(failure.CodeLedgerIdempotencyConflict, ErrIdempotencyConflict, ErrIdempotencyConflict.Error())
		}

		// 还原首次结算的超额二次补扣金额：有独立 overage debit 即取其金额，否则为 0。
		overageCaptured := numericZero()
		overageEntry, err := queries.GetLedgerEntryByIdempotencyKey(ctx, overageIdempotencyKey(params.IdempotencyKey))
		if err == nil {
			overageCaptured = overageEntry.Amount
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return Reservation{}, ledgerFailure(failure.CodeLedgerStoreFailed, err, "lookup ledger overage entry")
		}

		// 平台核销残差 = 真实费用 - (冻结内扣费 + 超额补扣)；>0 时应存在 write_off 异常。
		residualWriteOff := numericGreaterThan(params.ActualAmount, numericAdd(capturedAmount, overageCaptured))

		exception, err := queries.GetLedgerBillingExceptionByReservationID(ctx, reservation.ID)
		if err == nil {
			if exception.EventType != string(BillingExceptionEventTypeWriteOff) ||
				!residualWriteOff ||
				!sameNumeric(exception.ActualAmount, params.ActualAmount) ||
				!sameNumeric(exception.CapturedAmount, numericAdd(capturedAmount, overageCaptured)) {
				return Reservation{}, ledgerFailure(failure.CodeLedgerIdempotencyConflict, ErrIdempotencyConflict, ErrIdempotencyConflict.Error())
			}

			replay := reservationFromSQLC(reservation)
			replay.OverageCapturedAmount = overageCaptured
			return replay, nil
		}

		if errors.Is(err, pgx.ErrNoRows) {
			if residualWriteOff {
				return Reservation{}, ledgerFailure(failure.CodeLedgerIdempotencyConflict, ErrIdempotencyConflict, ErrIdempotencyConflict.Error())
			}
			replay := reservationFromSQLC(reservation)
			replay.OverageCapturedAmount = overageCaptured
			return replay, nil
		}

		return Reservation{}, ledgerFailure(failure.CodeLedgerStoreFailed, err, "lookup ledger billing exception")

	case ReservationStatusReleased:
		// 已 release 的 reservation 不能重新扣费。
		return Reservation{}, ledgerFailure(failure.CodeLedgerIdempotencyConflict, ErrIdempotencyConflict, ErrIdempotencyConflict.Error())

	case ReservationStatusAuthorized:
		// authorized 是唯一允许首次 capture 的状态。

	default:
		return Reservation{}, ledgerFailure(failure.CodeLedgerStoreFailed, errors.New("ledger: unexpected reservation status"), "unexpected reservation status")
	}

	// balance_before/after 必须和本事务内的余额更新严格对应。
	before, err := queries.GetUserBalanceForUpdate(ctx, sqlc.GetUserBalanceForUpdateParams{
		UserID:   reservation.UserID,
		Currency: reservation.Currency,
	})
	if err != nil {
		return Reservation{}, ledgerFailure(failure.CodeLedgerStoreFailed, err, "lock user balance")
	}

	// Capture 同时扣真实余额并释放整笔冻结余额；差额记录到 reservation.released_amount。
	after, err := queries.CaptureUserReservedBalance(ctx, sqlc.CaptureUserReservedBalanceParams{
		CapturedAmount:   capturedAmount,
		AuthorizedAmount: reservation.AuthorizedAmount,
		UserID:           reservation.UserID,
		Currency:         reservation.Currency,
	})
	if err != nil {
		return Reservation{}, ledgerFailure(failure.CodeLedgerStoreFailed, err, "capture user reserved balance")
	}

	requestRecordID := reservation.RequestRecordID
	// 真实余额发生变化时必须写 debit ledger entry，作为扣费审计事实。
	entry, err := queries.CreateLedgerEntry(ctx, sqlc.CreateLedgerEntryParams{
		UserID:          reservation.UserID,
		RequestRecordID: int64PtrToPgtypeInt8(&requestRecordID),
		EntryType:       string(EntryTypeDebit),
		Amount:          capturedAmount,
		Currency:        reservation.Currency,
		BalanceBefore:   before.Balance,
		BalanceAfter:    after.Balance,
		IdempotencyKey:  params.IdempotencyKey,
		Reason:          params.Reason,
	})
	if err != nil {
		// reservation 已加锁；这里的唯一冲突通常表示幂等键被其他业务复用。
		if isUniqueViolation(err) {
			return Reservation{}, ledgerFailure(failure.CodeLedgerIdempotencyConflict, ErrIdempotencyConflict, ErrIdempotencyConflict.Error())
		}

		return Reservation{}, ledgerFailure(failure.CodeLedgerStoreFailed, err, "create capture ledger entry")
	}

	// reservation 终态必须指向刚创建的 debit ledger entry，形成 request -> reservation -> ledger 的链路。
	captured, err := queries.CaptureLedgerReservation(ctx, sqlc.CaptureLedgerReservationParams{
		CapturedAmount: capturedAmount,
		CaptureLedgerEntryID: pgtype.Int8{
			Int64: entry.ID,
			Valid: true,
		},
		ID: reservation.ID,
	})
	if err != nil {
		return Reservation{}, ledgerFailure(failure.CodeLedgerStoreFailed, err, "capture ledger reservation")
	}

	// 默认无超额补扣；仅当真实费用超出冻结额度时尝试二次补扣。
	overageCaptured := numericZero()
	if overageRequired {
		// 真实费用超出冻结额度：在同一行锁内从未冻结可用余额二次补扣，扣到清空可用余额为止，余额永不为负。
		overage, err := queries.CollectUserBalanceOverage(ctx, sqlc.CollectUserBalanceOverageParams{
			ActualAmount:     params.ActualAmount,
			AuthorizedAmount: reservation.AuthorizedAmount,
			UserID:           reservation.UserID,
			Currency:         reservation.Currency,
		})
		if err != nil {
			return Reservation{}, ledgerFailure(failure.CodeLedgerStoreFailed, err, "collect user balance overage")
		}
		overageCaptured = overage.CollectedAmount

		// 实际补扣到金额时，写一条独立的超额扣费 debit ledger entry，作为审计事实。
		if isPositiveNumeric(overageCaptured) {
			_, err := queries.CreateLedgerEntry(ctx, sqlc.CreateLedgerEntryParams{
				UserID:          reservation.UserID,
				RequestRecordID: int64PtrToPgtypeInt8(&requestRecordID),
				EntryType:       string(EntryTypeDebit),
				Amount:          overageCaptured,
				Currency:        reservation.Currency,
				BalanceBefore:   after.Balance,
				BalanceAfter:    overage.Balance,
				IdempotencyKey:  overageIdempotencyKey(params.IdempotencyKey),
				Reason:          params.Reason + " (overage)",
			})
			if err != nil {
				if isUniqueViolation(err) {
					return Reservation{}, ledgerFailure(failure.CodeLedgerIdempotencyConflict, ErrIdempotencyConflict, ErrIdempotencyConflict.Error())
				}
				return Reservation{}, ledgerFailure(failure.CodeLedgerStoreFailed, err, "create overage capture ledger entry")
			}
		}

		// 二次补扣后仍不可回收的残差（真实费用 - 冻结内扣费 - 超额补扣）才由平台核销。
		if numericGreaterThan(params.ActualAmount, numericAdd(capturedAmount, overageCaptured)) {
			_, err := queries.CreateLedgerWriteOffException(ctx, sqlc.CreateLedgerWriteOffExceptionParams{
				UserID:          reservation.UserID,
				RequestRecordID: reservation.RequestRecordID,
				ReservationID:   reservation.ID,
				ActualAmount:    params.ActualAmount,
				CapturedAmount:  capturedAmount,
				OverageAmount:   overageCaptured,
				Currency:        reservation.Currency,
				ReasonCode:      "authorization_underfunded",
				Reason:          "actual amount exceeded authorized amount and available balance",
			})
			if err != nil {
				return Reservation{}, ledgerFailure(failure.CodeLedgerStoreFailed, err, "create ledger billing exception")
			}
		}
	}

	result := reservationFromSQLC(captured)
	result.OverageCapturedAmount = overageCaptured
	return result, nil
}

// Release 释放一次尚未结算的预授权金额。
// 它只减少 reserved_balance，不写 ledger entry，因为用户真实余额 balance 没有变化。
func (s *Service) Release(ctx context.Context, params ReleaseParams) (Reservation, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Reservation{}, ledgerFailure(failure.CodeLedgerStoreFailed, err, "begin ledger transaction")
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	txQueries := s.queries.WithTx(tx)

	released, err := s.releaseWithQueries(ctx, txQueries, params)
	if err != nil {
		return Reservation{}, err
	}

	// 只有冻结余额释放和 reservation 终态都更新成功，才提交本次 release。
	if err := tx.Commit(ctx); err != nil {
		return Reservation{}, ledgerFailure(failure.CodeLedgerStoreFailed, err, "commit ledger transaction")
	}

	return released, nil
}

// ReleaseWithBillingException 释放冻结余额，并在同一事务内记录平台账务异常。
// 当前用于 stream 无 final usage 的风险敞口记录；它不扣用户余额，也不写 debit ledger entry。
func (s *Service) ReleaseWithBillingException(ctx context.Context, params ReleaseWithBillingExceptionParams) (Reservation, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Reservation{}, ledgerFailure(failure.CodeLedgerStoreFailed, err, "begin ledger billing exception transaction")
	}
	defer func() { _ = tx.Rollback(ctx) }()

	txQueries := s.queries.WithTx(tx)

	released, err := s.releaseWithQueries(ctx, txQueries, ReleaseParams{
		RequestRecordID: params.RequestRecordID,
		ReservationID:   params.ReservationID,
	})
	if err != nil {
		return Reservation{}, err
	}

	_, err = txQueries.CreateLedgerRiskExposureException(ctx, sqlc.CreateLedgerRiskExposureExceptionParams{
		UserID:          released.UserID,
		RequestRecordID: released.RequestRecordID,
		ReservationID:   released.ID,
		PlatformAmount:  released.AuthorizedAmount,
		Currency:        released.Currency,
		ReasonCode:      params.ReasonCode,
		Reason:          params.Reason,
	})
	if err != nil {
		return Reservation{}, ledgerFailure(failure.CodeLedgerStoreFailed, err, "create ledger billing exception")
	}

	if err := tx.Commit(ctx); err != nil {
		return Reservation{}, ledgerFailure(failure.CodeLedgerStoreFailed, err, "commit ledger billing exception transaction")
	}

	return released, nil
}

// ReleaseWithQueries 使用调用方传入的 queries 释放预授权资金。
// 调用方必须传入事务内 queries，确保 request 状态和 reservation 终态一致提交。
func (s *Service) ReleaseWithQueries(ctx context.Context, queries *sqlc.Queries, params ReleaseParams) (Reservation, error) {
	return s.releaseWithQueries(ctx, queries, params)
}

// releaseWithQueries 执行 authorized -> released 状态转移。
// 它只释放冻结余额，不写 ledger entry。
func (s *Service) releaseWithQueries(ctx context.Context, queries *sqlc.Queries, params ReleaseParams) (Reservation, error) {
	// 锁住 reservation，串行化同一请求的 capture/release 竞争。
	reservation, err := queries.GetLedgerReservationByRequestRecordIDForUpdate(ctx, params.RequestRecordID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Reservation{}, ledgerFailure(failure.CodeLedgerReservationNotFound, ErrReservationNotFound, ErrReservationNotFound.Error())
		}
		return Reservation{}, ledgerFailure(failure.CodeLedgerStoreFailed, err, "lock ledger reservation")
	}

	// ReservationID 是调用方持有的冻结事实 ID，用于防止参数错乱。
	if params.ReservationID != nil && reservation.ID != *params.ReservationID {
		return Reservation{}, ledgerFailure(failure.CodeLedgerIdempotencyConflict, ErrIdempotencyConflict, ErrIdempotencyConflict.Error())
	}

	switch ReservationStatus(reservation.Status) {
	case ReservationStatusReleased:
		// 已 release 视为幂等重放。
		return reservationFromSQLC(reservation), nil

	case ReservationStatusCaptured:
		// 已 capture 的 reservation 不能再释放。
		return Reservation{}, ledgerFailure(failure.CodeLedgerIdempotencyConflict, ErrIdempotencyConflict, ErrIdempotencyConflict.Error())

	case ReservationStatusAuthorized:
		// authorized 是唯一允许首次 release 的状态。

	default:
		return Reservation{}, ledgerFailure(failure.CodeLedgerStoreFailed, errors.New("ledger: unexpected reservation status"), "unexpected reservation status")
	}

	// Release 不改变真实余额，只减少冻结余额。
	_, err = queries.ReleaseUserReservedBalance(ctx, sqlc.ReleaseUserReservedBalanceParams{
		Amount:   reservation.AuthorizedAmount,
		UserID:   reservation.UserID,
		Currency: reservation.Currency,
	})
	if err != nil {
		return Reservation{}, ledgerFailure(failure.CodeLedgerStoreFailed, err, "release user reserved balance")
	}

	// released_amount 等于 authorized_amount，表示整笔冻结金额已释放。
	released, err := queries.ReleaseLedgerReservation(ctx, reservation.ID)
	if err != nil {
		return Reservation{}, ledgerFailure(failure.CodeLedgerStoreFailed, err, "release ledger reservation")
	}

	return reservationFromSQLC(released), nil
}

// resolveReservationCreateConflict 在 reservation 唯一约束冲突后解析冲突来源。
func (s *Service) resolveReservationCreateConflict(ctx context.Context, tx pgx.Tx, idempotencyKey string, userID int64, requestRecordID int64, amount pgtype.Numeric, currency string) (Reservation, error) {
	_ = tx.Rollback(ctx)

	// 第一优先级查 idempotency_key：同一个幂等键并发进入时，应返回已有 reservation，视为幂等成功。
	existing, err := s.queries.GetLedgerReservationByIdempotencyKey(ctx, idempotencyKey)
	if err == nil {
		if err := ensureIdempotentReservationMatches(existing, userID, requestRecordID, amount, currency); err != nil {
			return Reservation{}, err
		}
		return reservationFromSQLC(existing), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Reservation{}, ledgerFailure(failure.CodeLedgerStoreFailed, err, "lookup committed reservation idempotency key")
	}

	// 第二优先级查 request_record_id：同一个 request 被不同幂等键重复预授权，应视为业务幂等冲突。
	existing, err = s.queries.GetLedgerReservationByRequestRecordID(ctx, requestRecordID)
	if err == nil {
		return Reservation{}, ledgerFailure(failure.CodeLedgerIdempotencyConflict, ErrIdempotencyConflict, ErrIdempotencyConflict.Error())
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Reservation{}, ledgerFailure(failure.CodeLedgerStoreFailed, err, "lookup committed reservation request")
	}

	// 理论上唯一冲突只能来自 idempotency_key 或 request_record_id；两个都查不到说明状态不符合当前业务假设。
	return Reservation{}, ledgerFailure(failure.CodeLedgerStoreFailed, pgx.ErrNoRows, "resolve reservation create conflict")
}

func (s *Service) resolveReservationUnavailable(ctx context.Context, tx pgx.Tx, params PreAuthorizeParams) (Reservation, error) {
	_ = tx.Rollback(ctx)

	existing, err := s.queries.GetLedgerReservationByIdempotencyKey(ctx, params.IdempotencyKey)
	if err == nil {
		if err := ensureIdempotentReservationMatches(existing, params.UserID, params.RequestRecordID, params.EstimatedAmount, params.Currency); err != nil {
			return Reservation{}, err
		}
		return reservationFromSQLC(existing), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Reservation{}, ledgerFailure(failure.CodeLedgerStoreFailed, err, "lookup committed reservation idempotency key")
	}

	return Reservation{}, ledgerFailure(failure.CodeLedgerInsufficientBalance, ErrInsufficientBalance, ErrInsufficientBalance.Error())
}

// reservationFromSQLC 将 sqlc 预扣除记录转换为 ledger 领域 DTO。
func reservationFromSQLC(row sqlc.LedgerReservation) Reservation {
	return Reservation{
		ID:                   row.ID,
		UserID:               row.UserID,
		RequestRecordID:      row.RequestRecordID,
		Currency:             row.Currency,
		Status:               ReservationStatus(row.Status),
		AuthorizedAmount:     row.AuthorizedAmount,
		CapturedAmount:       row.CapturedAmount,
		ReleasedAmount:       row.ReleasedAmount,
		EstimatedAmount:      row.EstimatedAmount,
		CaptureLedgerEntryID: pgtypeInt8ToInt64Ptr(row.CaptureLedgerEntryID),
		IdempotencyKey:       row.IdempotencyKey,
		Reason:               row.Reason,
		// 默认无超额补扣；capture 路径在确有二次补扣时覆盖为实扣金额。
		OverageCapturedAmount: numericZero(),
	}
}

// overageIdempotencyKey 由 capture 幂等键派生超额补扣 debit 的幂等键，保证与主扣费流水键不冲突。
func overageIdempotencyKey(captureIdempotencyKey string) string {
	return captureIdempotencyKey + ":overage"
}

func ensureIdempotentReservationMatches(row sqlc.LedgerReservation, userID int64, requestRecordID int64, amount pgtype.Numeric, currency string) error {
	if row.UserID != userID {
		return ledgerFailure(failure.CodeLedgerIdempotencyConflict, ErrIdempotencyConflict, ErrIdempotencyConflict.Error())
	}
	if row.RequestRecordID != requestRecordID {
		return ledgerFailure(failure.CodeLedgerIdempotencyConflict, ErrIdempotencyConflict, ErrIdempotencyConflict.Error())
	}
	if row.Currency != currency {
		return ledgerFailure(failure.CodeLedgerIdempotencyConflict, ErrIdempotencyConflict, ErrIdempotencyConflict.Error())
	}
	if !sameNumeric(row.EstimatedAmount, amount) {
		return ledgerFailure(failure.CodeLedgerIdempotencyConflict, ErrIdempotencyConflict, ErrIdempotencyConflict.Error())
	}

	return nil
}
