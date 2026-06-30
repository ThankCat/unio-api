package ledger

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
)

// numeric 创建测试用 NUMERIC 参数，避免 ledger 测试使用 float64。
func numeric(value int64) pgtype.Numeric {
	return pgtype.Numeric{Int: big.NewInt(value), Valid: true}
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

// newServiceTestDeps 创建 ledger service 集成测试依赖。
func newServiceTestDeps(t *testing.T) (context.Context, *pgxpool.Pool, *sqlc.Queries, *Service, func()) {
	t.Helper()

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		cancel()
		t.Fatalf("create postgres pool: %v", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		cancel()
		t.Fatalf("ping postgres: %v", err)
	}

	queries := sqlc.New(pool)
	service := NewService(pool, queries)
	cleanup := func() {
		pool.Close()
		cancel()
	}

	return ctx, pool, queries, service, cleanup
}

// createLedgerTestUser 创建 ledger service 测试用户。
func createLedgerTestUser(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int64 {
	t.Helper()

	email := fmt.Sprintf("ledger-service-%d@example.test", time.Now().UnixNano())

	var userID int64
	err := pool.QueryRow(ctx, `
		INSERT INTO users (email, password_hash, display_name)
		VALUES ($1, $2, $3)
		RETURNING id
	`, email, "test-password-hash", "Ledger Test User").Scan(&userID)
	if err != nil {
		t.Fatalf("insert ledger test user: %v", err)
	}

	return userID
}

// createLedgerTestRequestRecord 创建 ledger service 预授权测试需要的 request record。
func createLedgerTestRequestRecord(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID int64) int64 {
	t.Helper()

	suffix := time.Now().UnixNano()

	// 线路必填：先建一条线路供 API Key 绑定（route_id 现为 NOT NULL）。
	var routeID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO routes (name, mode, pool_kind, status, price_ratio)
		VALUES ($1, 'cheapest', 'all', 'enabled', 1)
		RETURNING id
	`, fmt.Sprintf("ledger-route-%d", suffix)).Scan(&routeID); err != nil {
		t.Fatalf("insert ledger test route: %v", err)
	}

	var apiKeyID int64
	err := pool.QueryRow(ctx, `
		INSERT INTO api_keys (user_id, name, key_prefix, key_hash, route_id)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id
	`, userID, "ledger test key", "sk-test", fmt.Sprintf("ledger-key-hash-%d", suffix), routeID).Scan(&apiKeyID)
	if err != nil {
		t.Fatalf("insert ledger test api key: %v", err)
	}

	var requestRecordID int64
	err = pool.QueryRow(ctx, `
		INSERT INTO request_records (
			request_id,
			user_id,
			api_key_id,
			requested_model_id,
			ingress_protocol,
			operation,
			stream,
			status,
			started_at
		)
		VALUES ($1, $2, $3, $4, 'openai', 'chat_completions', false, 'pending', now())
		RETURNING id
	`, fmt.Sprintf("ledger-request-%d", suffix), userID, apiKeyID, "deepseek-v4-pro").Scan(&requestRecordID)
	if err != nil {
		t.Fatalf("insert ledger test request record: %v", err)
	}

	return requestRecordID
}

// cleanupLedgerTestUser 删除 ledger service 测试产生的账本、余额和用户。
func cleanupLedgerTestUser(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID int64) {
	t.Helper()

	if _, err := pool.Exec(ctx, `DELETE FROM ledger_billing_exceptions WHERE user_id = $1`, userID); err != nil {
		t.Fatalf("delete ledger billing exceptions: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM ledger_reservations WHERE user_id = $1`, userID); err != nil {
		t.Fatalf("delete ledger reservations: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM ledger_entries WHERE user_id = $1`, userID); err != nil {
		t.Fatalf("delete ledger entries: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM request_records WHERE user_id = $1`, userID); err != nil {
		t.Fatalf("delete request records: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM user_balances WHERE user_id = $1`, userID); err != nil {
		t.Fatalf("delete user balances: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID); err != nil {
		t.Fatalf("delete user: %v", err)
	}
}

func TestPreAuthorizeReservesBalanceAndIsIdempotent(t *testing.T) {
	ctx, pool, queries, service, cleanup := newServiceTestDeps(t)
	defer cleanup()

	userID := createLedgerTestUser(t, ctx, pool)
	defer cleanupLedgerTestUser(t, context.Background(), pool, userID)
	requestRecordID := createLedgerTestRequestRecord(t, ctx, pool, userID)

	if _, err := service.Credit(ctx, CreditParams{
		UserID:         userID,
		Amount:         numeric(100),
		Currency:       "USD",
		IdempotencyKey: fmt.Sprintf("preauthorize-seed-credit-%d", time.Now().UnixNano()),
		Reason:         "seed preauthorize balance",
	}); err != nil {
		t.Fatalf("seed credit: %v", err)
	}

	params := PreAuthorizeParams{
		UserID:          userID,
		RequestRecordID: requestRecordID,
		EstimatedAmount: numeric(40),
		Currency:        "USD",
		IdempotencyKey:  fmt.Sprintf("preauthorize-%d", time.Now().UnixNano()),
		Reason:          "test preauthorize",
	}

	created, err := service.PreAuthorize(ctx, params)
	if err != nil {
		t.Fatalf("preauthorize: %v", err)
	}
	if created.Status != ReservationStatusAuthorized {
		t.Fatalf("expected authorized reservation, got %q", created.Status)
	}
	if created.UserID != userID {
		t.Fatalf("expected user id %d, got %d", userID, created.UserID)
	}
	if created.RequestRecordID != requestRecordID {
		t.Fatalf("expected request record id %d, got %d", requestRecordID, created.RequestRecordID)
	}
	assertNumericEquals(t, created.EstimatedAmount, 40)
	assertNumericEquals(t, created.AuthorizedAmount, 40)
	assertNumericEquals(t, created.CapturedAmount, 0)
	assertNumericEquals(t, created.ReleasedAmount, 0)

	balance, err := queries.GetUserBalance(ctx, sqlc.GetUserBalanceParams{
		UserID:   userID,
		Currency: "USD",
	})
	if err != nil {
		t.Fatalf("get balance after preauthorize: %v", err)
	}
	assertNumericEquals(t, balance.Balance, 100)
	assertNumericEquals(t, balance.ReservedBalance, 40)

	repeated, err := service.PreAuthorize(ctx, params)
	if err != nil {
		t.Fatalf("repeat preauthorize: %v", err)
	}
	if repeated.ID != created.ID {
		t.Fatalf("expected idempotent reservation id %d, got %d", created.ID, repeated.ID)
	}

	balance, err = queries.GetUserBalance(ctx, sqlc.GetUserBalanceParams{
		UserID:   userID,
		Currency: "USD",
	})
	if err != nil {
		t.Fatalf("get balance after repeated preauthorize: %v", err)
	}
	assertNumericEquals(t, balance.Balance, 100)
	assertNumericEquals(t, balance.ReservedBalance, 40)
}

func TestPreAuthorizeFreezesAvailableBalanceWhenBelowEstimate(t *testing.T) {
	ctx, pool, queries, service, cleanup := newServiceTestDeps(t)
	defer cleanup()

	userID := createLedgerTestUser(t, ctx, pool)
	defer cleanupLedgerTestUser(t, context.Background(), pool, userID)
	requestRecordID := createLedgerTestRequestRecord(t, ctx, pool, userID)

	if _, err := service.Credit(ctx, CreditParams{
		UserID:         userID,
		Amount:         numeric(30),
		Currency:       "USD",
		IdempotencyKey: fmt.Sprintf("preauthorize-insufficient-credit-%d", time.Now().UnixNano()),
		Reason:         "seed insufficient balance",
	}); err != nil {
		t.Fatalf("seed credit: %v", err)
	}

	reservation, err := service.PreAuthorize(ctx, PreAuthorizeParams{
		UserID:          userID,
		RequestRecordID: requestRecordID,
		EstimatedAmount: numeric(40),
		Currency:        "USD",
		IdempotencyKey:  fmt.Sprintf("preauthorize-partial-%d", time.Now().UnixNano()),
		Reason:          "test partial preauthorize",
	})
	if err != nil {
		t.Fatalf("preauthorize partial balance: %v", err)
	}
	assertNumericEquals(t, reservation.EstimatedAmount, 40)
	assertNumericEquals(t, reservation.AuthorizedAmount, 30)

	balance, err := queries.GetUserBalance(ctx, sqlc.GetUserBalanceParams{
		UserID:   userID,
		Currency: "USD",
	})
	if err != nil {
		t.Fatalf("get balance after partial preauthorize: %v", err)
	}
	assertNumericEquals(t, balance.Balance, 30)
	assertNumericEquals(t, balance.ReservedBalance, 30)
}

func TestPreAuthorizeReturnsInsufficientBalanceWithoutReservation(t *testing.T) {
	ctx, pool, queries, service, cleanup := newServiceTestDeps(t)
	defer cleanup()

	userID := createLedgerTestUser(t, ctx, pool)
	defer cleanupLedgerTestUser(t, context.Background(), pool, userID)
	requestRecordID := createLedgerTestRequestRecord(t, ctx, pool, userID)

	_, err := service.PreAuthorize(ctx, PreAuthorizeParams{
		UserID:          userID,
		RequestRecordID: requestRecordID,
		EstimatedAmount: numeric(40),
		Currency:        "USD",
		IdempotencyKey:  fmt.Sprintf("preauthorize-insufficient-%d", time.Now().UnixNano()),
		Reason:          "test insufficient preauthorize",
	})
	if !errors.Is(err, ErrInsufficientBalance) {
		t.Fatalf("expected ErrInsufficientBalance, got %v", err)
	}
	if got := failure.CodeOf(err); got != failure.CodeLedgerInsufficientBalance {
		t.Fatalf("expected failure code %q, got %q", failure.CodeLedgerInsufficientBalance, got)
	}

	if _, err := queries.GetLedgerReservationByRequestRecordID(ctx, requestRecordID); err == nil {
		t.Fatal("expected no reservation after failed preauthorize")
	}
}

func TestPreAuthorizeRejectsInvalidAmount(t *testing.T) {
	_, _, _, service, cleanup := newServiceTestDeps(t)
	defer cleanup()

	for _, amount := range []pgtype.Numeric{numeric(0), numeric(-1), {}} {
		_, err := service.PreAuthorize(context.Background(), PreAuthorizeParams{
			UserID:          1,
			RequestRecordID: 1,
			EstimatedAmount: amount,
			Currency:        "USD",
			IdempotencyKey:  fmt.Sprintf("preauthorize-invalid-%d", time.Now().UnixNano()),
			Reason:          "invalid preauthorize amount",
		})
		if !errors.Is(err, ErrInvalidAmount) {
			t.Fatalf("expected ErrInvalidAmount for amount %+v, got %v", amount, err)
		}
		if got := failure.CodeOf(err); got != failure.CodeLedgerInvalidAmount {
			t.Fatalf("expected failure code %q, got %q", failure.CodeLedgerInvalidAmount, got)
		}
	}
}

func TestPreAuthorizeIdempotencyKeyConflictReturnsDomainError(t *testing.T) {
	ctx, pool, _, service, cleanup := newServiceTestDeps(t)
	defer cleanup()

	userID := createLedgerTestUser(t, ctx, pool)
	defer cleanupLedgerTestUser(t, context.Background(), pool, userID)
	requestRecordID := createLedgerTestRequestRecord(t, ctx, pool, userID)

	if _, err := service.Credit(ctx, CreditParams{
		UserID:         userID,
		Amount:         numeric(100),
		Currency:       "USD",
		IdempotencyKey: fmt.Sprintf("preauthorize-conflict-credit-%d", time.Now().UnixNano()),
		Reason:         "seed preauthorize conflict balance",
	}); err != nil {
		t.Fatalf("seed credit: %v", err)
	}

	key := fmt.Sprintf("preauthorize-conflict-%d", time.Now().UnixNano())
	if _, err := service.PreAuthorize(ctx, PreAuthorizeParams{
		UserID:          userID,
		RequestRecordID: requestRecordID,
		EstimatedAmount: numeric(40),
		Currency:        "USD",
		IdempotencyKey:  key,
		Reason:          "initial preauthorize",
	}); err != nil {
		t.Fatalf("initial preauthorize: %v", err)
	}

	_, err := service.PreAuthorize(ctx, PreAuthorizeParams{
		UserID:          userID,
		RequestRecordID: requestRecordID,
		EstimatedAmount: numeric(50),
		Currency:        "USD",
		IdempotencyKey:  key,
		Reason:          "conflicting preauthorize amount",
	})
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("expected ErrIdempotencyConflict for different amount, got %v", err)
	}
	if got := failure.CodeOf(err); got != failure.CodeLedgerIdempotencyConflict {
		t.Fatalf("expected failure code %q, got %q", failure.CodeLedgerIdempotencyConflict, got)
	}
}

func TestPreAuthorizeRequestConflictReturnsDomainError(t *testing.T) {
	ctx, pool, _, service, cleanup := newServiceTestDeps(t)
	defer cleanup()

	userID := createLedgerTestUser(t, ctx, pool)
	defer cleanupLedgerTestUser(t, context.Background(), pool, userID)
	requestRecordID := createLedgerTestRequestRecord(t, ctx, pool, userID)

	if _, err := service.Credit(ctx, CreditParams{
		UserID:         userID,
		Amount:         numeric(100),
		Currency:       "USD",
		IdempotencyKey: fmt.Sprintf("preauthorize-request-conflict-credit-%d", time.Now().UnixNano()),
		Reason:         "seed preauthorize request conflict balance",
	}); err != nil {
		t.Fatalf("seed credit: %v", err)
	}

	if _, err := service.PreAuthorize(ctx, PreAuthorizeParams{
		UserID:          userID,
		RequestRecordID: requestRecordID,
		EstimatedAmount: numeric(40),
		Currency:        "USD",
		IdempotencyKey:  fmt.Sprintf("preauthorize-request-conflict-a-%d", time.Now().UnixNano()),
		Reason:          "initial preauthorize",
	}); err != nil {
		t.Fatalf("initial preauthorize: %v", err)
	}

	_, err := service.PreAuthorize(ctx, PreAuthorizeParams{
		UserID:          userID,
		RequestRecordID: requestRecordID,
		EstimatedAmount: numeric(40),
		Currency:        "USD",
		IdempotencyKey:  fmt.Sprintf("preauthorize-request-conflict-b-%d", time.Now().UnixNano()),
		Reason:          "conflicting request preauthorize",
	})
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("expected ErrIdempotencyConflict for duplicate request reservation, got %v", err)
	}
	if got := failure.CodeOf(err); got != failure.CodeLedgerIdempotencyConflict {
		t.Fatalf("expected failure code %q, got %q", failure.CodeLedgerIdempotencyConflict, got)
	}
}

func TestConcurrentSamePreAuthorizeIdempotencyKeyDoesNotDoubleReserve(t *testing.T) {
	ctx, pool, queries, service, cleanup := newServiceTestDeps(t)
	defer cleanup()

	userID := createLedgerTestUser(t, ctx, pool)
	defer cleanupLedgerTestUser(t, context.Background(), pool, userID)
	requestRecordID := createLedgerTestRequestRecord(t, ctx, pool, userID)

	if _, err := service.Credit(ctx, CreditParams{
		UserID:         userID,
		Amount:         numeric(100),
		Currency:       "USD",
		IdempotencyKey: fmt.Sprintf("concurrent-preauthorize-credit-%d", time.Now().UnixNano()),
		Reason:         "seed concurrent preauthorize balance",
	}); err != nil {
		t.Fatalf("seed credit: %v", err)
	}

	params := PreAuthorizeParams{
		UserID:          userID,
		RequestRecordID: requestRecordID,
		EstimatedAmount: numeric(40),
		Currency:        "USD",
		IdempotencyKey:  fmt.Sprintf("concurrent-preauthorize-%d", time.Now().UnixNano()),
		Reason:          "concurrent preauthorize",
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make(chan Reservation, 2)
	errs := make(chan error, 2)

	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			<-start
			reservation, err := service.PreAuthorize(context.Background(), params)
			if err != nil {
				errs <- err
				return
			}
			results <- reservation
		}()
	}

	close(start)
	wg.Wait()
	close(results)
	close(errs)

	if len(errs) != 0 {
		for err := range errs {
			t.Fatalf("concurrent preauthorize failed: %v", err)
		}
	}

	var reservations []Reservation
	for reservation := range results {
		reservations = append(reservations, reservation)
	}
	if len(reservations) != 2 {
		t.Fatalf("expected 2 successful preauthorize results, got %d", len(reservations))
	}
	if reservations[0].ID != reservations[1].ID {
		t.Fatalf("expected concurrent idempotent preauthorizations to return same reservation id, got %d and %d", reservations[0].ID, reservations[1].ID)
	}

	balance, err := queries.GetUserBalance(ctx, sqlc.GetUserBalanceParams{
		UserID:   userID,
		Currency: "USD",
	})
	if err != nil {
		t.Fatalf("get balance after concurrent preauthorize: %v", err)
	}
	assertNumericEquals(t, balance.Balance, 100)
	assertNumericEquals(t, balance.ReservedBalance, 40)
}

func TestCaptureWritesOffActualAmountAboveAuthorization(t *testing.T) {
	ctx, pool, queries, service, cleanup := newServiceTestDeps(t)
	defer cleanup()

	userID := createLedgerTestUser(t, ctx, pool)
	defer cleanupLedgerTestUser(t, context.Background(), pool, userID)
	requestRecordID := createLedgerTestRequestRecord(t, ctx, pool, userID)

	if _, err := service.Credit(ctx, CreditParams{
		UserID:         userID,
		Amount:         numeric(80),
		Currency:       "USD",
		IdempotencyKey: fmt.Sprintf("capture-writeoff-credit-%d", time.Now().UnixNano()),
		Reason:         "seed write off balance",
	}); err != nil {
		t.Fatalf("seed credit: %v", err)
	}

	reservation, err := service.PreAuthorize(ctx, PreAuthorizeParams{
		UserID:          userID,
		RequestRecordID: requestRecordID,
		EstimatedAmount: numeric(100),
		Currency:        "USD",
		IdempotencyKey:  fmt.Sprintf("capture-writeoff-preauthorize-%d", time.Now().UnixNano()),
		Reason:          "test write off preauthorize",
	})
	if err != nil {
		t.Fatalf("preauthorize partial balance: %v", err)
	}
	assertNumericEquals(t, reservation.EstimatedAmount, 100)
	assertNumericEquals(t, reservation.AuthorizedAmount, 80)

	captureParams := CaptureParams{
		RequestRecordID: requestRecordID,
		ReservationID:   &reservation.ID,
		ActualAmount:    numeric(100),
		IdempotencyKey:  fmt.Sprintf("capture-writeoff-%d", time.Now().UnixNano()),
		Reason:          "test write off capture",
	}
	captured, err := service.Capture(ctx, captureParams)
	if err != nil {
		t.Fatalf("capture with write off: %v", err)
	}
	if captured.Status != ReservationStatusCaptured {
		t.Fatalf("expected captured reservation, got %q", captured.Status)
	}
	assertNumericEquals(t, captured.CapturedAmount, 80)
	assertNumericEquals(t, captured.ReleasedAmount, 0)

	balance, err := queries.GetUserBalance(ctx, sqlc.GetUserBalanceParams{
		UserID:   userID,
		Currency: "USD",
	})
	if err != nil {
		t.Fatalf("get balance after capture write off: %v", err)
	}
	assertNumericEquals(t, balance.Balance, 0)
	assertNumericEquals(t, balance.ReservedBalance, 0)

	entry, err := queries.GetLedgerEntryByIdempotencyKey(ctx, captureParams.IdempotencyKey)
	if err != nil {
		t.Fatalf("get capture ledger entry: %v", err)
	}
	assertNumericEquals(t, entry.Amount, 80)
	assertNumericEquals(t, entry.BalanceBefore, 80)
	assertNumericEquals(t, entry.BalanceAfter, 0)

	writeOff, err := queries.GetLedgerBillingExceptionByReservationID(ctx, reservation.ID)
	if err != nil {
		t.Fatalf("get ledger billing exception: %v", err)
	}
	if writeOff.EventType != string(BillingExceptionEventTypeWriteOff) {
		t.Fatalf("expected write off event type, got %q", writeOff.EventType)
	}
	assertNumericEquals(t, writeOff.ActualAmount, 100)
	assertNumericEquals(t, writeOff.CapturedAmount, 80)
	assertNumericEquals(t, writeOff.PlatformAmount, 20)
	if writeOff.ReasonCode != "authorization_underfunded" {
		t.Fatalf("expected write off reason authorization_underfunded, got %q", writeOff.ReasonCode)
	}

	repeated, err := service.Capture(ctx, captureParams)
	if err != nil {
		t.Fatalf("repeat capture with write off: %v", err)
	}
	if repeated.ID != captured.ID {
		t.Fatalf("expected idempotent capture reservation id %d, got %d", captured.ID, repeated.ID)
	}
}

func TestCaptureCollectsOverageFromAvailableBalanceWithoutWriteOff(t *testing.T) {
	ctx, pool, queries, service, cleanup := newServiceTestDeps(t)
	defer cleanup()

	userID := createLedgerTestUser(t, ctx, pool)
	defer cleanupLedgerTestUser(t, context.Background(), pool, userID)
	requestRecordID := createLedgerTestRequestRecord(t, ctx, pool, userID)

	// 余额充足（200），按模型保守估算只冻结 80；真实费用 100 的超额应从未冻结可用余额二次补扣，不动用平台核销。
	if _, err := service.Credit(ctx, CreditParams{
		UserID:         userID,
		Amount:         numeric(200),
		Currency:       "USD",
		IdempotencyKey: fmt.Sprintf("overage-full-credit-%d", time.Now().UnixNano()),
		Reason:         "seed overage balance",
	}); err != nil {
		t.Fatalf("seed credit: %v", err)
	}

	reservation, err := service.PreAuthorize(ctx, PreAuthorizeParams{
		UserID:          userID,
		RequestRecordID: requestRecordID,
		EstimatedAmount: numeric(80),
		Currency:        "USD",
		IdempotencyKey:  fmt.Sprintf("overage-full-preauthorize-%d", time.Now().UnixNano()),
		Reason:          "test overage preauthorize",
	})
	if err != nil {
		t.Fatalf("preauthorize: %v", err)
	}
	assertNumericEquals(t, reservation.AuthorizedAmount, 80)

	captureParams := CaptureParams{
		RequestRecordID: requestRecordID,
		ReservationID:   &reservation.ID,
		ActualAmount:    numeric(100),
		IdempotencyKey:  fmt.Sprintf("overage-full-capture-%d", time.Now().UnixNano()),
		Reason:          "test overage capture",
	}
	captured, err := service.Capture(ctx, captureParams)
	if err != nil {
		t.Fatalf("capture with overage: %v", err)
	}
	if captured.Status != ReservationStatusCaptured {
		t.Fatalf("expected captured reservation, got %q", captured.Status)
	}
	// reservation 行只记录冻结内扣费；超额补扣不写回 reservation。
	assertNumericEquals(t, captured.CapturedAmount, 80)
	assertNumericEquals(t, captured.OverageCapturedAmount, 20)

	balance, err := queries.GetUserBalance(ctx, sqlc.GetUserBalanceParams{UserID: userID, Currency: "USD"})
	if err != nil {
		t.Fatalf("get balance after overage capture: %v", err)
	}
	// 200 - 80(冻结扣费) - 20(超额补扣) = 100，余额非负且无平台损失。
	assertNumericEquals(t, balance.Balance, 100)
	assertNumericEquals(t, balance.ReservedBalance, 0)

	captureEntry, err := queries.GetLedgerEntryByIdempotencyKey(ctx, captureParams.IdempotencyKey)
	if err != nil {
		t.Fatalf("get capture ledger entry: %v", err)
	}
	assertNumericEquals(t, captureEntry.Amount, 80)
	assertNumericEquals(t, captureEntry.BalanceBefore, 200)
	assertNumericEquals(t, captureEntry.BalanceAfter, 120)

	overageEntry, err := queries.GetLedgerEntryByIdempotencyKey(ctx, captureParams.IdempotencyKey+":overage")
	if err != nil {
		t.Fatalf("get overage ledger entry: %v", err)
	}
	assertNumericEquals(t, overageEntry.Amount, 20)
	assertNumericEquals(t, overageEntry.BalanceBefore, 120)
	assertNumericEquals(t, overageEntry.BalanceAfter, 100)

	if _, err := queries.GetLedgerBillingExceptionByReservationID(ctx, reservation.ID); err == nil {
		t.Fatal("expected no write off exception when overage fully collected")
	}

	// 幂等重放：金额、流水都不应重复。
	repeated, err := service.Capture(ctx, captureParams)
	if err != nil {
		t.Fatalf("repeat overage capture: %v", err)
	}
	if repeated.ID != captured.ID {
		t.Fatalf("expected idempotent capture id %d, got %d", captured.ID, repeated.ID)
	}
	assertNumericEquals(t, repeated.OverageCapturedAmount, 20)

	balance, err = queries.GetUserBalance(ctx, sqlc.GetUserBalanceParams{UserID: userID, Currency: "USD"})
	if err != nil {
		t.Fatalf("get balance after repeated overage capture: %v", err)
	}
	assertNumericEquals(t, balance.Balance, 100)
}

func TestCapturePartiallyCollectsOverageThenWritesOffResidual(t *testing.T) {
	ctx, pool, queries, service, cleanup := newServiceTestDeps(t)
	defer cleanup()

	userID := createLedgerTestUser(t, ctx, pool)
	defer cleanupLedgerTestUser(t, context.Background(), pool, userID)
	requestRecordID := createLedgerTestRequestRecord(t, ctx, pool, userID)

	// 余额 90：冻结 80，真实费用 100。超额 20 中只有 10 可二次补扣（清空可用余额），剩余 10 平台核销。
	if _, err := service.Credit(ctx, CreditParams{
		UserID:         userID,
		Amount:         numeric(90),
		Currency:       "USD",
		IdempotencyKey: fmt.Sprintf("overage-partial-credit-%d", time.Now().UnixNano()),
		Reason:         "seed partial overage balance",
	}); err != nil {
		t.Fatalf("seed credit: %v", err)
	}

	reservation, err := service.PreAuthorize(ctx, PreAuthorizeParams{
		UserID:          userID,
		RequestRecordID: requestRecordID,
		EstimatedAmount: numeric(80),
		Currency:        "USD",
		IdempotencyKey:  fmt.Sprintf("overage-partial-preauthorize-%d", time.Now().UnixNano()),
		Reason:          "test partial overage preauthorize",
	})
	if err != nil {
		t.Fatalf("preauthorize: %v", err)
	}
	assertNumericEquals(t, reservation.AuthorizedAmount, 80)

	captureParams := CaptureParams{
		RequestRecordID: requestRecordID,
		ReservationID:   &reservation.ID,
		ActualAmount:    numeric(100),
		IdempotencyKey:  fmt.Sprintf("overage-partial-capture-%d", time.Now().UnixNano()),
		Reason:          "test partial overage capture",
	}
	captured, err := service.Capture(ctx, captureParams)
	if err != nil {
		t.Fatalf("capture with partial overage: %v", err)
	}
	assertNumericEquals(t, captured.CapturedAmount, 80)
	assertNumericEquals(t, captured.OverageCapturedAmount, 10)

	balance, err := queries.GetUserBalance(ctx, sqlc.GetUserBalanceParams{UserID: userID, Currency: "USD"})
	if err != nil {
		t.Fatalf("get balance after partial overage capture: %v", err)
	}
	// 90 - 80 - 10 = 0，可用余额清空但永不为负。
	assertNumericEquals(t, balance.Balance, 0)
	assertNumericEquals(t, balance.ReservedBalance, 0)

	overageEntry, err := queries.GetLedgerEntryByIdempotencyKey(ctx, captureParams.IdempotencyKey+":overage")
	if err != nil {
		t.Fatalf("get partial overage ledger entry: %v", err)
	}
	assertNumericEquals(t, overageEntry.Amount, 10)

	writeOff, err := queries.GetLedgerBillingExceptionByReservationID(ctx, reservation.ID)
	if err != nil {
		t.Fatalf("get write off exception: %v", err)
	}
	if writeOff.EventType != string(BillingExceptionEventTypeWriteOff) {
		t.Fatalf("expected write off event type, got %q", writeOff.EventType)
	}
	assertNumericEquals(t, writeOff.ActualAmount, 100)
	// captured_amount 记录用户真实承担总额（冻结内 80 + 超额补扣 10）。
	assertNumericEquals(t, writeOff.CapturedAmount, 90)
	// 平台只核销补扣后仍不可回收的残差 10。
	assertNumericEquals(t, writeOff.PlatformAmount, 10)

	// 幂等重放。
	repeated, err := service.Capture(ctx, captureParams)
	if err != nil {
		t.Fatalf("repeat partial overage capture: %v", err)
	}
	if repeated.ID != captured.ID {
		t.Fatalf("expected idempotent capture id %d, got %d", captured.ID, repeated.ID)
	}
	assertNumericEquals(t, repeated.OverageCapturedAmount, 10)

	balance, err = queries.GetUserBalance(ctx, sqlc.GetUserBalanceParams{UserID: userID, Currency: "USD"})
	if err != nil {
		t.Fatalf("get balance after repeated partial overage capture: %v", err)
	}
	assertNumericEquals(t, balance.Balance, 0)
}

func TestReleaseWithBillingExceptionRecordsRiskExposure(t *testing.T) {
	ctx, pool, queries, service, cleanup := newServiceTestDeps(t)
	defer cleanup()

	userID := createLedgerTestUser(t, ctx, pool)
	defer cleanupLedgerTestUser(t, context.Background(), pool, userID)
	requestRecordID := createLedgerTestRequestRecord(t, ctx, pool, userID)

	if _, err := service.Credit(ctx, CreditParams{
		UserID:         userID,
		Amount:         numeric(50),
		Currency:       "USD",
		IdempotencyKey: fmt.Sprintf("risk-exposure-credit-%d", time.Now().UnixNano()),
		Reason:         "seed risk exposure balance",
	}); err != nil {
		t.Fatalf("seed credit: %v", err)
	}

	reservation, err := service.PreAuthorize(ctx, PreAuthorizeParams{
		UserID:          userID,
		RequestRecordID: requestRecordID,
		EstimatedAmount: numeric(40),
		Currency:        "USD",
		IdempotencyKey:  fmt.Sprintf("risk-exposure-preauthorize-%d", time.Now().UnixNano()),
		Reason:          "test risk exposure preauthorize",
	})
	if err != nil {
		t.Fatalf("preauthorize: %v", err)
	}

	released, err := service.ReleaseWithBillingException(ctx, ReleaseWithBillingExceptionParams{
		RequestRecordID: requestRecordID,
		ReservationID:   &reservation.ID,
		ReasonCode:      "stream_final_usage_missing",
		Reason:          "stream ended without final usage",
	})
	if err != nil {
		t.Fatalf("release with billing exception: %v", err)
	}
	if released.Status != ReservationStatusReleased {
		t.Fatalf("expected released reservation, got %q", released.Status)
	}
	assertNumericEquals(t, released.CapturedAmount, 0)
	assertNumericEquals(t, released.ReleasedAmount, 40)

	balance, err := queries.GetUserBalance(ctx, sqlc.GetUserBalanceParams{
		UserID:   userID,
		Currency: "USD",
	})
	if err != nil {
		t.Fatalf("get balance after risk exposure release: %v", err)
	}
	assertNumericEquals(t, balance.Balance, 50)
	assertNumericEquals(t, balance.ReservedBalance, 0)

	exception, err := queries.GetLedgerBillingExceptionByReservationID(ctx, reservation.ID)
	if err != nil {
		t.Fatalf("get ledger billing exception: %v", err)
	}
	if exception.EventType != string(BillingExceptionEventTypeRiskExposure) {
		t.Fatalf("expected risk exposure event type, got %q", exception.EventType)
	}
	if exception.ActualAmount.Valid {
		t.Fatalf("expected nil actual amount for risk exposure, got %#v", exception.ActualAmount)
	}
	assertNumericEquals(t, exception.CapturedAmount, 0)
	assertNumericEquals(t, exception.PlatformAmount, 40)
	if exception.ReasonCode != "stream_final_usage_missing" {
		t.Fatalf("expected stream_final_usage_missing reason code, got %q", exception.ReasonCode)
	}

	repeated, err := service.ReleaseWithBillingException(ctx, ReleaseWithBillingExceptionParams{
		RequestRecordID: requestRecordID,
		ReservationID:   &reservation.ID,
		ReasonCode:      "stream_final_usage_missing",
		Reason:          "stream ended without final usage",
	})
	if err != nil {
		t.Fatalf("repeat release with billing exception: %v", err)
	}
	if repeated.ID != released.ID {
		t.Fatalf("expected idempotent release reservation id %d, got %d", released.ID, repeated.ID)
	}
}

func TestCreditCreatesBalanceAndIsIdempotent(t *testing.T) {
	ctx, pool, queries, service, cleanup := newServiceTestDeps(t)
	defer cleanup()

	userID := createLedgerTestUser(t, ctx, pool)
	defer cleanupLedgerTestUser(t, context.Background(), pool, userID)

	params := CreditParams{
		UserID:         userID,
		Amount:         numeric(100),
		Currency:       "USD",
		IdempotencyKey: fmt.Sprintf("credit-%d", time.Now().UnixNano()),
		Reason:         "test credit",
	}

	created, err := service.Credit(ctx, params)
	if err != nil {
		t.Fatalf("credit: %v", err)
	}
	if created.EntryType != EntryTypeCredit {
		t.Fatalf("expected credit entry type, got %q", created.EntryType)
	}
	if created.UserID != userID {
		t.Fatalf("expected user id %d, got %d", userID, created.UserID)
	}
	if created.RequestRecordID != nil {
		t.Fatalf("expected nil request record id, got %d", *created.RequestRecordID)
	}
	assertNumericEquals(t, created.Amount, 100)
	assertNumericEquals(t, created.BalanceBefore, 0)
	assertNumericEquals(t, created.BalanceAfter, 100)

	balance, err := queries.GetUserBalance(ctx, sqlc.GetUserBalanceParams{
		UserID:   userID,
		Currency: "USD",
	})
	if err != nil {
		t.Fatalf("get balance after credit: %v", err)
	}
	assertNumericEquals(t, balance.Balance, 100)

	repeated, err := service.Credit(ctx, params)
	if err != nil {
		t.Fatalf("repeat credit: %v", err)
	}
	if repeated.ID != created.ID {
		t.Fatalf("expected idempotent credit entry id %d, got %d", created.ID, repeated.ID)
	}

	balance, err = queries.GetUserBalance(ctx, sqlc.GetUserBalanceParams{
		UserID:   userID,
		Currency: "USD",
	})
	if err != nil {
		t.Fatalf("get balance after repeated credit: %v", err)
	}
	assertNumericEquals(t, balance.Balance, 100)

	entries, err := queries.ListLedgerEntriesByUser(ctx, sqlc.ListLedgerEntriesByUserParams{
		UserID:     userID,
		Currency:   "USD",
		LimitRows:  10,
		OffsetRows: 0,
	})
	if err != nil {
		t.Fatalf("list ledger entries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 ledger entry after idempotent credit, got %d", len(entries))
	}
}

func TestDebitSubtractsBalanceAndIsIdempotent(t *testing.T) {
	ctx, pool, queries, service, cleanup := newServiceTestDeps(t)
	defer cleanup()

	userID := createLedgerTestUser(t, ctx, pool)
	defer cleanupLedgerTestUser(t, context.Background(), pool, userID)

	if _, err := service.Credit(ctx, CreditParams{
		UserID:         userID,
		Amount:         numeric(100),
		Currency:       "USD",
		IdempotencyKey: fmt.Sprintf("seed-credit-%d", time.Now().UnixNano()),
		Reason:         "seed balance",
	}); err != nil {
		t.Fatalf("seed credit: %v", err)
	}

	params := DebitParams{
		UserID:         userID,
		Amount:         numeric(35),
		Currency:       "USD",
		IdempotencyKey: fmt.Sprintf("debit-%d", time.Now().UnixNano()),
		Reason:         "test debit",
	}

	created, err := service.Debit(ctx, params)
	if err != nil {
		t.Fatalf("debit: %v", err)
	}
	if created.EntryType != EntryTypeDebit {
		t.Fatalf("expected debit entry type, got %q", created.EntryType)
	}
	assertNumericEquals(t, created.Amount, 35)
	assertNumericEquals(t, created.BalanceBefore, 100)
	assertNumericEquals(t, created.BalanceAfter, 65)

	balance, err := queries.GetUserBalance(ctx, sqlc.GetUserBalanceParams{
		UserID:   userID,
		Currency: "USD",
	})
	if err != nil {
		t.Fatalf("get balance after debit: %v", err)
	}
	assertNumericEquals(t, balance.Balance, 65)

	repeated, err := service.Debit(ctx, params)
	if err != nil {
		t.Fatalf("repeat debit: %v", err)
	}
	if repeated.ID != created.ID {
		t.Fatalf("expected idempotent debit entry id %d, got %d", created.ID, repeated.ID)
	}

	balance, err = queries.GetUserBalance(ctx, sqlc.GetUserBalanceParams{
		UserID:   userID,
		Currency: "USD",
	})
	if err != nil {
		t.Fatalf("get balance after repeated debit: %v", err)
	}
	assertNumericEquals(t, balance.Balance, 65)
}

func TestIdempotencyKeyConflictReturnsDomainError(t *testing.T) {
	ctx, pool, _, service, cleanup := newServiceTestDeps(t)
	defer cleanup()

	userID := createLedgerTestUser(t, ctx, pool)
	defer cleanupLedgerTestUser(t, context.Background(), pool, userID)

	key := fmt.Sprintf("conflict-%d", time.Now().UnixNano())
	if _, err := service.Credit(ctx, CreditParams{
		UserID:         userID,
		Amount:         numeric(100),
		Currency:       "USD",
		IdempotencyKey: key,
		Reason:         "initial credit",
	}); err != nil {
		t.Fatalf("initial credit: %v", err)
	}

	_, err := service.Credit(ctx, CreditParams{
		UserID:         userID,
		Amount:         numeric(200),
		Currency:       "USD",
		IdempotencyKey: key,
		Reason:         "conflicting amount",
	})
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("expected ErrIdempotencyConflict for different amount, got %v", err)
	}

	_, err = service.Debit(ctx, DebitParams{
		UserID:         userID,
		Amount:         numeric(100),
		Currency:       "USD",
		IdempotencyKey: key,
		Reason:         "conflicting entry type",
	})
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("expected ErrIdempotencyConflict for different entry type, got %v", err)
	}
}

