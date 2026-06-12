package customer

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-api/internal/core/ledger"
	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
)

func mustNumeric(t *testing.T, s string) pgtype.Numeric {
	t.Helper()
	var n pgtype.Numeric
	if err := n.Scan(s); err != nil {
		t.Fatalf("scan numeric %q: %v", s, err)
	}
	return n
}

// --- AdjustmentService ---

type fakeAdjustLedger struct {
	creditCalled bool
	debitCalled  bool
	lastParams   ledger.AdjustParams
	entry        ledger.Entry
	err          error
}

func (f *fakeAdjustLedger) AdjustCredit(_ context.Context, p ledger.AdjustParams) (ledger.Entry, error) {
	f.creditCalled = true
	f.lastParams = p
	return f.entry, f.err
}

func (f *fakeAdjustLedger) AdjustDebit(_ context.Context, p ledger.AdjustParams) (ledger.Entry, error) {
	f.debitCalled = true
	f.lastParams = p
	return f.entry, f.err
}

func TestAdjustmentServiceCreditSuccess(t *testing.T) {
	fake := &fakeAdjustLedger{entry: ledger.Entry{
		ID:           7,
		UserID:       10,
		EntryType:    ledger.EntryTypeAdjustmentCredit,
		Amount:       mustNumeric(t, "50"),
		Currency:     "USD",
		BalanceAfter: mustNumeric(t, "150"),
		Reason:       "manual top-up",
	}}
	svc := NewAdjustmentService(fake)

	got, err := svc.Adjust(context.Background(), AdjustParams{
		UserID:    10,
		Direction: AdjustmentDirectionCredit,
		Amount:    "50",
		Currency:  "USD",
		Reason:    "manual top-up",
	})
	if err != nil {
		t.Fatalf("adjust: %v", err)
	}
	if !fake.creditCalled || fake.debitCalled {
		t.Fatalf("expected only credit to be called, credit=%v debit=%v", fake.creditCalled, fake.debitCalled)
	}
	if got.EntryID != 7 || got.Amount != "50" || got.BalanceAfter != "150" {
		t.Fatalf("unexpected adjustment view: %+v", got)
	}
	if fake.lastParams.IdempotencyKey == "" {
		t.Fatal("expected a generated idempotency key when none provided")
	}
}

func TestAdjustmentServiceDebitRoutes(t *testing.T) {
	fake := &fakeAdjustLedger{entry: ledger.Entry{EntryType: ledger.EntryTypeAdjustmentDebit, Amount: mustNumeric(t, "5"), BalanceAfter: mustNumeric(t, "0"), Currency: "USD"}}
	svc := NewAdjustmentService(fake)

	if _, err := svc.Adjust(context.Background(), AdjustParams{
		UserID:    1,
		Direction: AdjustmentDirectionDebit,
		Amount:    "5",
		Currency:  "USD",
		Reason:    "deduct",
	}); err != nil {
		t.Fatalf("adjust debit: %v", err)
	}
	if fake.creditCalled || !fake.debitCalled {
		t.Fatalf("expected only debit to be called, credit=%v debit=%v", fake.creditCalled, fake.debitCalled)
	}
}

func TestAdjustmentServiceValidation(t *testing.T) {
	cases := []struct {
		name   string
		params AdjustParams
	}{
		{"empty currency", AdjustParams{UserID: 1, Direction: "credit", Amount: "10", Currency: " ", Reason: "x"}},
		{"empty reason", AdjustParams{UserID: 1, Direction: "credit", Amount: "10", Currency: "USD", Reason: ""}},
		{"zero amount", AdjustParams{UserID: 1, Direction: "credit", Amount: "0", Currency: "USD", Reason: "x"}},
		{"negative amount", AdjustParams{UserID: 1, Direction: "credit", Amount: "-5", Currency: "USD", Reason: "x"}},
		{"bad direction", AdjustParams{UserID: 1, Direction: "transfer", Amount: "10", Currency: "USD", Reason: "x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeAdjustLedger{}
			svc := NewAdjustmentService(fake)
			_, err := svc.Adjust(context.Background(), tc.params)
			if failure.CodeOf(err) != failure.CodeAdminInvalidArgument {
				t.Fatalf("expected admin_invalid_argument, got %v (%v)", failure.CodeOf(err), err)
			}
			if fake.creditCalled || fake.debitCalled {
				t.Fatal("ledger must not be called on invalid input")
			}
		})
	}
}

// --- APIKeyService ---

type fakeAPIKeyStore struct {
	project       sqlc.Project
	projectErr    error
	created       sqlc.ApiKey
	createErr     error
	spendLimitArg sqlc.SetAPIKeySpendLimitParams
	spendLimitSet bool
}

