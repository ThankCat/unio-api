package providerbalance

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/core/providerledger"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type providerBalanceStoreStub struct {
	Store
	provider    sqlc.Provider
	balance     sqlc.ProviderBalance
	providerErr error
	balanceErr  error
	ledgerRows  []sqlc.ListProviderLedgerEntriesPageRow
	ledgerArg   sqlc.ListProviderLedgerEntriesPageParams
	riskRows    []sqlc.ListProviderCostRisksPageRow
}

func (s *providerBalanceStoreStub) GetProvider(context.Context, int64) (sqlc.Provider, error) {
	if s.providerErr != nil {
		return sqlc.Provider{}, s.providerErr
	}
	return s.provider, nil
}

func (s *providerBalanceStoreStub) GetProviderBalance(context.Context, sqlc.GetProviderBalanceParams) (sqlc.ProviderBalance, error) {
	if s.balanceErr != nil {
		return sqlc.ProviderBalance{}, s.balanceErr
	}
	return s.balance, nil
}

func (s *providerBalanceStoreStub) ListProviderLedgerEntriesPage(_ context.Context, arg sqlc.ListProviderLedgerEntriesPageParams) ([]sqlc.ListProviderLedgerEntriesPageRow, error) {
	s.ledgerArg = arg
	return s.ledgerRows, nil
}

func (s *providerBalanceStoreStub) CountProviderLedgerEntries(context.Context, sqlc.CountProviderLedgerEntriesParams) (int64, error) {
	return int64(len(s.ledgerRows)), nil
}

func (s *providerBalanceStoreStub) ListProviderCostRisksPage(context.Context, sqlc.ListProviderCostRisksPageParams) ([]sqlc.ListProviderCostRisksPageRow, error) {
	return s.riskRows, nil
}

func (s *providerBalanceStoreStub) CountProviderCostRisks(context.Context, sqlc.CountProviderCostRisksParams) (int64, error) {
	return int64(len(s.riskRows)), nil
}

type providerBalanceLedgerStub struct {
	entry providerledger.Entry
	err   error
}

func (s *providerBalanceLedgerStub) AdjustCredit(context.Context, providerledger.AdjustParams) (providerledger.Entry, error) {
	return s.entry, s.err
}
func (s *providerBalanceLedgerStub) AdjustDebit(context.Context, providerledger.AdjustParams) (providerledger.Entry, error) {
	return s.entry, s.err
}
func (s *providerBalanceLedgerStub) SetTargetBalance(context.Context, providerledger.TargetParams) (providerledger.Entry, error) {
	return s.entry, s.err
}

func testBalanceNumeric(t *testing.T, value string) pgtype.Numeric {
	t.Helper()
	var n pgtype.Numeric
	if err := n.Scan(value); err != nil {
		t.Fatalf("scan numeric: %v", err)
	}
	return n
}

func TestBalanceUSDUsesFourExplicitStatuses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		amount string
		status string
	}{
		{name: "unconfigured", status: "unconfigured"},
		{name: "normal", amount: "10", status: "normal"},
		{name: "low", amount: "9.999", status: "low"},
		{name: "negative", amount: "-0.01", status: "negative"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &providerBalanceStoreStub{provider: sqlc.Provider{ID: 7}}
			if tc.amount == "" {
				store.balanceErr = pgx.ErrNoRows
			} else {
				store.balance = sqlc.ProviderBalance{ProviderID: 7, Currency: CurrencyUSD, Balance: testBalanceNumeric(t, tc.amount)}
			}
			got, err := NewService(store, &providerBalanceLedgerStub{}).BalanceUSD(context.Background(), 7)
			if err != nil {
				t.Fatalf("BalanceUSD: %v", err)
			}
			if got.Status != tc.status {
				t.Fatalf("expected status %q, got %q", tc.status, got.Status)
			}
			if tc.amount == "" && got.Amount != nil {
				t.Fatal("unconfigured balance must be nil")
			}
		})
	}
}

func TestAdjustValidatesCurrencyAmountAndReason(t *testing.T) {
	service := NewService(
		&providerBalanceStoreStub{provider: sqlc.Provider{ID: 7}},
		&providerBalanceLedgerStub{},
	)
	base := AdjustParams{ProviderID: 7, Direction: providerledger.DirectionCredit, Amount: "1", Currency: "USD", Reason: "seed"}
	for _, tc := range []struct {
		name   string
		mutate func(*AdjustParams)
	}{
		{name: "currency", mutate: func(p *AdjustParams) { p.Currency = "CNY" }},
		{name: "amount", mutate: func(p *AdjustParams) { p.Amount = "0" }},
		{name: "reason", mutate: func(p *AdjustParams) { p.Reason = " " }},
		{name: "direction", mutate: func(p *AdjustParams) { p.Direction = "other" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			params := base
			tc.mutate(&params)
			_, err := service.Adjust(context.Background(), params)
			if failure.CodeOf(err) != failure.CodeAdminInvalidArgument {
				t.Fatalf("expected invalid argument, got %v (%s)", err, failure.CodeOf(err))
			}
		})
	}
}

