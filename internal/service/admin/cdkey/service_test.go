package cdkey

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	corecdkey "github.com/ThankCat/unio-gateway/internal/core/cdkey"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

// fakeTx 只实现事务终态记录；其余 pgx.Tx 方法不会被 service 调用（调用即 nil 接口 panic，暴露误用）。
type fakeTx struct {
	pgx.Tx
	committed  bool
	rolledBack bool
	commitErr  error
}

func (t *fakeTx) Commit(context.Context) error {
	if t.commitErr != nil {
		return t.commitErr
	}
	t.committed = true
	return nil
}

func (t *fakeTx) Rollback(context.Context) error {
	if !t.committed {
		t.rolledBack = true
	}
	return nil
}

type fakeDB struct {
	txs      []*fakeTx
	beginErr error
}

func (db *fakeDB) Begin(context.Context) (pgx.Tx, error) {
	if db.beginErr != nil {
		return nil, db.beginErr
	}
	tx := &fakeTx{}
	db.txs = append(db.txs, tx)
	return tx, nil
}

func (db *fakeDB) last() *fakeTx {
	if len(db.txs) == 0 {
		return nil
	}
	return db.txs[len(db.txs)-1]
}

// memoryStore 用内存表模拟 CDKEY 行；StoreForTx 返回自身，事务边界由 fakeTx 记录。
type memoryStore struct {
	rows       map[int64]sqlc.Cdkey
	nextID     int64
	createErr  error
	revoked    []int64
	deleted    []int64
	exportByID sqlc.ExportCDKeysByIDsParams
	exportAll  sqlc.ExportCDKeysByFilterParams
	listIDs    sqlc.ListCDKeyIDsParams
}

func newMemoryStore(rows ...sqlc.Cdkey) *memoryStore {
	store := &memoryStore{rows: map[int64]sqlc.Cdkey{}, nextID: 100}
	for _, row := range rows {
		store.rows[row.ID] = row
	}
	return store
}

func (m *memoryStore) StoreForTx(pgx.Tx) Store { return m }

func (m *memoryStore) CreateCDKey(_ context.Context, arg sqlc.CreateCDKeyParams) (sqlc.Cdkey, error) {
	if m.createErr != nil {
		return sqlc.Cdkey{}, m.createErr
	}
	m.nextID++
	row := sqlc.Cdkey{
		ID: m.nextID, BatchID: arg.BatchID, CodePlaintext: arg.CodePlaintext, CodeHash: arg.CodeHash,
		CodePrefix: arg.CodePrefix, CodeSuffix: arg.CodeSuffix, Amount: arg.Amount, Currency: arg.Currency, Status: arg.Status,
	}
	m.rows[row.ID] = row
	return row, nil
}

