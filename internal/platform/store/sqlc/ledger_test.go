package sqlc_test

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

// isForeignKeyViolation 判断数据库错误是否是外键约束冲突。
func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

// assertNumericEquals 校验 NUMERIC 字段表示的金额值，忽略 PostgreSQL 返回的 scale 差异。
func assertNumericEquals(t *testing.T, got pgtype.Numeric, want int64) {
	t.Helper()

	if !got.Valid {
		t.Fatalf("expected numeric %d to be valid", want)
	}
	if got.Int == nil {
		t.Fatal("expected numeric int to be set")
	}

	rat := new(big.Rat).SetInt(new(big.Int).Set(got.Int))
	if got.Exp > 0 {
		rat.Mul(rat, new(big.Rat).SetInt(pow10(got.Exp)))
	}
	if got.Exp < 0 {
		rat.Quo(rat, new(big.Rat).SetInt(pow10(-got.Exp)))
	}

	if rat.Cmp(big.NewRat(want, 1)) != 0 {
		t.Fatalf("expected numeric %d, got %s", want, rat.String())
	}
}

// pow10 返回 10 的 exp 次方。
func pow10(exp int32) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(exp)), nil)
}

// createLedgerEntryForTest 创建测试账本流水。
func createLedgerEntryForTest(t *testing.T, queries *sqlc.Queries, ctx context.Context, params sqlc.CreateLedgerEntryParams) sqlc.LedgerEntry {
	t.Helper()

	entry, err := queries.CreateLedgerEntry(ctx, params)
	if err != nil {
		t.Fatalf("create ledger entry: %v", err)
	}

	return entry
}

func TestUserBalanceLifecycleAndUniqueness(t *testing.T) {
	ctx, _, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	identity := createRequestRecordIdentity(t, ctx, queries)

	created, err := queries.CreateUserBalance(ctx, sqlc.CreateUserBalanceParams{
		UserID:   identity.user.ID,
		Currency: "USD",
		Balance:  numeric(100),
	})
	if err != nil {
		t.Fatalf("create user balance: %v", err)
	}
	if created.UserID != identity.user.ID {
		t.Fatalf("expected user id %d, got %d", identity.user.ID, created.UserID)
	}
	if created.Currency != "USD" {
		t.Fatalf("expected currency USD, got %q", created.Currency)
	}
	assertNumericEquals(t, created.Balance, 100)
	assertNumericEquals(t, created.ReservedBalance, 0)
	if !created.CreatedAt.Valid || !created.UpdatedAt.Valid {
		t.Fatal("expected balance timestamps to be set")
	}

	got, err := queries.GetUserBalance(ctx, sqlc.GetUserBalanceParams{
		UserID:   identity.user.ID,
		Currency: "USD",
	})
	if err != nil {
		t.Fatalf("get user balance: %v", err)
	}
	if got.ID != created.ID {
		t.Fatalf("expected balance id %d, got %d", created.ID, got.ID)
	}

	locked, err := queries.GetUserBalanceForUpdate(ctx, sqlc.GetUserBalanceForUpdateParams{
		UserID:   identity.user.ID,
		Currency: "USD",
	})
	if err != nil {
		t.Fatalf("get user balance for update: %v", err)
	}
	if locked.ID != created.ID {
		t.Fatalf("expected locked balance id %d, got %d", created.ID, locked.ID)
	}

	updated, err := queries.UpdateUserBalance(ctx, sqlc.UpdateUserBalanceParams{
		Balance:  numeric(75),
		UserID:   identity.user.ID,
		Currency: "USD",
	})
	if err != nil {
		t.Fatalf("update user balance: %v", err)
	}
	assertNumericEquals(t, updated.Balance, 75)
	if !updated.UpdatedAt.Valid {
		t.Fatal("expected updated_at to be set")
	}

	_, err = queries.CreateUserBalance(ctx, sqlc.CreateUserBalanceParams{
		UserID:   identity.user.ID,
		Currency: "USD",
		Balance:  numeric(1),
	})
	if err == nil {
		t.Fatal("expected duplicate user currency balance to fail")
	}
	if !isUniqueViolation(err) {
		t.Fatalf("expected unique violation, got %v", err)
	}
}