func TestConcurrentSameDebitIdempotencyKeyDoesNotDoubleCharge(t *testing.T) {
	ctx, pool, queries, service, cleanup := newServiceTestDeps(t)
	defer cleanup()

	userID := createLedgerTestUser(t, ctx, pool)
	defer cleanupLedgerTestUser(t, context.Background(), pool, userID)

	if _, err := service.Credit(ctx, CreditParams{
		UserID:         userID,
		Amount:         numeric(100),
		Currency:       "USD",
		IdempotencyKey: fmt.Sprintf("concurrent-seed-credit-%d", time.Now().UnixNano()),
		Reason:         "seed concurrent balance",
	}); err != nil {
		t.Fatalf("seed credit: %v", err)
	}

	params := DebitParams{
		UserID:         userID,
		Amount:         numeric(40),
		Currency:       "USD",
		IdempotencyKey: fmt.Sprintf("concurrent-debit-%d", time.Now().UnixNano()),
		Reason:         "concurrent debit",
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make(chan Entry, 2)
	errs := make(chan error, 2)

	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			<-start
			entry, err := service.Debit(context.Background(), params)
			if err != nil {
				errs <- err
				return
			}
			results <- entry
		}()
	}

	close(start)
	wg.Wait()
	close(results)
	close(errs)

	if len(errs) != 0 {
		for err := range errs {
			t.Fatalf("concurrent debit failed: %v", err)
		}
	}

	var entries []Entry
	for entry := range results {
		entries = append(entries, entry)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 successful debit results, got %d", len(entries))
	}
	if entries[0].ID != entries[1].ID {
		t.Fatalf("expected concurrent idempotent debits to return same entry id, got %d and %d", entries[0].ID, entries[1].ID)
	}

	balance, err := queries.GetUserBalance(ctx, sqlc.GetUserBalanceParams{
		UserID:   userID,
		Currency: "USD",
	})
	if err != nil {
		t.Fatalf("get balance after concurrent debit: %v", err)
	}
	assertNumericEquals(t, balance.Balance, 60)

	entriesByUser, err := queries.ListLedgerEntriesByUser(ctx, sqlc.ListLedgerEntriesByUserParams{
		UserID:     userID,
		Currency:   "USD",
		LimitRows:  10,
		OffsetRows: 0,
	})
	if err != nil {
		t.Fatalf("list ledger entries: %v", err)
	}
	if len(entriesByUser) != 2 {
		t.Fatalf("expected seed credit and one debit entry, got %d entries", len(entriesByUser))
	}
}

