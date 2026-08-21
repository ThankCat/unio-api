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
	routes        []sqlc.ListConsoleFilterRoutesRow
	keys          []sqlc.ListConsoleFilterAPIKeysRow
	keysUserID    int64
	endpoints     []string
	streams       []bool
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

func (f *fakeStore) ListConsoleFilterRoutes(context.Context) ([]sqlc.ListConsoleFilterRoutesRow, error) {
	return f.routes, nil
}

func (f *fakeStore) ListConsoleFilterAPIKeys(_ context.Context, userID int64) ([]sqlc.ListConsoleFilterAPIKeysRow, error) {
	f.keysUserID = userID
	return f.keys, nil
}

func (f *fakeStore) ListConsoleBilledRequestEndpoints(context.Context, int64) ([]string, error) {
	return f.endpoints, nil
}

func (f *fakeStore) ListConsoleBilledRequestStreamTypes(context.Context, int64) ([]bool, error) {
	return f.streams, nil
}

func TestListMapsCustomerSafeFieldsAndScopesToUser(t *testing.T) {
	started := time.Date(2026, 8, 20, 8, 0, 0, 0, time.UTC)
	completed := started.Add(1500 * time.Millisecond)
	store := &fakeStore{
		listRows: []sqlc.ListConsoleBilledRequestsRow{{
			TotalCount:              1,
			ID:                      11,
			RequestID:               "req_safe",
			CreatedAt:               pgtype.Timestamptz{Time: started, Valid: true},
			ClientIp:                pgtype.Text{String: "203.0.113.10", Valid: true},
			RouteID:                 pgtype.Int8{Int64: 3, Valid: true},
			RouteName:               pgtype.Text{String: "Claude", Valid: true},
			ApiKeyID:                9,
			ApiKeyName:              pgtype.Text{String: "prod", Valid: true},
			ApiKeyPrefix:            pgtype.Text{String: "unio_sk_XhE8wL5D", Valid: true},
			ApiKeyPlaintext:         pgtype.Text{String: "unio_sk_XhE8wL5Dabcdefghijklmnopqrstuvwxyz012345", Valid: true},
			Endpoint:                "chat_completions",
			Stream:                  true,
			RequestedModelID:        "claude-sonnet-4-5",
			ModelDisplayName:        pgtype.Text{String: "Claude Sonnet 4.5", Valid: true},
			IngressProtocol:         "anthropic",
			InputPricePer1m:         mustNumeric(t, "1"),
			OutputPricePer1m:        mustNumeric(t, "6"),
			CacheReadPricePer1m:     mustNumeric(t, "0.1"),
			PriceServiceTier:        pgtype.Text{String: "standard", Valid: true},
			ReasoningEffort:         pgtype.Text{String: "medium", Valid: true},
			UncachedInputTokens:     80,
			CacheReadInputTokens:    10,
			CacheWrite5mInputTokens: 10,
			InputTokens:             100,
			OutputTokens:            20,
			ReasoningOutputTokens:   5,
			StartedAt:               pgtype.Timestamptz{Time: started, Valid: true},
			CompletedAt:             pgtype.Timestamptz{Time: completed, Valid: true},
			GatewayFirstTokenAt:     pgtype.Timestamptz{Time: started.Add(400 * time.Millisecond), Valid: true},
			UserChargeUsd:           mustNumeric(t, "0.15"),
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
	if item.Endpoint != "/chat/completions" {
		t.Fatalf("endpoint = %q", item.Endpoint)
	}
	if item.APIKeyName != "prod" || item.RouteName != "Claude" {
		t.Fatalf("item = %+v", item)
	}
	if item.APIKeyPrefix != "unio_sk_XhE8wL5D" || item.APIKeyPlaintext == nil || *item.APIKeyPlaintext == "" {
		t.Fatalf("api key secret = prefix=%q plaintext=%v", item.APIKeyPrefix, item.APIKeyPlaintext)
	}
	if item.LatencyMs == nil || *item.LatencyMs != 1500 {
		t.Fatalf("latency = %v", item.LatencyMs)
	}
	if item.FirstTokenMs == nil || *item.FirstTokenMs != 400 {
		t.Fatalf("first token = %v", item.FirstTokenMs)
	}
	if item.TPS == nil || *item.TPS < 18.18 || *item.TPS > 18.19 {
		t.Fatalf("tps = %v", item.TPS)
	}
	if item.UserChargeUSD != "0.15" {
		t.Fatalf("charge = %q", item.UserChargeUSD)
	}
	if item.ModelDisplayName != "Claude Sonnet 4.5" || item.RequestedModelID != "claude-sonnet-4-5" {
		t.Fatalf("model = name=%q id=%q", item.ModelDisplayName, item.RequestedModelID)
	}
	if item.IngressProtocol != "anthropic" {
		t.Fatalf("protocol = %q", item.IngressProtocol)
	}
	if item.InputPricePer1M == nil || *item.InputPricePer1M != "1" || item.OutputPricePer1M == nil || *item.OutputPricePer1M != "6" {
		t.Fatalf("prices = in=%v out=%v", item.InputPricePer1M, item.OutputPricePer1M)
	}
	if item.CacheReadPricePer1M == nil || *item.CacheReadPricePer1M != "0.1" {
		t.Fatalf("cache read price = %v", item.CacheReadPricePer1M)
	}
	if item.PriceServiceTier == nil || *item.PriceServiceTier != "standard" {
		t.Fatalf("tier = %v", item.PriceServiceTier)
	}
	if item.UncachedInputTokens != 80 || item.CacheReadInputTokens != 10 || item.CacheWrite5mInputTokens != 10 || item.ReasoningOutputTokens != 5 {
		t.Fatalf("token breakdown = %+v", item)
	}
}

func TestListFallsBackToModelIDWhenDisplayNameMissing(t *testing.T) {
	store := &fakeStore{
		listRows: []sqlc.ListConsoleBilledRequestsRow{{
			TotalCount:       1,
			ID:               11,
			RequestID:        "req_orphan",
			CreatedAt:        pgtype.Timestamptz{Time: time.Date(2026, 8, 20, 8, 0, 0, 0, time.UTC), Valid: true},
			ApiKeyID:         9,
			Endpoint:         "chat_completions",
			RequestedModelID: "gpt-5.6-sol",
			UserChargeUsd:    mustNumeric(t, "0.15"),
		}},
	}

	items, _, err := requests.NewService(store).List(context.Background(), requests.ListParams{
		UserID: 7,
		Limit:  20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ModelDisplayName != "gpt-5.6-sol" {
		t.Fatalf("fallback display name = %#v", items)
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

func TestFiltersExposeCatalogRoutesAndPublicEndpoints(t *testing.T) {
	store := &fakeStore{
		routes: []sqlc.ListConsoleFilterRoutesRow{
			{ID: 3, Name: "Claude"},
			{ID: 4, Name: "GPT"},
		},
		keys:      []sqlc.ListConsoleFilterAPIKeysRow{{ID: 9, Name: "prod"}},
		endpoints: []string{"chat_completions", "messages"},
		streams:   []bool{true, false},
	}

	filters, err := requests.NewService(store).Filters(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(filters.Routes) != 2 || filters.Routes[0].ID != 3 || filters.Routes[1].Name != "GPT" {
		t.Fatalf("routes = %+v", filters.Routes)
	}
	if store.keysUserID != 7 {
		t.Fatalf("keys user = %d", store.keysUserID)
	}
	if len(filters.APIKeys) != 1 || filters.APIKeys[0].Name != "prod" {
		t.Fatalf("keys = %+v", filters.APIKeys)
	}
	if len(filters.Endpoints) != 2 || filters.Endpoints[0] != "/chat/completions" || filters.Endpoints[1] != "/messages" {
		t.Fatalf("endpoints = %#v", filters.Endpoints)
	}
	if len(filters.StreamTypes) != 2 || filters.StreamTypes[0] != "stream" || filters.StreamTypes[1] != "sync" {
		t.Fatalf("stream types = %#v", filters.StreamTypes)
	}
}

func TestListTranslatesPublicEndpointFilters(t *testing.T) {
	store := &fakeStore{}
	_, _, err := requests.NewService(store).List(context.Background(), requests.ListParams{
		UserID:    7,
		Endpoints: []string{"/chat/completions"},
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