func TestEnsureAddAndSubtractUserBalance(t *testing.T) {
	ctx, _, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	identity := createRequestRecordIdentity(t, ctx, queries)

	if err := queries.EnsureUserBalance(ctx, sqlc.EnsureUserBalanceParams{
		UserID:   identity.user.ID,
		Currency: "USD",
	}); err != nil {
		t.Fatalf("ensure user balance: %v", err)
	}

	// EnsureUserBalance 是幂等操作；余额行已存在时不应报唯一约束错误。
	if err := queries.EnsureUserBalance(ctx, sqlc.EnsureUserBalanceParams{
		UserID:   identity.user.ID,
		Currency: "USD",
	}); err != nil {
		t.Fatalf("ensure existing user balance: %v", err)
	}

	initial, err := queries.GetUserBalance(ctx, sqlc.GetUserBalanceParams{
		UserID:   identity.user.ID,
		Currency: "USD",
	})
	if err != nil {
		t.Fatalf("get ensured user balance: %v", err)
	}
	assertNumericEquals(t, initial.Balance, 0)
	assertNumericEquals(t, initial.ReservedBalance, 0)

	added, err := queries.AddUserBalance(ctx, sqlc.AddUserBalanceParams{
		Amount:   numeric(25),
		UserID:   identity.user.ID,
		Currency: "USD",
	})
	if err != nil {
		t.Fatalf("add user balance: %v", err)
	}
	assertNumericEquals(t, added.Balance, 25)
	assertNumericEquals(t, added.ReservedBalance, 0)

	subtracted, err := queries.SubtractUserBalance(ctx, sqlc.SubtractUserBalanceParams{
		Amount:   numeric(10),
		UserID:   identity.user.ID,
		Currency: "USD",
	})
	if err != nil {
		t.Fatalf("subtract user balance: %v", err)
	}
	assertNumericEquals(t, subtracted.Balance, 15)
	assertNumericEquals(t, subtracted.ReservedBalance, 0)

	_, err = queries.SubtractUserBalance(ctx, sqlc.SubtractUserBalanceParams{
		Amount:   numeric(99),
		UserID:   identity.user.ID,
		Currency: "USD",
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expected no rows for insufficient balance, got %v", err)
	}

	got, err := queries.GetUserBalance(ctx, sqlc.GetUserBalanceParams{
		UserID:   identity.user.ID,
		Currency: "USD",
	})
	if err != nil {
		t.Fatalf("get user balance after failed subtract: %v", err)
	}
	assertNumericEquals(t, got.Balance, 15)
	assertNumericEquals(t, got.ReservedBalance, 0)
}

func TestReserveUserBalanceConsumesAvailableBalance(t *testing.T) {
	ctx, _, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	identity := createRequestRecordIdentity(t, ctx, queries)

	if _, err := queries.CreateUserBalance(ctx, sqlc.CreateUserBalanceParams{
		UserID:   identity.user.ID,
		Currency: "USD",
		Balance:  numeric(100),
	}); err != nil {
		t.Fatalf("create user balance: %v", err)
	}

	reserved, err := queries.ReserveUserBalance(ctx, sqlc.ReserveUserBalanceParams{
		Amount:   numeric(70),
		UserID:   identity.user.ID,
		Currency: "USD",
	})
	if err != nil {
		t.Fatalf("reserve user balance: %v", err)
	}
	assertNumericEquals(t, reserved.Balance, 100)
	assertNumericEquals(t, reserved.ReservedBalance, 70)

	_, err = queries.ReserveUserBalance(ctx, sqlc.ReserveUserBalanceParams{
		Amount:   numeric(31),
		UserID:   identity.user.ID,
		Currency: "USD",
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expected no rows when reserve exceeds available balance, got %v", err)
	}

	got, err := queries.GetUserBalance(ctx, sqlc.GetUserBalanceParams{
		UserID:   identity.user.ID,
		Currency: "USD",
	})
	if err != nil {
		t.Fatalf("get user balance after failed reserve: %v", err)
	}
	assertNumericEquals(t, got.Balance, 100)
	assertNumericEquals(t, got.ReservedBalance, 70)
}

func TestSubtractUserBalanceRespectsReservedBalance(t *testing.T) {
	ctx, _, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	identity := createRequestRecordIdentity(t, ctx, queries)

	if _, err := queries.CreateUserBalance(ctx, sqlc.CreateUserBalanceParams{
		UserID:   identity.user.ID,
		Currency: "USD",
		Balance:  numeric(100),
	}); err != nil {
		t.Fatalf("create user balance: %v", err)
	}
	if _, err := queries.ReserveUserBalance(ctx, sqlc.ReserveUserBalanceParams{
		Amount:   numeric(70),
		UserID:   identity.user.ID,
		Currency: "USD",
	}); err != nil {
		t.Fatalf("reserve user balance: %v", err)
	}

	_, err := queries.SubtractUserBalance(ctx, sqlc.SubtractUserBalanceParams{
		Amount:   numeric(40),
		UserID:   identity.user.ID,
		Currency: "USD",
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expected no rows when subtract exceeds available balance, got %v", err)
	}

	subtracted, err := queries.SubtractUserBalance(ctx, sqlc.SubtractUserBalanceParams{
		Amount:   numeric(30),
		UserID:   identity.user.ID,
		Currency: "USD",
	})
	if err != nil {
		t.Fatalf("subtract unreserved balance: %v", err)
	}
	assertNumericEquals(t, subtracted.Balance, 70)
	assertNumericEquals(t, subtracted.ReservedBalance, 70)
}

func TestUserBalanceRejectsInvalidConstraints(t *testing.T) {
	cases := []struct {
		name     string
		currency string
		balance  pgtype.Numeric
	}{
		{
			name:     "empty currency",
			currency: "",
			balance:  numeric(0),
		},
		{
			name:     "negative balance",
			currency: "USD",
			balance:  numeric(-1),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, _, queries, cleanup := newModelChannelTestTx(t)
			defer cleanup()

			identity := createRequestRecordIdentity(t, ctx, queries)

			_, err := queries.CreateUserBalance(ctx, sqlc.CreateUserBalanceParams{
				UserID:   identity.user.ID,
				Currency: tc.currency,
				Balance:  tc.balance,
			})
			if err == nil {
				t.Fatal("expected check violation")
			}
			if !isCheckViolation(err) {
				t.Fatalf("expected check violation, got %v", err)
			}
		})
	}
}

func TestLedgerEntryCreateGetAndList(t *testing.T) {
	ctx, _, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	identity := createRequestRecordIdentity(t, ctx, queries)
	requestRecord := createRequestRecordForTest(t, ctx, queries, identity, fmt.Sprintf("ledger-list-%d", time.Now().UnixNano()))

	credit := createLedgerEntryForTest(t, queries, ctx, sqlc.CreateLedgerEntryParams{
		UserID:          identity.user.ID,
		RequestRecordID: pgtype.Int8{Valid: false},
		EntryType:       "credit",
		Amount:          numeric(100),
		Currency:        "USD",
		BalanceBefore:   numeric(0),
		BalanceAfter:    numeric(100),
		IdempotencyKey:  fmt.Sprintf("ledger-credit-%d", time.Now().UnixNano()),
		Reason:          "test credit",
	})
	debit := createLedgerEntryForTest(t, queries, ctx, sqlc.CreateLedgerEntryParams{
		UserID:          identity.user.ID,
		RequestRecordID: pgtype.Int8{Int64: requestRecord.ID, Valid: true},
		EntryType:       "debit",
		Amount:          numeric(25),
		Currency:        "USD",
		BalanceBefore:   numeric(100),
		BalanceAfter:    numeric(75),
		IdempotencyKey:  fmt.Sprintf("ledger-debit-%d", time.Now().UnixNano()),
		Reason:          "test debit",
	})
	refund := createLedgerEntryForTest(t, queries, ctx, sqlc.CreateLedgerEntryParams{
		UserID:          identity.user.ID,
		RequestRecordID: pgtype.Int8{Int64: requestRecord.ID, Valid: true},
		EntryType:       "refund",
		Amount:          numeric(5),
		Currency:        "USD",
		BalanceBefore:   numeric(75),
		BalanceAfter:    numeric(80),
		IdempotencyKey:  fmt.Sprintf("ledger-refund-%d", time.Now().UnixNano()),
		Reason:          "test refund",
	})

	got, err := queries.GetLedgerEntryByIdempotencyKey(ctx, debit.IdempotencyKey)
	if err != nil {
		t.Fatalf("get ledger entry by idempotency key: %v", err)
	}
	if got.ID != debit.ID {
		t.Fatalf("expected ledger id %d, got %d", debit.ID, got.ID)
	}
	assertNumericEquals(t, got.Amount, 25)
	assertNumericEquals(t, got.BalanceBefore, 100)
	assertNumericEquals(t, got.BalanceAfter, 75)

	byUser, err := queries.ListLedgerEntriesByUser(ctx, sqlc.ListLedgerEntriesByUserParams{
		UserID:     identity.user.ID,
		Currency:   "USD",
		OffsetRows: 0,
		LimitRows:  10,
	})
	if err != nil {
		t.Fatalf("list ledger entries by user: %v", err)
	}
	if len(byUser) != 3 {
		t.Fatalf("expected 3 ledger entries by user, got %d", len(byUser))
	}
	if byUser[0].ID != refund.ID || byUser[1].ID != debit.ID || byUser[2].ID != credit.ID {
		t.Fatalf("expected entries ordered refund/debit/credit, got ids %d/%d/%d", byUser[0].ID, byUser[1].ID, byUser[2].ID)
	}

	byRequest, err := queries.ListLedgerEntriesByRequest(ctx, pgtype.Int8{Int64: requestRecord.ID, Valid: true})
	if err != nil {
		t.Fatalf("list ledger entries by request: %v", err)
	}
	if len(byRequest) != 2 {
		t.Fatalf("expected 2 ledger entries by request, got %d", len(byRequest))
	}
	if byRequest[0].ID != debit.ID || byRequest[1].ID != refund.ID {
		t.Fatalf("expected request entries ordered debit/refund, got ids %d/%d", byRequest[0].ID, byRequest[1].ID)
	}
}

func TestLedgerEntryRejectsInvalidConstraints(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*sqlc.CreateLedgerEntryParams)
	}{
		{
			name: "invalid entry type",
			mutate: func(params *sqlc.CreateLedgerEntryParams) {
				params.EntryType = "reserve"
			},
		},
		{
			name: "zero amount",
			mutate: func(params *sqlc.CreateLedgerEntryParams) {
				params.Amount = numeric(0)
			},
		},
		{
			name: "negative amount",
			mutate: func(params *sqlc.CreateLedgerEntryParams) {
				params.Amount = numeric(-1)
			},
		},
		{
			name: "empty currency",
			mutate: func(params *sqlc.CreateLedgerEntryParams) {
				params.Currency = ""
			},
		},
		{
			name: "negative balance before",
			mutate: func(params *sqlc.CreateLedgerEntryParams) {
				params.BalanceBefore = numeric(-1)
			},
		},
		{
			name: "negative balance after",
			mutate: func(params *sqlc.CreateLedgerEntryParams) {
				params.BalanceAfter = numeric(-1)
			},
		},
		{
			name: "empty idempotency key",
			mutate: func(params *sqlc.CreateLedgerEntryParams) {
				params.IdempotencyKey = ""
			},
		},
		{
			name: "empty reason",
			mutate: func(params *sqlc.CreateLedgerEntryParams) {
				params.Reason = ""
			},
		},
		{
			name: "credit balance math mismatch",
			mutate: func(params *sqlc.CreateLedgerEntryParams) {
				params.EntryType = "credit"
				params.Amount = numeric(10)
				params.BalanceBefore = numeric(100)
				params.BalanceAfter = numeric(100)
			},
		},
		{
			name: "debit balance math mismatch",
			mutate: func(params *sqlc.CreateLedgerEntryParams) {
				params.EntryType = "debit"
				params.Amount = numeric(10)
				params.BalanceBefore = numeric(100)
				params.BalanceAfter = numeric(100)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, _, queries, cleanup := newModelChannelTestTx(t)
			defer cleanup()

			identity := createRequestRecordIdentity(t, ctx, queries)
			params := sqlc.CreateLedgerEntryParams{
				UserID:          identity.user.ID,
				RequestRecordID: pgtype.Int8{Valid: false},
				EntryType:       "credit",
				Amount:          numeric(1),
				Currency:        "USD",
				BalanceBefore:   numeric(0),
				BalanceAfter:    numeric(1),
				IdempotencyKey:  fmt.Sprintf("ledger-invalid-%s-%d", tc.name, time.Now().UnixNano()),
				Reason:          "test invalid constraint",
			}
			tc.mutate(&params)

			_, err := queries.CreateLedgerEntry(ctx, params)
			if err == nil {
				t.Fatal("expected check violation")
			}
			if !isCheckViolation(err) {
				t.Fatalf("expected check violation, got %v", err)
			}
		})
	}
}