func (m *memoryStore) ListCDKeyIDs(_ context.Context, arg sqlc.ListCDKeyIDsParams) ([]int64, error) {
	m.listIDs = arg
	ids := make([]int64, 0, len(m.rows))
	for id, row := range m.rows {
		if len(arg.Statuses) > 0 {
			match := false
			for _, status := range arg.Statuses {
				if row.Status == status {
					match = true
				}
			}
			if !match {
				continue
			}
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func (m *memoryStore) ListCDKeysPage(context.Context, sqlc.ListCDKeysPageParams) ([]sqlc.ListCDKeysPageRow, error) {
	return nil, nil
}
func (m *memoryStore) CountCDKeys(context.Context, sqlc.CountCDKeysParams) (int64, error) {
	return 0, nil
}
func (m *memoryStore) GetCDKeySummary(context.Context, sqlc.GetCDKeySummaryParams) ([]sqlc.GetCDKeySummaryRow, error) {
	return nil, nil
}
func (m *memoryStore) CountCDKeyBatches(context.Context, sqlc.CountCDKeyBatchesParams) (int64, error) {
	return 0, nil
}
func (m *memoryStore) ListCDKeyRedemptionsPage(context.Context, sqlc.ListCDKeyRedemptionsPageParams) ([]sqlc.ListCDKeyRedemptionsPageRow, error) {
	return nil, nil
}
func (m *memoryStore) CountCDKeyRedemptions(context.Context, sqlc.CountCDKeyRedemptionsParams) (int64, error) {
	return 0, nil
}

func (m *memoryStore) GetCDKeyByIDForUpdate(_ context.Context, id int64) (sqlc.Cdkey, error) {
	row, ok := m.rows[id]
	if !ok {
		return sqlc.Cdkey{}, pgx.ErrNoRows
	}
	return row, nil
}

func (m *memoryStore) GetCDKeysForUpdateByIDs(_ context.Context, ids []int64) ([]sqlc.Cdkey, error) {
	out := make([]sqlc.Cdkey, 0, len(ids))
	for _, id := range ids {
		if row, ok := m.rows[id]; ok {
			out = append(out, row)
		}
	}
	return out, nil
}

func (m *memoryStore) RevokeCDKeyIfUnused(_ context.Context, id int64) (int64, error) {
	row := m.rows[id]
	if row.Status != corecdkey.StatusUnused {
		return 0, nil
	}
	row.Status = corecdkey.StatusRevoked
	m.rows[id] = row
	m.revoked = append(m.revoked, id)
	return 1, nil
}

func (m *memoryStore) DeleteCDKey(_ context.Context, id int64) (int64, error) {
	if _, ok := m.rows[id]; !ok {
		return 0, nil
	}
	delete(m.rows, id)
	m.deleted = append(m.deleted, id)
	return 1, nil
}

func (m *memoryStore) ExportCDKeysByIDs(_ context.Context, arg sqlc.ExportCDKeysByIDsParams) ([]sqlc.ExportCDKeysByIDsRow, error) {
	m.exportByID = arg
	return nil, nil
}

func (m *memoryStore) ExportCDKeysByFilter(_ context.Context, arg sqlc.ExportCDKeysByFilterParams) ([]sqlc.ExportCDKeysByFilterRow, error) {
	m.exportAll = arg
	return nil, nil
}

func cdkeyRow(id int64, status string) sqlc.Cdkey {
	return sqlc.Cdkey{
		ID: id, BatchID: pgtype.UUID{Bytes: uuid.New(), Valid: true}, Status: status, Currency: corecdkey.Currency,
		CodePlaintext: "UNIO-AAAA-BBBB-CCCC-DDDD", CodePrefix: "AAAA", CodeSuffix: "DDDD",
	}
}

func TestGenerateWritesEveryKeyInOneTransactionAndMasksCodes(t *testing.T) {
	db := &fakeDB{}
	store := newMemoryStore()
	svc := NewService(db, store)

	result, err := svc.Generate(context.Background(), GenerateParams{Items: []GenerateItem{
		{Amount: "10", Quantity: 3},
		{Amount: "50", Quantity: 2},
	}})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if result.TotalQuantity != 5 || len(result.MaskedCodes) != 5 || len(store.rows) != 5 {
		t.Fatalf("expected 5 keys, got quantity=%d masked=%d stored=%d", result.TotalQuantity, len(result.MaskedCodes), len(store.rows))
	}
	if result.TotalValue != "130" || result.Currency != corecdkey.Currency {
		t.Fatalf("total value/currency = %q %q", result.TotalValue, result.Currency)
	}
	if len(result.Lines) != 2 || result.Lines[0].Value != "30" || result.Lines[1].Value != "100" {
		t.Fatalf("lines = %+v", result.Lines)
	}
	if _, err := uuid.Parse(result.BatchID); err != nil {
		t.Fatalf("batch id must be a UUID: %v", err)
	}
	for _, masked := range result.MaskedCodes {
		if !strings.Contains(masked, "*") {
			t.Fatalf("masked code must not leak plaintext: %q", masked)
		}
	}
	seen := map[string]struct{}{}
	for _, row := range store.rows {
		if row.Status != corecdkey.StatusUnused || row.CodeHash != corecdkey.Hash(row.CodePlaintext) {
			t.Fatalf("stored row must be unused with matching hash: %+v", row)
		}
		if _, dup := seen[row.CodeHash]; dup {
			t.Fatal("generated codes must be unique")
		}
		seen[row.CodeHash] = struct{}{}
	}
	if tx := db.last(); tx == nil || !tx.committed {
		t.Fatal("generation must commit exactly one transaction")
	}
}

func TestGenerateRejectsInvalidDenominationsBeforeOpeningTransaction(t *testing.T) {
	cases := []struct {
		name  string
		items []GenerateItem
	}{
		{name: "empty", items: nil},
		{name: "unsupported amount", items: []GenerateItem{{Amount: "7", Quantity: 1}}},
		{name: "duplicate amount", items: []GenerateItem{{Amount: "10", Quantity: 1}, {Amount: "10", Quantity: 1}}},
		{name: "zero quantity", items: []GenerateItem{{Amount: "10", Quantity: 0}}},
		{name: "over max quantity", items: []GenerateItem{{Amount: "10", Quantity: corecdkey.MaxQuantity + 1}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := &fakeDB{}
			svc := NewService(db, newMemoryStore())
			_, err := svc.Generate(context.Background(), GenerateParams{Items: tc.items})
			if failure.CodeOf(err) != failure.CodeAdminInvalidArgument {
				t.Fatalf("expected invalid argument, got %v", err)
			}
			if len(db.txs) != 0 {
				t.Fatal("validation failures must not open a transaction")
			}
		})
	}
}

func TestGenerateRollsBackWhenStoreFails(t *testing.T) {
	db := &fakeDB{}
	store := newMemoryStore()
	store.createErr = errors.New("disk full")
	svc := NewService(db, store)

	_, err := svc.Generate(context.Background(), GenerateParams{Items: []GenerateItem{{Amount: "5", Quantity: 1}}})
	if failure.CodeOf(err) != failure.CodeAdminStoreFailed {
		t.Fatalf("expected store failure, got %v", err)
	}
	if tx := db.last(); tx == nil || tx.committed || !tx.rolledBack {
		t.Fatalf("store failure must roll back: %+v", tx)
	}
}

func TestRevokeOnlyTransitionsUnusedKeys(t *testing.T) {
	db := &fakeDB{}
	store := newMemoryStore(cdkeyRow(1, corecdkey.StatusUnused), cdkeyRow(2, corecdkey.StatusRedeemed), cdkeyRow(3, corecdkey.StatusRevoked))
	svc := NewService(db, store)

	if err := svc.Revoke(context.Background(), 1); err != nil {
		t.Fatalf("revoke unused: %v", err)
	}
	if store.rows[1].Status != corecdkey.StatusRevoked || !db.last().committed {
		t.Fatalf("unused key must become revoked in a committed transaction: %+v", store.rows[1])
	}

	err := svc.Revoke(context.Background(), 2)
	if failure.CodeOf(err) != failure.CodeAdminConflict {
		t.Fatalf("redeemed key must be a conflict, got %v", err)
	}
	if db.last().committed {
		t.Fatal("conflict must not commit")
	}

	// 已吊销的键幂等：不重复写、正常提交。
	if err := svc.Revoke(context.Background(), 3); err != nil {
		t.Fatalf("revoke already revoked: %v", err)
	}
	if len(store.revoked) != 1 {
		t.Fatalf("revoke must be idempotent, revoked writes = %v", store.revoked)
	}

	if err := svc.Revoke(context.Background(), 404); failure.CodeOf(err) != failure.CodeAdminNotFound {
		t.Fatalf("missing key must be not found, got %v", err)
	}
	if err := svc.Revoke(context.Background(), 0); failure.CodeOf(err) != failure.CodeAdminInvalidArgument {
		t.Fatalf("non-positive id must be invalid, got %v", err)
	}
}

func TestDeleteRefusesRedeemedKeys(t *testing.T) {
	db := &fakeDB{}
	store := newMemoryStore(cdkeyRow(1, corecdkey.StatusRevoked), cdkeyRow(2, corecdkey.StatusRedeemed))
	svc := NewService(db, store)

	if err := svc.Delete(context.Background(), 1); err != nil {
		t.Fatalf("delete revoked: %v", err)
	}
	if _, exists := store.rows[1]; exists || !db.last().committed {
		t.Fatal("revoked key must be deleted in a committed transaction")
	}
	if err := svc.Delete(context.Background(), 2); failure.CodeOf(err) != failure.CodeAdminConflict {
		t.Fatalf("redeemed key must be a conflict, got %v", err)
	}
	if _, exists := store.rows[2]; !exists {
		t.Fatal("redeemed key must survive a rejected delete")
	}
}

func TestBulkRevokeSkipsNonUnusedAndCountsPerKey(t *testing.T) {
	db := &fakeDB{}
	store := newMemoryStore(cdkeyRow(1, corecdkey.StatusUnused), cdkeyRow(2, corecdkey.StatusRedeemed), cdkeyRow(3, corecdkey.StatusRevoked), cdkeyRow(4, corecdkey.StatusUnused))
	svc := NewService(db, store)

	result, err := svc.BulkRevoke(context.Background(), []int64{1, 2, 3, 4, 4})
	if err != nil {
		t.Fatalf("bulk revoke: %v", err)
	}
	if result.Requested != 4 || result.Affected != 2 || result.Skipped != 2 {
		t.Fatalf("bulk result = %+v", result)
	}
	if store.rows[1].Status != corecdkey.StatusRevoked || store.rows[4].Status != corecdkey.StatusRevoked || store.rows[2].Status != corecdkey.StatusRedeemed {
		t.Fatalf("unexpected statuses after bulk revoke: %+v", store.rows)
	}
	if !db.last().committed {
		t.Fatal("bulk revoke must commit")
	}
}

func TestBulkDeleteIsAllOrNothingWhenSelectionContainsRedeemed(t *testing.T) {
	db := &fakeDB{}
	store := newMemoryStore(cdkeyRow(1, corecdkey.StatusUnused), cdkeyRow(2, corecdkey.StatusRedeemed))
	svc := NewService(db, store)

	_, err := svc.BulkDelete(context.Background(), []int64{1, 2})
	if failure.CodeOf(err) != failure.CodeAdminConflict {
		t.Fatalf("expected conflict, got %v", err)
	}
	if len(store.deleted) != 0 || len(store.rows) != 2 {
		t.Fatalf("no key may be deleted when the selection is rejected: deleted=%v", store.deleted)
	}
	if db.last().committed {
		t.Fatal("rejected bulk delete must not commit")
	}

	result, err := svc.BulkDelete(context.Background(), []int64{1})
	if err != nil || result.Affected != 1 {
		t.Fatalf("bulk delete unused: result=%+v err=%v", result, err)
	}
}

func TestBulkOperationsRejectMissingOrInvalidIDs(t *testing.T) {
	db := &fakeDB{}
	store := newMemoryStore(cdkeyRow(1, corecdkey.StatusUnused))
	svc := NewService(db, store)

	if _, err := svc.BulkRevoke(context.Background(), []int64{1, 999}); failure.CodeOf(err) != failure.CodeAdminNotFound {
		t.Fatalf("missing id must be not found, got %v", err)
	}
	if store.rows[1].Status != corecdkey.StatusUnused {
		t.Fatal("a not-found selection must not partially revoke")
	}
	if _, err := svc.BulkRevoke(context.Background(), []int64{0}); failure.CodeOf(err) != failure.CodeAdminInvalidArgument {
		t.Fatalf("non-positive id must be invalid, got %v", err)
	}
	if _, err := svc.BulkRevoke(context.Background(), nil); failure.CodeOf(err) != failure.CodeAdminInvalidArgument {
		t.Fatalf("empty selection must be invalid, got %v", err)
	}
}

func TestBulkRevokeSelectionByFilterResolvesIDsServerSide(t *testing.T) {
	db := &fakeDB{}
	store := newMemoryStore(cdkeyRow(1, corecdkey.StatusUnused), cdkeyRow(2, corecdkey.StatusUnused), cdkeyRow(3, corecdkey.StatusRedeemed))
	svc := NewService(db, store)

	result, err := svc.BulkRevokeSelection(context.Background(), Selection{
		Scope:      "filter",
		Filter:     ListParams{Statuses: []string{"unused"}},
		ExcludeIDs: []int64{2},
	})
	if err != nil {
		t.Fatalf("bulk revoke by filter: %v", err)
	}
	if result.Requested != 1 || result.Affected != 1 {
		t.Fatalf("filter selection result = %+v", result)
	}
	if store.rows[1].Status != corecdkey.StatusRevoked || store.rows[2].Status != corecdkey.StatusUnused {
		t.Fatalf("excluded id must stay untouched: %+v", store.rows)
	}
}

func TestExportValidatesScopeAndStatusesBeforeTouchingPlaintext(t *testing.T) {
	store := newMemoryStore()
	svc := NewService(&fakeDB{}, store)

	if _, err := svc.Export(context.Background(), ExportParams{Scope: "all"}); failure.CodeOf(err) != failure.CodeAdminInvalidArgument {
		t.Fatalf("export without statuses must be invalid, got %v", err)
	}
	if _, err := svc.Export(context.Background(), ExportParams{Scope: "selected", Statuses: []string{"unused"}}); failure.CodeOf(err) != failure.CodeAdminInvalidArgument {
		t.Fatalf("selected export without ids must be invalid, got %v", err)
	}
	if _, err := svc.Export(context.Background(), ExportParams{Scope: "weird", Statuses: []string{"unused"}}); failure.CodeOf(err) != failure.CodeAdminInvalidArgument {
		t.Fatalf("unknown scope must be invalid, got %v", err)
	}
	if _, err := svc.Export(context.Background(), ExportParams{Statuses: []string{"bogus"}}); failure.CodeOf(err) != failure.CodeAdminInvalidArgument {
		t.Fatalf("unsupported status must be invalid, got %v", err)
	}

	if _, err := svc.Export(context.Background(), ExportParams{Scope: "selected", IDs: []int64{3, 1}, Statuses: []string{"Unused", "unused", "revoked"}}); err != nil {
		t.Fatalf("selected export: %v", err)
	}
	if len(store.exportByID.Ids) != 2 || len(store.exportByID.Statuses) != 2 {
		t.Fatalf("selected export params = %+v (statuses must be normalized and de-duplicated)", store.exportByID)
	}
	if _, err := svc.Export(context.Background(), ExportParams{Statuses: []string{"unused"}}); err != nil {
		t.Fatalf("all export: %v", err)
	}
	if len(store.exportAll.Statuses) != 1 || store.exportAll.Statuses[0] != "unused" {
		t.Fatalf("all export params = %+v", store.exportAll)
	}
}