func TestDebitWithQueriesConcurrentSameIdempotencyKeyDoesNotAbortExternalTransaction(t *testing.T) {
	ctx, pool, queries, service, cleanup := newServiceTestDeps(t)
	defer cleanup()

	userID := createLedgerTestUser(t, ctx, pool)
	defer cleanupLedgerTestUser(t, context.Background(), pool, userID)

	if _, err := service.Credit(ctx, CreditParams{
		UserID:         userID,
		Amount:         numeric(100),
		Currency:       "USD",
		IdempotencyKey: fmt.Sprintf("external-debit-seed-credit-%d", time.Now().UnixNano()),
		Reason:         "seed external debit balance",
	}); err != nil {
		t.Fatalf("seed credit: %v", err)
	}

	// Hold the balance row so both goroutines enter DebitWithQueries concurrently.
	// With the idempotency-key lock in place, the second transaction waits before
	// it reads or mutates balance state.
	blockerTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin balance blocker tx: %v", err)
	}
	defer func() {
		_ = blockerTx.Rollback(context.Background())
	}()
	if _, err := blockerTx.Exec(ctx, `
		SELECT id
		FROM user_balances
		WHERE user_id = $1
		  AND currency = $2
		FOR UPDATE
	`, userID, "USD"); err != nil {
		t.Fatalf("lock balance row: %v", err)
	}

	params := DebitParams{
		UserID:         userID,
		Amount:         numeric(40),
		Currency:       "USD",
		IdempotencyKey: fmt.Sprintf("external-concurrent-debit-%d", time.Now().UnixNano()),
		Reason:         "external concurrent debit",
	}

	type debitResult struct {
		entry Entry
		err   error
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make(chan debitResult, 2)

	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			<-start
			tx, err := pool.Begin(context.Background())
			if err != nil {
				results <- debitResult{err: err}
				return
			}
			txQueries := queries.WithTx(tx)

			entry, err := service.DebitWithQueries(context.Background(), txQueries, params)
			if err != nil {
				_ = tx.Rollback(context.Background())
				results <- debitResult{err: err}
				return
			}
			if err := tx.Commit(context.Background()); err != nil {
				results <- debitResult{err: err}
				return
			}

			results <- debitResult{entry: entry}
		}()
	}

	close(start)
	time.Sleep(100 * time.Millisecond)
	if err := blockerTx.Commit(ctx); err != nil {
		t.Fatalf("release balance blocker tx: %v", err)
	}

	wg.Wait()
	close(results)

	var entries []Entry
	for result := range results {
		if result.err != nil {
			t.Fatalf("external concurrent debit failed: %v", result.err)
		}
		entries = append(entries, result.entry)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 successful debit results, got %d", len(entries))
	}
	if entries[0].ID != entries[1].ID {
		t.Fatalf("expected external idempotent debits to return same entry id, got %d and %d", entries[0].ID, entries[1].ID)
	}

	balance, err := queries.GetUserBalance(ctx, sqlc.GetUserBalanceParams{
		UserID:   userID,
		Currency: "USD",
	})
	if err != nil {
		t.Fatalf("get balance after external concurrent debit: %v", err)
	}
	assertNumericEquals(t, balance.Balance, 60)

	entriesByUser, err := queries.ListLedgerEntriesByUser(ctx, sqlc.ListLedgerEntriesByUserParams{
		UserID:     userID,
		Currency:   "USD",
		LimitRows:  10,
		OffsetRows: 0,
	})
	if err != nil {
		t.Fatalf("list ledger entries: %v", err)
	}
	if len(entriesByUser) != 2 {
		t.Fatalf("expected seed credit and one external debit entry, got %d entries", len(entriesByUser))
	}
}

func TestDebitReturnsInsufficientBalanceWithoutLedgerEntry(t *testing.T) {
	ctx, pool, queries, service, cleanup := newServiceTestDeps(t)
	defer cleanup()

	userID := createLedgerTestUser(t, ctx, pool)
	defer cleanupLedgerTestUser(t, context.Background(), pool, userID)

	_, err := service.Debit(ctx, DebitParams{
		UserID:         userID,
		Amount:         numeric(1),
		Currency:       "USD",
		IdempotencyKey: fmt.Sprintf("insufficient-debit-%d", time.Now().UnixNano()),
		Reason:         "test insufficient balance",
	})
	if !errors.Is(err, ErrInsufficientBalance) {
		t.Fatalf("expected ErrInsufficientBalance, got %v", err)
	}

	entries, err := queries.ListLedgerEntriesByUser(ctx, sqlc.ListLedgerEntriesByUserParams{
		UserID:     userID,
		Currency:   "USD",
		LimitRows:  10,
		OffsetRows: 0,
	})
	if err != nil {
		t.Fatalf("list ledger entries: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no ledger entries for insufficient balance, got %d", len(entries))
	}
}