func TestLedgerEntryRejectsDuplicateIdempotencyKey(t *testing.T) {
	ctx, _, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	identity := createRequestRecordIdentity(t, ctx, queries)
	key := fmt.Sprintf("ledger-duplicate-%d", time.Now().UnixNano())
	params := sqlc.CreateLedgerEntryParams{
		UserID:          identity.user.ID,
		RequestRecordID: pgtype.Int8{Valid: false},
		EntryType:       "credit",
		Amount:          numeric(1),
		Currency:        "USD",
		BalanceBefore:   numeric(0),
		BalanceAfter:    numeric(1),
		IdempotencyKey:  key,
		Reason:          "test duplicate idempotency key",
	}

	createLedgerEntryForTest(t, queries, ctx, params)

	_, err := queries.CreateLedgerEntry(ctx, params)
	if err == nil {
		t.Fatal("expected duplicate idempotency key to fail")
	}
	if !isUniqueViolation(err) {
		t.Fatalf("expected unique violation, got %v", err)
	}
}

func TestLedgerEntryRequiresMatchingRequestUser(t *testing.T) {
	ctx, _, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	requestOwner := createRequestRecordIdentity(t, ctx, queries)
	otherUser := createRequestRecordIdentity(t, ctx, queries)
	requestRecord := createRequestRecordForTest(t, ctx, queries, requestOwner, fmt.Sprintf("ledger-request-owner-%d", time.Now().UnixNano()))

	_, err := queries.CreateLedgerEntry(ctx, sqlc.CreateLedgerEntryParams{
		UserID:          otherUser.user.ID,
		RequestRecordID: pgtype.Int8{Int64: requestRecord.ID, Valid: true},
		EntryType:       "debit",
		Amount:          numeric(1),
		Currency:        "USD",
		BalanceBefore:   numeric(10),
		BalanceAfter:    numeric(9),
		IdempotencyKey:  fmt.Sprintf("ledger-user-mismatch-%d", time.Now().UnixNano()),
		Reason:          "mismatched request user",
	})
	if err == nil {
		t.Fatal("expected ledger entry with mismatched request user to fail")
	}
	if !isForeignKeyViolation(err) {
		t.Fatalf("expected foreign key violation, got %v", err)
	}
}