func (f *fakeAPIKeyStore) ListAPIKeysByProjectPage(context.Context, sqlc.ListAPIKeysByProjectPageParams) ([]sqlc.ListAPIKeysByProjectPageRow, error) {
	return nil, nil
}
func (f *fakeAPIKeyStore) CountAPIKeysByProject(context.Context, int64) (int64, error) { return 0, nil }
func (f *fakeAPIKeyStore) GetAPIKeyByID(context.Context, int64) (sqlc.GetAPIKeyByIDRow, error) {
	return sqlc.GetAPIKeyByIDRow{}, nil
}
func (f *fakeAPIKeyStore) GetProjectByID(context.Context, int64) (sqlc.Project, error) {
	return f.project, f.projectErr
}
func (f *fakeAPIKeyStore) CreateAPIKey(_ context.Context, arg sqlc.CreateAPIKeyParams) (sqlc.ApiKey, error) {
	f.created.ProjectID = arg.ProjectID
	f.created.Name = arg.Name
	f.created.KeyPrefix = arg.KeyPrefix
	return f.created, f.createErr
}
func (f *fakeAPIKeyStore) SetAPIKeyDisabled(context.Context, sqlc.SetAPIKeyDisabledParams) (sqlc.SetAPIKeyDisabledRow, error) {
	return sqlc.SetAPIKeyDisabledRow{}, nil
}
func (f *fakeAPIKeyStore) RevokeAPIKey(context.Context, int64) (sqlc.RevokeAPIKeyRow, error) {
	return sqlc.RevokeAPIKeyRow{}, nil
}
func (f *fakeAPIKeyStore) SetAPIKeySpendLimit(_ context.Context, arg sqlc.SetAPIKeySpendLimitParams) (sqlc.SetAPIKeySpendLimitRow, error) {
	f.spendLimitSet = true
	f.spendLimitArg = arg
	return sqlc.SetAPIKeySpendLimitRow{
		ID:         arg.ID,
		SpendLimit: arg.SpendLimit,
	}, nil
}

func TestAPIKeyServiceCreateReturnsPlaintextAndSetsSpendLimit(t *testing.T) {
	store := &fakeAPIKeyStore{
		project: sqlc.Project{ID: 100, UserID: 10, Name: "ws"},
		created: sqlc.ApiKey{ID: 5},
	}
	svc := NewAPIKeyService(store)

	limit := "20.50"
	got, err := svc.Create(context.Background(), APIKeyCreateParams{
		ProjectID:  100,
		Name:       "ci",
		SpendLimit: &limit,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if got.Plaintext == "" {
		t.Fatal("expected one-time plaintext to be returned")
	}
	if !store.spendLimitSet {
		t.Fatal("expected spend limit to be applied after create")
	}
	if got.SpendLimit == nil || *got.SpendLimit != "20.50" {
		t.Fatalf("expected spend limit 20.50, got %v", got.SpendLimit)
	}
	if got.Status != APIKeyStatusActive {
		t.Fatalf("expected active status, got %q", got.Status)
	}
}

func TestAPIKeyServiceCreateRejectsEmptyName(t *testing.T) {
	store := &fakeAPIKeyStore{project: sqlc.Project{ID: 100}}
	svc := NewAPIKeyService(store)

	if _, err := svc.Create(context.Background(), APIKeyCreateParams{ProjectID: 100, Name: "  "}); failure.CodeOf(err) != failure.CodeAdminInvalidArgument {
		t.Fatalf("expected admin_invalid_argument, got %v", failure.CodeOf(err))
	}
}

func TestAPIKeyServiceComputeStatus(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	svc := &APIKeyService{now: func() time.Time { return now }}

	ts := func(tt time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: tt, Valid: true} }
	none := pgtype.Timestamptz{}

	cases := []struct {
		name                       string
		disabled, revoked, expires pgtype.Timestamptz
		want                       string
	}{
		{"active", none, none, none, APIKeyStatusActive},
		{"future expiry active", none, none, ts(now.Add(time.Hour)), APIKeyStatusActive},
		{"expired", none, none, ts(now.Add(-time.Hour)), APIKeyStatusExpired},
		{"disabled", ts(now), none, none, APIKeyStatusDisabled},
		{"revoked beats disabled", ts(now), ts(now), none, APIKeyStatusRevoked},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := svc.computeStatus(tc.disabled, tc.revoked, tc.expires); got != tc.want {
				t.Fatalf("computeStatus = %q, want %q", got, tc.want)
			}
		})
	}
}
