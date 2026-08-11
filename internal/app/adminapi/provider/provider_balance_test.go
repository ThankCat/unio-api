package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/service/admin/providerbalance"
	"github.com/go-chi/chi/v5"
)

type providerBalanceHandlerStub struct {
	adjustment providerbalance.Adjustment
	entries    []providerbalance.Entry
}

func (s *providerBalanceHandlerStub) Adjust(context.Context, providerbalance.AdjustParams) (providerbalance.Adjustment, error) {
	return s.adjustment, nil
}
func (s *providerBalanceHandlerStub) List(context.Context, providerbalance.ListParams) ([]providerbalance.Entry, int64, error) {
	return s.entries, int64(len(s.entries)), nil
}
func TestProviderBalanceAdjustmentAndLedgerRoutes(t *testing.T) {
	created := time.Date(2026, 8, 6, 1, 2, 3, 0, time.UTC)
	service := &providerBalanceHandlerStub{
		adjustment: providerbalance.Adjustment{EntryID: 11, ProviderID: 7, EntryType: "adjustment_credit", Amount: "5", Currency: "USD", BalanceAfter: "5", Reason: "seed"},
		entries:    []providerbalance.Entry{{ID: 11, ProviderID: 7, EntryType: "adjustment_credit", Amount: "5", Currency: "USD", BalanceBefore: "0", BalanceAfter: "5", Reason: "seed", CreatedAt: created}},
	}
	r := chi.NewRouter()
	Register(r, Deps{BalanceService: service})

	post := httptest.NewRecorder()
	r.ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/providers/7/balance-adjustments", strings.NewReader(`{"direction":"credit","amount":"5","currency":"USD","reason":"seed"}`)))
	if post.Code != http.StatusCreated {
		t.Fatalf("expected adjustment 201, got %d (%s)", post.Code, post.Body.String())
	}
	get := httptest.NewRecorder()
	r.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/providers/7/ledger-entries?page=1&page_size=20", nil))
	if get.Code != http.StatusOK {
		t.Fatalf("expected ledger 200, got %d (%s)", get.Code, get.Body.String())
	}
	var response struct {
		Data []struct {
			BalanceAfter string `json:"balance_after"`
		} `json:"data"`
		Meta map[string]any `json:"meta"`
	}
	if err := json.Unmarshal(get.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode ledger response: %v", err)
	}
	if len(response.Data) != 1 || response.Data[0].BalanceAfter != "5" {
		t.Fatalf("unexpected ledger response: %+v", response)
	}
}