func TestLedgerReservationCreateGetAndUniqueness(t *testing.T) {
	ctx, _, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	identity := createRequestRecordIdentity(t, ctx, queries)
	requestRecord := createRequestRecordForTest(t, ctx, queries, identity, fmt.Sprintf("reservation-%d", time.Now().UnixNano()))

	created, err := queries.CreateLedgerReservation(ctx, sqlc.CreateLedgerReservationParams{
		UserID:           identity.user.ID,
		RequestRecordID:  requestRecord.ID,
		Currency:         "USD",
		AuthorizedAmount: numeric(40),
		EstimatedAmount:  numeric(40),
		IdempotencyKey:   fmt.Sprintf("reservation-idempotency-%d", time.Now().UnixNano()),
		Reason:           "test reservation",
	})
	if err != nil {
		t.Fatalf("create ledger reservation: %v", err)
	}
	if created.UserID != identity.user.ID {
		t.Fatalf("expected user id %d, got %d", identity.user.ID, created.UserID)
	}
	if created.RequestRecordID != requestRecord.ID {
		t.Fatalf("expected request record id %d, got %d", requestRecord.ID, created.RequestRecordID)
	}
	if created.Status != "authorized" {
		t.Fatalf("expected authorized reservation, got %q", created.Status)
	}
	assertNumericEquals(t, created.AuthorizedAmount, 40)
	assertNumericEquals(t, created.CapturedAmount, 0)
	assertNumericEquals(t, created.ReleasedAmount, 0)
	if created.CaptureLedgerEntryID.Valid {
		t.Fatalf("expected no capture ledger entry id, got %d", created.CaptureLedgerEntryID.Int64)
	}
	if !created.CreatedAt.Valid || !created.UpdatedAt.Valid {
		t.Fatal("expected reservation timestamps to be set")
	}
	if created.CapturedAt.Valid || created.ReleasedAt.Valid {
		t.Fatal("expected authorized reservation to have no terminal timestamps")
	}

	byKey, err := queries.GetLedgerReservationByIdempotencyKey(ctx, created.IdempotencyKey)
	if err != nil {
		t.Fatalf("get reservation by idempotency key: %v", err)
	}
	if byKey.ID != created.ID {
		t.Fatalf("expected reservation id %d by key, got %d", created.ID, byKey.ID)
	}

	byRequest, err := queries.GetLedgerReservationByRequestRecordID(ctx, requestRecord.ID)
	if err != nil {
		t.Fatalf("get reservation by request record id: %v", err)
	}
	if byRequest.ID != created.ID {
		t.Fatalf("expected reservation id %d by request, got %d", created.ID, byRequest.ID)
	}

	_, err = queries.CreateLedgerReservation(ctx, sqlc.CreateLedgerReservationParams{
		UserID:           identity.user.ID,
		RequestRecordID:  requestRecord.ID,
		Currency:         "USD",
		AuthorizedAmount: numeric(1),
		EstimatedAmount:  numeric(1),
		IdempotencyKey:   fmt.Sprintf("reservation-duplicate-request-%d", time.Now().UnixNano()),
		Reason:           "duplicate request reservation",
	})
	if err == nil {
		t.Fatal("expected duplicate request reservation to fail")
	}
	if !isUniqueViolation(err) {
		t.Fatalf("expected unique violation, got %v", err)
	}
}

