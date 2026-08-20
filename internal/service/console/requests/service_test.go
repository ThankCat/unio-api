package requests_test

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/console/requests"
)

type fakeStore struct {
	listRows      []sqlc.ListConsoleBilledRequestsRow
	listParams    sqlc.ListConsoleBilledRequestsParams
	countTotal    int64
	countCalled   bool
	summary       sqlc.SummarizeConsoleBilledRequestsRow
	summaryParams sqlc.SummarizeConsoleBilledRequestsParams
	routes        []sqlc.ListConsoleBilledRequestRoutesRow
	keys          []sqlc.ListConsoleBilledRequestAPIKeysRow
	endpoints     []string
}

func (f *fakeStore) ListConsoleBilledRequests(_ context.Context, arg sqlc.ListConsoleBilledRequestsParams) ([]sqlc.ListConsoleBilledRequestsRow, error) {
	f.listParams = arg
	return f.listRows, nil
}

func (f *fakeStore) CountConsoleBilledRequests(context.Context, sqlc.CountConsoleBilledRequestsParams) (int64, error) {
	f.countCalled = true
	return f.countTotal, nil
}

func (f *fakeStore) SummarizeConsoleBilledRequests(_ context.Context, arg sqlc.SummarizeConsoleBilledRequestsParams) (sqlc.SummarizeConsoleBilledRequestsRow, error) {
	f.summaryParams = arg
	return f.summary, nil
}

func (f *fakeStore) ListConsoleBilledRequestRoutes(context.Context, int64) ([]sqlc.ListConsoleBilledRequestRoutesRow, error) {
	return f.routes, nil
}

func (f *fakeStore) ListConsoleBilledRequestAPIKeys(context.Context, int64) ([]sqlc.ListConsoleBilledRequestAPIKeysRow, error) {
	return f.keys, nil
}

func (f *fakeStore) ListConsoleBilledRequestEndpoints(context.Context, int64) ([]string, error) {
	return f.endpoints, nil
}

func TestListMapsCustomerSafeFieldsAndScopesToUser(t *testing.T) {
	started := time.Date(2026, 8, 20, 8, 0, 0, 0, time.UTC)
	completed := started.Add(1500 * time.Millisecond)
	store := &fakeStore{
		listRows: []sqlc.ListConsoleBilledRequestsRow{{
			TotalCount:       1,
			ID:               11,
			RequestID:        "req_safe",
			CreatedAt:        pgtype.Timestamptz{Time: started, Valid: true},
			ClientIp:         pgtype.Text{String: "203.0.113.10", Valid: true},
			RouteID:          pgtype.Int8{Int64: 3, Valid: true},
			RouteName:        pgtype.Text{String: "Claude", Valid: true},
			ApiKeyID:         9,
			ApiKeyName:       pgtype.Text{String: "prod", Valid: true},
			Endpoint:         "chat_completions",
			Stream:           true,
			RequestedModelID: "claude-sonnet-4-5",
			ReasoningEffort:  pgtype.Text{String: "medium", Valid: true},
			InputTokens:      100,
			OutputTokens:     20,
			StartedAt:        pgtype.Timestamptz{Time: started, Valid: true},
			CompletedAt:      pgtype.Timestamptz{Time: completed, Valid: true},
			UserChargeUsd:    mustNumeric(t, "0.15"),
		}},
	}

	items, total, err := requests.NewService(store).List(context.Background(), requests.ListParams{
		UserID:   7,
		Q:        "claude",
		From:     &started,
		Limit:    20,
		Offset:   0,
		RouteIDs: []int64{3},
	})
	if err != nil {
		t.Fatal(err)
	}
	if store.listParams.UserID != 7 {
		t.Fatalf("user id = %d, want 7", store.listParams.UserID)
	}
	if store.listParams.Q.String != "claude" || !store.listParams.Q.Valid {
		t.Fatalf("search not forwarded: %+v", store.listParams.Q)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("got %d items total=%d", len(items), total)
	}
	item := items[0]
	if item.Endpoint != "/v1/chat/completions" {
		t.Fatalf("endpoint = %q", item.Endpoint)
	}
	if item.APIKeyName != "prod" || item.RouteName != "Claude" {
		t.Fatalf("item = %+v", item)
	}
	if item.LatencyMs == nil || *item.LatencyMs != 1500 {
		t.Fatalf("latency = %v", item.LatencyMs)
	}
	if item.UserChargeUSD != "0.15" {
		t.Fatalf("charge = %q", item.UserChargeUSD)
	}
}

