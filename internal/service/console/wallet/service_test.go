package wallet_test

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/console/wallet"
)

type fakeStore struct {
	listRows    []sqlc.ListConsoleLedgerEntriesRow
	listParams  sqlc.ListConsoleLedgerEntriesParams
	countTotal  int64
	countCalled bool
	countParams sqlc.CountConsoleLedgerEntriesParams
}

func (f *fakeStore) ListConsoleLedgerEntries(_ context.Context, arg sqlc.ListConsoleLedgerEntriesParams) ([]sqlc.ListConsoleLedgerEntriesRow, error) {
	f.listParams = arg
	return f.listRows, nil
}

func (f *fakeStore) CountConsoleLedgerEntries(_ context.Context, arg sqlc.CountConsoleLedgerEntriesParams) (int64, error) {
	f.countCalled = true
	f.countParams = arg
	return f.countTotal, nil
}

func mustNumeric(t *testing.T, value string) pgtype.Numeric {
	t.Helper()
	rat, ok := new(big.Rat).SetString(value)
	if !ok {
		t.Fatalf("invalid numeric literal %q", value)
	}
	var numeric pgtype.Numeric
	if err := numeric.Scan(rat.FloatString(10)); err != nil {
		t.Fatalf("scan numeric %q: %v", value, err)
	}
	return numeric
}

func TestListScopesToUserAndMapsFields(t *testing.T) {
	createdAt := time.Date(2026, 8, 25, 6, 32, 0, 0, time.UTC)
	requestID := int64(42)
	store := &fakeStore{
		listRows: []sqlc.ListConsoleLedgerEntriesRow{{
			ID:              7,
			EntryType:       "adjustment_credit",
			Amount:          mustNumeric(t, "100"),
			Currency:        "USD",
			BalanceAfter:    mustNumeric(t, "214.72"),
			RequestRecordID: pgtype.Int8{Int64: requestID, Valid: true},
			Reason:          "admin adjust",
			CreatedAt:       pgtype.Timestamptz{Time: createdAt, Valid: true},
			TotalCount:      9,
		}},
	}
	service := wallet.NewService(store)

	entries, total, err := service.List(context.Background(), wallet.ListParams{
		UserID:     11,
		EntryTypes: []string{"adjustment_credit"},
		Limit:      20,
		Offset:     0,
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if store.listParams.UserID != 11 {
		t.Fatalf("user id = %d, want 11", store.listParams.UserID)
	}
	if store.listParams.Currency != "USD" {
		t.Fatalf("currency = %q, want USD", store.listParams.Currency)
	}
	if total != 9 {
		t.Fatalf("total = %d, want 9 (window count)", total)
	}
	if store.countCalled {
		t.Fatal("count should not run when the window count is available")
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	entry := entries[0]
	if entry.EntryType != "adjustment_credit" || entry.Amount != "100" {
		t.Fatalf("unexpected entry: %+v", entry)
	}
	if entry.BalanceAfter != "214.72" {
		t.Fatalf("balance after = %q, want 214.72", entry.BalanceAfter)
	}
	if entry.RequestRecordID == nil || *entry.RequestRecordID != requestID {
		t.Fatalf("request record id = %v, want %d", entry.RequestRecordID, requestID)
	}
	if !entry.CreatedAt.Equal(createdAt) {
		t.Fatalf("created at = %v, want %v", entry.CreatedAt, createdAt)
	}
}

func TestListFallsBackToCountOnEmptyTailPage(t *testing.T) {
	store := &fakeStore{countTotal: 35}
	service := wallet.NewService(store)

	entries, total, err := service.List(context.Background(), wallet.ListParams{
		UserID: 11,
		Limit:  20,
		Offset: 40,
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("entries = %d, want 0", len(entries))
	}
	if !store.countCalled {
		t.Fatal("expected count fallback for out-of-range offset")
	}
	if store.countParams.UserID != 11 {
		t.Fatalf("count user id = %d, want 11", store.countParams.UserID)
	}
	if total != 35 {
		t.Fatalf("total = %d, want 35", total)
	}
}

func TestListDefaultsLimit(t *testing.T) {
	store := &fakeStore{}
	service := wallet.NewService(store)

	if _, _, err := service.List(context.Background(), wallet.ListParams{UserID: 11}); err != nil {
		t.Fatalf("list: %v", err)
	}
	if store.listParams.PageLimit != 20 {
		t.Fatalf("page limit = %d, want default 20", store.listParams.PageLimit)
	}
	// 不筛选时默认只查资金往来类型：用量结算的 debit 不属于钱包页。
	want := []string{"credit", "cdkey_credit", "refund", "adjustment_credit", "adjustment_debit"}
	if len(store.listParams.EntryTypes) != len(want) {
		t.Fatalf("entry types = %v, want %v", store.listParams.EntryTypes, want)
	}
	for i, entryType := range want {
		if store.listParams.EntryTypes[i] != entryType {
			t.Fatalf("entry types = %v, want %v", store.listParams.EntryTypes, want)
		}
	}
}