func TestLedgerReservationRequiresMatchingRequestUser(t *testing.T) {
	ctx, _, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	requestOwner := createRequestRecordIdentity(t, ctx, queries)
	otherUser := createRequestRecordIdentity(t, ctx, queries)
	requestRecord := createRequestRecordForTest(t, ctx, queries, requestOwner, fmt.Sprintf("reservation-request-owner-%d", time.Now().UnixNano()))

	_, err := queries.CreateLedgerReservation(ctx, sqlc.CreateLedgerReservationParams{
		UserID:           otherUser.user.ID,
		RequestRecordID:  requestRecord.ID,
		Currency:         "USD",
		AuthorizedAmount: numeric(1),
		EstimatedAmount:  numeric(1),
		IdempotencyKey:   fmt.Sprintf("reservation-user-mismatch-%d", time.Now().UnixNano()),
		Reason:           "mismatched request user",
	})
	if err == nil {
		t.Fatal("expected reservation with mismatched request user to fail")
	}
	if !isForeignKeyViolation(err) {
		t.Fatalf("expected foreign key violation, got %v", err)
	}
}

func TestCaptureReservedBalanceAndReservation(t *testing.T) {
	ctx, _, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	identity := createRequestRecordIdentity(t, ctx, queries)
	requestRecord := createRequestRecordForTest(t, ctx, queries, identity, fmt.Sprintf("capture-reservation-%d", time.Now().UnixNano()))

	if _, err := queries.CreateUserBalance(ctx, sqlc.CreateUserBalanceParams{
		UserID:   identity.user.ID,
		Currency: "USD",
		Balance:  numeric(100),
	}); err != nil {
		t.Fatalf("create user balance: %v", err)
	}
	if _, err := queries.ReserveUserBalance(ctx, sqlc.ReserveUserBalanceParams{
		Amount:   numeric(40),
		UserID:   identity.user.ID,
		Currency: "USD",
	}); err != nil {
		t.Fatalf("reserve user balance: %v", err)
	}
	reservation, err := queries.CreateLedgerReservation(ctx, sqlc.CreateLedgerReservationParams{
		UserID:           identity.user.ID,
		RequestRecordID:  requestRecord.ID,
		Currency:         "USD",
		AuthorizedAmount: numeric(40),
		EstimatedAmount:  numeric(40),
		IdempotencyKey:   fmt.Sprintf("capture-reservation-%d", time.Now().UnixNano()),
		Reason:           "test capture reservation",
	})
	if err != nil {
		t.Fatalf("create ledger reservation: %v", err)
	}

	locked, err := queries.GetLedgerReservationByRequestRecordIDForUpdate(ctx, requestRecord.ID)
	if err != nil {
		t.Fatalf("get reservation for update: %v", err)
	}
	if locked.ID != reservation.ID {
		t.Fatalf("expected locked reservation id %d, got %d", reservation.ID, locked.ID)
	}

	debit := createLedgerEntryForTest(t, queries, ctx, sqlc.CreateLedgerEntryParams{
		UserID:          identity.user.ID,
		RequestRecordID: pgtype.Int8{Int64: requestRecord.ID, Valid: true},
		EntryType:       "debit",
		Amount:          numeric(25),
		Currency:        "USD",
		BalanceBefore:   numeric(100),
		BalanceAfter:    numeric(75),
		IdempotencyKey:  fmt.Sprintf("capture-debit-%d", time.Now().UnixNano()),
		Reason:          "capture reserved balance",
	})

	balance, err := queries.CaptureUserReservedBalance(ctx, sqlc.CaptureUserReservedBalanceParams{
		CapturedAmount:   numeric(25),
		AuthorizedAmount: numeric(40),
		UserID:           identity.user.ID,
		Currency:         "USD",
	})
	if err != nil {
		t.Fatalf("capture user reserved balance: %v", err)
	}
	assertNumericEquals(t, balance.Balance, 75)
	assertNumericEquals(t, balance.ReservedBalance, 0)

	captured, err := queries.CaptureLedgerReservation(ctx, sqlc.CaptureLedgerReservationParams{
		CapturedAmount:       numeric(25),
		CaptureLedgerEntryID: pgtype.Int8{Int64: debit.ID, Valid: true},
		ID:                   reservation.ID,
	})
	if err != nil {
		t.Fatalf("capture ledger reservation: %v", err)
	}
	if captured.Status != "captured" {
		t.Fatalf("expected captured status, got %q", captured.Status)
	}
	assertNumericEquals(t, captured.AuthorizedAmount, 40)
	assertNumericEquals(t, captured.CapturedAmount, 25)
	assertNumericEquals(t, captured.ReleasedAmount, 15)
	if !captured.CaptureLedgerEntryID.Valid || captured.CaptureLedgerEntryID.Int64 != debit.ID {
		t.Fatalf("expected capture ledger entry id %d, got %+v", debit.ID, captured.CaptureLedgerEntryID)
	}
	if !captured.CapturedAt.Valid {
		t.Fatal("expected captured_at to be set")
	}
	if !captured.ReleasedAt.Valid {
		t.Fatal("expected released_at to be set when capture releases unused amount")
	}
}