func TestSummaryUsesAllTimeWhenBoundsOmitted(t *testing.T) {
	store := &fakeStore{
		summary: sqlc.SummarizeConsoleBilledRequestsRow{
			RequestCount:     4,
			TokenCount:       180,
			InputTokenCount:  120,
			OutputTokenCount: 60,
			ChargeUsd:        mustNumeric(t, "1.25"),
			AverageLatencyMs: 750,
		},
	}

	summary, err := requests.NewService(store).Summary(context.Background(), requests.SummaryParams{
		UserID: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	if store.summaryParams.UserID != 7 || store.summaryParams.FromTime.Valid || store.summaryParams.ToTime.Valid {
		t.Fatalf("summary params = %+v", store.summaryParams)
	}
	if summary.RequestCount != 4 || summary.TokenCount != 180 || summary.InputTokenCount != 120 || summary.OutputTokenCount != 60 || summary.ChargeUSD != "1.25" || summary.AverageLatencyMs != 750 {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestSummaryForwardsOptionalTimeBounds(t *testing.T) {
	store := &fakeStore{
		summary: sqlc.SummarizeConsoleBilledRequestsRow{
			RequestCount:     4,
			TokenCount:       180,
			InputTokenCount:  120,
			OutputTokenCount: 60,
			ChargeUsd:        mustNumeric(t, "1.25"),
			AverageLatencyMs: 750,
		},
	}

	from := time.Date(2026, 8, 19, 16, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)
	_, err := requests.NewService(store).Summary(context.Background(), requests.SummaryParams{
		UserID: 7,
		From:   &from,
		To:     &to,
	})
	if err != nil {
		t.Fatal(err)
	}
	if store.summaryParams.UserID != 7 || !store.summaryParams.FromTime.Valid || !store.summaryParams.ToTime.Valid {
		t.Fatalf("summary params = %+v", store.summaryParams)
	}
	if !store.summaryParams.FromTime.Time.Equal(from) || !store.summaryParams.ToTime.Time.Equal(to) {
		t.Fatalf("time bounds = from=%v to=%v", store.summaryParams.FromTime.Time, store.summaryParams.ToTime.Time)
	}
}

func TestFiltersExposePublicEndpoints(t *testing.T) {
	store := &fakeStore{
		routes:    []sqlc.ListConsoleBilledRequestRoutesRow{{ID: 3, Name: "Claude"}},
		keys:      []sqlc.ListConsoleBilledRequestAPIKeysRow{{ID: 9, Name: "prod"}},
		endpoints: []string{"chat_completions", "messages"},
	}

	filters, err := requests.NewService(store).Filters(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(filters.Routes) != 1 || filters.Routes[0].ID != 3 {
		t.Fatalf("routes = %+v", filters.Routes)
	}
	if len(filters.APIKeys) != 1 || filters.APIKeys[0].Name != "prod" {
		t.Fatalf("keys = %+v", filters.APIKeys)
	}
	if len(filters.Endpoints) != 2 || filters.Endpoints[0] != "/v1/chat/completions" || filters.Endpoints[1] != "/v1/messages" {
		t.Fatalf("endpoints = %#v", filters.Endpoints)
	}
}

func TestListTranslatesPublicEndpointFilters(t *testing.T) {
	store := &fakeStore{}
	_, _, err := requests.NewService(store).List(context.Background(), requests.ListParams{
		UserID:    7,
		Endpoints: []string{"/v1/chat/completions"},
		Limit:     20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(store.listParams.Endpoints) != 1 || store.listParams.Endpoints[0] != "chat_completions" {
		t.Fatalf("endpoints = %#v", store.listParams.Endpoints)
	}
}

func mustNumeric(t *testing.T, raw string) pgtype.Numeric {
	t.Helper()
	n := new(big.Rat)
	if _, ok := n.SetString(raw); !ok {
		t.Fatalf("invalid numeric %q", raw)
	}
	var value pgtype.Numeric
	if err := value.Scan(raw); err != nil {
		t.Fatal(err)
	}
	return value
}