func TestBalanceStoreErrorIsNotReportedAsUnconfigured(t *testing.T) {
	store := &providerBalanceStoreStub{provider: sqlc.Provider{ID: 7}, balanceErr: errors.New("db down")}
	_, err := NewService(store, &providerBalanceLedgerStub{}).BalanceUSD(context.Background(), 7)
	if failure.CodeOf(err) != failure.CodeAdminStoreFailed {
		t.Fatalf("expected store failure, got %v (%s)", err, failure.CodeOf(err))
	}
}

func TestListSupportsProbeDebitAndUsesProbeLabels(t *testing.T) {
	now := time.Now().UTC()
	store := &providerBalanceStoreStub{
		provider: sqlc.Provider{ID: 7},
		ledgerRows: []sqlc.ListProviderLedgerEntriesPageRow{{
			ID: 11, ProviderID: 7, ProviderProbeRecordID: pgtype.Int8{Int64: 31, Valid: true},
			ProbeChannelID: pgtype.Int8{Int64: 9, Valid: true}, ProbeChannelName: pgtype.Text{String: "Aihub", Valid: true},
			ProbeUpstreamModel: pgtype.Text{String: "gpt-test", Valid: true}, EntryType: providerledger.EntryTypeProbeDebit,
			Amount: testBalanceNumeric(t, "0.001"), Currency: CurrencyUSD,
			BalanceBefore: testBalanceNumeric(t, "2"), BalanceAfter: testBalanceNumeric(t, "1.999"),
			IdempotencyKey: "probe-entry", Reason: "模型探测产生的服务商成本", CreatedAt: pgtype.Timestamptz{Time: now, Valid: true},
		}},
	}
	items, total, err := NewService(store, &providerBalanceLedgerStub{}).List(context.Background(), ListParams{
		ProviderID: 7, EntryType: providerledger.EntryTypeProbeDebit, Limit: 20,
	})
	if err != nil {
		t.Fatalf("list probe ledger entries: %v", err)
	}
	if total != 1 || len(items) != 1 || items[0].ChannelID == nil || *items[0].ChannelID != 9 ||
		items[0].ChannelName == nil || *items[0].ChannelName != "Aihub" ||
		items[0].UpstreamModel == nil || *items[0].UpstreamModel != "gpt-test" {
		t.Fatalf("probe ledger labels were not merged: total=%d items=%+v", total, items)
	}
	if !store.ledgerArg.EntryType.Valid || store.ledgerArg.EntryType.String != providerledger.EntryTypeProbeDebit {
		t.Fatalf("probe debit filter was not passed through: %+v", store.ledgerArg.EntryType)
	}
}

func TestListRisksUsesRequestAttemptModel(t *testing.T) {
	store := &providerBalanceStoreStub{
		provider: sqlc.Provider{ID: 7},
		riskRows: []sqlc.ListProviderCostRisksPageRow{{
			ID: 12, ProviderID: 7, RequestRecordID: pgtype.Int8{Int64: 21, Valid: true},
			RequestAttemptID: pgtype.Int8{Int64: 22, Valid: true}, SourceType: providerledger.RiskSourceRequest,
			ReasonCode: "upstream_timeout", Reason: "上游请求超时，可能已经产生费用，需要人工核对",
			Status: providerledger.RiskStatusUnresolved, RequestUpstreamModel: pgtype.Text{String: "gpt-request", Valid: true},
			ChannelName: pgtype.Text{String: "Aihub", Valid: true}, CreatedAt: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
		}},
	}
	items, total, err := NewService(store, &providerBalanceLedgerStub{}).ListRisks(context.Background(), RiskListParams{
		ProviderID: 7, Status: providerledger.RiskStatusUnresolved, Limit: 20,
	})
	if err != nil {
		t.Fatalf("list request cost risks: %v", err)
	}
	if total != 1 || len(items) != 1 || items[0].UpstreamModel == nil || *items[0].UpstreamModel != "gpt-request" {
		t.Fatalf("request attempt model was not exposed: total=%d items=%+v", total, items)
	}
}