func TestReleaseReservedBalanceAndReservation(t *testing.T) {
	ctx, _, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	identity := createRequestRecordIdentity(t, ctx, queries)
	requestRecord := createRequestRecordForTest(t, ctx, queries, identity, fmt.Sprintf("release-reservation-%d", time.Now().UnixNano()))

	if _, err := queries.CreateUserBalance(ctx, sqlc.CreateUserBalanceParams{
		UserID:   identity.user.ID,
		Currency: "USD",
		Balance:  numeric(80),
	}); err != nil {
		t.Fatalf("create user balance: %v", err)
	}
	if _, err := queries.ReserveUserBalance(ctx, sqlc.ReserveUserBalanceParams{
		Amount:   numeric(30),
		UserID:   identity.user.ID,
		Currency: "USD",
	}); err != nil {
		t.Fatalf("reserve user balance: %v", err)
	}
	reservation, err := queries.CreateLedgerReservation(ctx, sqlc.CreateLedgerReservationParams{
		UserID:           identity.user.ID,
		RequestRecordID:  requestRecord.ID,
		Currency:         "USD",
		AuthorizedAmount: numeric(30),
		EstimatedAmount:  numeric(30),
		IdempotencyKey:   fmt.Sprintf("release-reservation-%d", time.Now().UnixNano()),
		Reason:           "test release reservation",
	})
	if err != nil {
		t.Fatalf("create ledger reservation: %v", err)
	}

	balance, err := queries.ReleaseUserReservedBalance(ctx, sqlc.ReleaseUserReservedBalanceParams{
		Amount:   numeric(30),
		UserID:   identity.user.ID,
		Currency: "USD",
	})
	if err != nil {
		t.Fatalf("release user reserved balance: %v", err)
	}
	assertNumericEquals(t, balance.Balance, 80)
	assertNumericEquals(t, balance.ReservedBalance, 0)

	released, err := queries.ReleaseLedgerReservation(ctx, reservation.ID)
	if err != nil {
		t.Fatalf("release ledger reservation: %v", err)
	}
	if released.Status != "released" {
		t.Fatalf("expected released status, got %q", released.Status)
	}
	assertNumericEquals(t, released.AuthorizedAmount, 30)
	assertNumericEquals(t, released.CapturedAmount, 0)
	assertNumericEquals(t, released.ReleasedAmount, 30)
	if released.CaptureLedgerEntryID.Valid {
		t.Fatalf("expected no capture ledger entry id, got %d", released.CaptureLedgerEntryID.Int64)
	}
	if released.CapturedAt.Valid {
		t.Fatal("expected captured_at to remain null")
	}
	if !released.ReleasedAt.Valid {
		t.Fatal("expected released_at to be set")
	}
}

