package ledger

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

func TestAdjustCreditWritesAdjustmentEntryAndIsIdempotent(t *testing.T) {
	ctx, pool, queries, service, cleanup := newServiceTestDeps(t)
	defer cleanup()

	userID := createLedgerTestUser(t, ctx, pool)
	defer cleanupLedgerTestUser(t, context.Background(), pool, userID)

	params := AdjustParams{
		UserID:         userID,
		Amount:         numeric(50),
		Currency:       "USD",
		IdempotencyKey: fmt.Sprintf("adjust-credit-%d", time.Now().UnixNano()),
		Reason:         "manual top-up",
	}

	created, err := service.AdjustCredit(ctx, params)
	if err != nil {
		t.Fatalf("adjust credit: %v", err)
	}
	if created.EntryType != EntryTypeAdjustmentCredit {
		t.Fatalf("expected adjustment_credit entry type, got %q", created.EntryType)
	}
	// 调额是用户级动作，不挂请求。
	if created.RequestRecordID != nil {
		t.Fatalf("expected nil request record id, got %d", *created.RequestRecordID)
	}
	assertNumericEquals(t, created.Amount, 50)
	assertNumericEquals(t, created.BalanceBefore, 0)
	assertNumericEquals(t, created.BalanceAfter, 50)

	repeated, err := service.AdjustCredit(ctx, params)
	if err != nil {
		t.Fatalf("repeat adjust credit: %v", err)
	}
	if repeated.ID != created.ID {
		t.Fatalf("expected idempotent entry id %d, got %d", created.ID, repeated.ID)
	}

	balance, err := queries.GetUserBalance(ctx, sqlc.GetUserBalanceParams{
		UserID:   userID,
		Currency: "USD",
	})
	if err != nil {
		t.Fatalf("get balance after adjust credit: %v", err)
	}
	assertNumericEquals(t, balance.Balance, 50)
}

func TestAdjustDebitSubtractsBalanceAndIsIdempotent(t *testing.T) {
	ctx, pool, queries, service, cleanup := newServiceTestDeps(t)
	defer cleanup()

	userID := createLedgerTestUser(t, ctx, pool)
	defer cleanupLedgerTestUser(t, context.Background(), pool, userID)

	if _, err := service.AdjustCredit(ctx, AdjustParams{
		UserID:         userID,
		Amount:         numeric(100),
		Currency:       "USD",
		IdempotencyKey: fmt.Sprintf("seed-adjust-%d", time.Now().UnixNano()),
		Reason:         "seed balance",
	}); err != nil {
		t.Fatalf("seed adjust credit: %v", err)
	}

	params := AdjustParams{
		UserID:         userID,
		Amount:         numeric(30),
		Currency:       "USD",
		IdempotencyKey: fmt.Sprintf("adjust-debit-%d", time.Now().UnixNano()),
		Reason:         "manual deduction",
	}

	created, err := service.AdjustDebit(ctx, params)
	if err != nil {
		t.Fatalf("adjust debit: %v", err)
	}
	if created.EntryType != EntryTypeAdjustmentDebit {
		t.Fatalf("expected adjustment_debit entry type, got %q", created.EntryType)
	}
	assertNumericEquals(t, created.BalanceAfter, 70)

	repeated, err := service.AdjustDebit(ctx, params)
	if err != nil {
		t.Fatalf("repeat adjust debit: %v", err)
	}
	if repeated.ID != created.ID {
		t.Fatalf("expected idempotent entry id %d, got %d", created.ID, repeated.ID)
	}

	balance, err := queries.GetUserBalance(ctx, sqlc.GetUserBalanceParams{
		UserID:   userID,
		Currency: "USD",
	})
	if err != nil {
		t.Fatalf("get balance after adjust debit: %v", err)
	}
	assertNumericEquals(t, balance.Balance, 70)
}

func TestAdjustDebitReturnsInsufficientBalanceWithoutEntry(t *testing.T) {
	ctx, pool, queries, service, cleanup := newServiceTestDeps(t)
	defer cleanup()

	userID := createLedgerTestUser(t, ctx, pool)
	defer cleanupLedgerTestUser(t, context.Background(), pool, userID)

	_, err := service.AdjustDebit(ctx, AdjustParams{
		UserID:         userID,
		Amount:         numeric(10),
		Currency:       "USD",
		IdempotencyKey: fmt.Sprintf("adjust-debit-empty-%d", time.Now().UnixNano()),
		Reason:         "deduct without balance",
	})
	if !errors.Is(err, ErrInsufficientBalance) {
		t.Fatalf("expected ErrInsufficientBalance, got %v", err)
	}
	if failure.CodeOf(err) != failure.CodeLedgerInsufficientBalance {
		t.Fatalf("expected ledger_insufficient_balance code, got %q", failure.CodeOf(err))
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
		t.Fatalf("expected no ledger entries after failed adjust debit, got %d", len(entries))
	}
}