func TestCaptureLedgerReservationRequiresMatchingLedgerEntry(t *testing.T) {
	ctx, _, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	identity := createRequestRecordIdentity(t, ctx, queries)
	otherIdentity := createRequestRecordIdentity(t, ctx, queries)
	requestRecord := createRequestRecordForTest(t, ctx, queries, identity, fmt.Sprintf("capture-fk-reservation-%d", time.Now().UnixNano()))
	otherRequestRecord := createRequestRecordForTest(t, ctx, queries, otherIdentity, fmt.Sprintf("capture-fk-other-%d", time.Now().UnixNano()))

	reservation, err := queries.CreateLedgerReservation(ctx, sqlc.CreateLedgerReservationParams{
		UserID:           identity.user.ID,
		RequestRecordID:  requestRecord.ID,
		Currency:         "USD",
		AuthorizedAmount: numeric(10),
		EstimatedAmount:  numeric(10),
		IdempotencyKey:   fmt.Sprintf("capture-fk-reservation-%d", time.Now().UnixNano()),
		Reason:           "test capture ledger entry foreign key",
	})
	if err != nil {
		t.Fatalf("create ledger reservation: %v", err)
	}
	wrongEntry := createLedgerEntryForTest(t, queries, ctx, sqlc.CreateLedgerEntryParams{
		UserID:          otherIdentity.user.ID,
		RequestRecordID: pgtype.Int8{Int64: otherRequestRecord.ID, Valid: true},
		EntryType:       "debit",
		Amount:          numeric(10),
		Currency:        "USD",
		BalanceBefore:   numeric(20),
		BalanceAfter:    numeric(10),
		IdempotencyKey:  fmt.Sprintf("capture-fk-wrong-entry-%d", time.Now().UnixNano()),
		Reason:          "wrong capture ledger entry",
	})

	_, err = queries.CaptureLedgerReservation(ctx, sqlc.CaptureLedgerReservationParams{
		CapturedAmount:       numeric(10),
		CaptureLedgerEntryID: pgtype.Int8{Int64: wrongEntry.ID, Valid: true},
		ID:                   reservation.ID,
	})
	if err == nil {
		t.Fatal("expected capture reservation with mismatched ledger entry to fail")
	}
	if !isForeignKeyViolation(err) {
		t.Fatalf("expected foreign key violation, got %v", err)
	}
}
