package query_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/admin/query"
)

type fakeRequestStore struct {
	record       sqlc.RequestRecord
	recordErr    error
	attempts     []sqlc.RequestAttempt
	attemptsErr  error
	usage        sqlc.UsageRecord
	usageErr     error
	entries      []sqlc.LedgerEntry
	entriesErr   error
	exception    sqlc.LedgerBillingException
	exceptionErr error
	costSnap     sqlc.CostSnapshot
	costSnapErr  error
	priceSnap    sqlc.PriceSnapshot
	priceSnapErr error
	route        sqlc.Route
	routeErr     error

	listRows []sqlc.ListRequestRecordsPageRow
	total    int64
	listGot  sqlc.ListRequestRecordsPageParams
	countGot sqlc.CountRequestRecordsParams
	counted  bool
}

func (f *fakeRequestStore) ListRequestRecordsPage(_ context.Context, params sqlc.ListRequestRecordsPageParams) ([]sqlc.ListRequestRecordsPageRow, error) {
	f.listGot = params
	return f.listRows, nil
}
func (f *fakeRequestStore) CountRequestRecords(_ context.Context, params sqlc.CountRequestRecordsParams) (int64, error) {
	f.counted = true
	f.countGot = params
	return f.total, nil
}
func (f *fakeRequestStore) GetRequestRecordByRequestID(context.Context, string) (sqlc.RequestRecord, error) {
	return f.record, f.recordErr
}
func (f *fakeRequestStore) ListRequestAttemptsByRequest(context.Context, int64) ([]sqlc.RequestAttempt, error) {
	return f.attempts, f.attemptsErr
}
func (f *fakeRequestStore) GetUsageRecordByRequest(context.Context, int64) (sqlc.UsageRecord, error) {
	return f.usage, f.usageErr
}
func (f *fakeRequestStore) ListLedgerEntriesByRequest(context.Context, pgtype.Int8) ([]sqlc.LedgerEntry, error) {
	return f.entries, f.entriesErr
}
func (f *fakeRequestStore) GetLedgerBillingExceptionByRequest(context.Context, int64) (sqlc.LedgerBillingException, error) {
	return f.exception, f.exceptionErr
}
func (f *fakeRequestStore) GetCostSnapshotByRequest(context.Context, int64) (sqlc.CostSnapshot, error) {
	return f.costSnap, f.costSnapErr
}
func (f *fakeRequestStore) GetPriceSnapshotByRequest(context.Context, int64) (sqlc.PriceSnapshot, error) {
	return f.priceSnap, f.priceSnapErr
}
func (f *fakeRequestStore) GetRouteByID(context.Context, int64) (sqlc.Route, error) {
	return f.route, f.routeErr
}

func baseRecord() sqlc.RequestRecord {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	return sqlc.RequestRecord{
		ID:                  1,
		RequestID:           "req_1",
		UserID:              7,
		ApiKeyID:            9,
		RequestedModelID:    "gpt-5.5",
		IngressProtocol:     "openai",
		Endpoint:            "chat_completions",
		Stream:              false,
		Status:              "failed",
		InternalErrorDetail: pgtype.Text{String: "upstream 500 raw body", Valid: true},
		DeliveryStatus:      "not_started",
		StartedAt:           pgtype.Timestamptz{Time: now, Valid: true},
		CreatedAt:           pgtype.Timestamptz{Time: now, Valid: true},
		UpdatedAt:           pgtype.Timestamptz{Time: now, Valid: true},
	}
}

func newFakeStoreWithDetail() *fakeRequestStore {
	return &fakeRequestStore{
		record: baseRecord(),
		attempts: []sqlc.RequestAttempt{{
			ID:                  10,
			RequestRecordID:     1,
			AttemptIndex:        0,
			ProviderID:          2,
			ChannelID:           4,
			AdapterKey:          "deepseek",
			UpstreamModel:       "deepseek-chat",
			UpstreamProtocol:    "openai",
			Status:              "failed",
			InternalErrorDetail: pgtype.Text{String: "attempt raw error", Valid: true},
			StartedAt:           pgtype.Timestamptz{Time: time.Now(), Valid: true},
			CreatedAt:           pgtype.Timestamptz{Time: time.Now(), Valid: true},
		}},
		usageErr:     pgx.ErrNoRows,
		exceptionErr: pgx.ErrNoRows,
		costSnapErr:  pgx.ErrNoRows,
		priceSnapErr: pgx.ErrNoRows,
		routeErr:     pgx.ErrNoRows,
	}
}

func TestRequestServiceGetHidesInternalByDefault(t *testing.T) {
	svc := query.NewRequestService(newFakeStoreWithDetail())

	detail, err := svc.Get(context.Background(), "req_1", false)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if detail.InternalErrorDetail != nil {
		t.Fatalf("expected request internal detail hidden, got %q", *detail.InternalErrorDetail)
	}
	if len(detail.Attempts) != 1 {
		t.Fatalf("expected 1 attempt, got %d", len(detail.Attempts))
	}
	if detail.Attempts[0].InternalErrorDetail != nil {
		t.Fatalf("expected attempt internal detail hidden, got %q", *detail.Attempts[0].InternalErrorDetail)
	}
	if detail.Usage != nil {
		t.Fatalf("expected no usage when ErrNoRows")
	}
	if detail.BillingException != nil {
		t.Fatalf("expected no billing exception when ErrNoRows")
	}
}

func TestRequestServiceGetIncludeInternal(t *testing.T) {
	svc := query.NewRequestService(newFakeStoreWithDetail())

	detail, err := svc.Get(context.Background(), "req_1", true)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if detail.InternalErrorDetail == nil || *detail.InternalErrorDetail != "upstream 500 raw body" {
		t.Fatalf("expected request internal detail surfaced, got %v", detail.InternalErrorDetail)
	}
	if detail.Attempts[0].InternalErrorDetail == nil || *detail.Attempts[0].InternalErrorDetail != "attempt raw error" {
		t.Fatalf("expected attempt internal detail surfaced, got %v", detail.Attempts[0].InternalErrorDetail)
	}
}

func TestRequestServiceGetMapsAttemptTimeoutAndScoringFacts(t *testing.T) {
	store := newFakeStoreWithDetail()
	store.attempts[0].UpstreamTimeoutPhase = pgtype.Text{String: "first_token", Valid: true}
	store.attempts[0].TtftScoringSample = false
	store.attempts[0].ErrorScoringSample = true
	store.attempts[0].ErrorScoringFailure = true

	detail, err := query.NewRequestService(store).Get(context.Background(), "req_1", false)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	attempt := detail.Attempts[0]
	if attempt.UpstreamTimeoutPhase == nil || *attempt.UpstreamTimeoutPhase != "first_token" ||
		attempt.TTFTScoringSample || !attempt.ErrorScoringSample || !attempt.ErrorScoringFailure {
		t.Fatalf("attempt timeout/scoring facts = %+v", attempt)
	}
}

func TestRequestServiceGetDerivesUpstreamTimingByRequestMode(t *testing.T) {
	upstreamStarted := time.Date(2026, 6, 1, 0, 0, 1, 0, time.UTC)
	upstreamFirstToken := upstreamStarted.Add(250 * time.Millisecond)
	upstreamCompleted := upstreamStarted.Add(2500 * time.Millisecond)

	for _, tt := range []struct {
		name       string
		stream     bool
		wantTTFTMs *int64
	}{
		{name: "stream", stream: true, wantTTFTMs: int64Ptr(250)},
		{name: "non-stream", stream: false, wantTTFTMs: nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := newFakeStoreWithDetail()
			store.record.Stream = tt.stream
			store.attempts[0].UpstreamStartedAt = pgtype.Timestamptz{Time: upstreamStarted, Valid: true}
			// 非流式 fixture 也故意带首字时间，验证管理查询不会把脏数据展示成 TTFT。
			store.attempts[0].UpstreamFirstTokenAt = pgtype.Timestamptz{Time: upstreamFirstToken, Valid: true}
			store.attempts[0].UpstreamCompletedAt = pgtype.Timestamptz{Time: upstreamCompleted, Valid: true}

			detail, err := query.NewRequestService(store).Get(context.Background(), "req_1", false)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			attempt := detail.Attempts[0]
			if attempt.UpstreamTotalMs == nil || *attempt.UpstreamTotalMs != 2500 {
				t.Fatalf("upstream total = %v, want 2500", attempt.UpstreamTotalMs)
			}
			if tt.wantTTFTMs == nil {
				if attempt.UpstreamTTFTMs != nil {
					t.Fatalf("non-stream upstream TTFT = %v, want nil", *attempt.UpstreamTTFTMs)
				}
			} else if attempt.UpstreamTTFTMs == nil || *attempt.UpstreamTTFTMs != *tt.wantTTFTMs {
				t.Fatalf("upstream TTFT = %v, want %d", attempt.UpstreamTTFTMs, *tt.wantTTFTMs)
			}
		})
	}
}

func int64Ptr(value int64) *int64 {
	return &value
}

func TestRequestServiceGetNotFound(t *testing.T) {
	svc := query.NewRequestService(&fakeRequestStore{recordErr: pgx.ErrNoRows})

	_, err := svc.Get(context.Background(), "missing", false)
	if failure.CodeOf(err) != failure.CodeAdminNotFound {
		t.Fatalf("expected admin_not_found, got %v", failure.CodeOf(err))
	}
}

func TestRequestServiceGetEmptyIDInvalid(t *testing.T) {
	svc := query.NewRequestService(&fakeRequestStore{})

	_, err := svc.Get(context.Background(), "", false)
	if failure.CodeOf(err) != failure.CodeAdminInvalidArgument {
		t.Fatalf("expected admin_invalid_argument, got %v", failure.CodeOf(err))
	}
}

func TestRequestServiceListMapsTotal(t *testing.T) {
	store := &fakeRequestStore{
		listRows: []sqlc.ListRequestRecordsPageRow{
			{TotalCount: 42, ID: 1, RequestID: "req_1", Status: "succeeded"},
			{TotalCount: 42, ID: 2, RequestID: "req_2", Status: "failed"},
		},
	}
	svc := query.NewRequestService(store)

	items, total, err := svc.List(context.Background(), query.RequestListParams{Limit: 20, Offset: 0})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 42 {
		t.Fatalf("expected total 42, got %d", total)
	}
	if len(items) != 2 || items[0].RequestID != "req_1" {
		t.Fatalf("unexpected items: %+v", items)
	}
	if store.counted {
		t.Fatal("CountRequestRecords should not run for a non-empty page")
	}
}

func TestRequestServiceListForwardsRoutingSampleFilters(t *testing.T) {
	store := &fakeRequestStore{}
	routeID := int64(7)
	channelID := int64(4)
	attemptID := int64(19)

	_, _, err := query.NewRequestService(store).List(context.Background(), query.RequestListParams{
		RouteID:       &routeID,
		ChannelID:     &channelID,
		AttemptID:     &attemptID,
		ScoringSample: "any",
		Limit:         10,
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for name, got := range map[string]sqlc.ListRequestRecordsPageParams{"list": store.listGot} {
		if !got.RouteID.Valid || got.RouteID.Int64 != routeID || !got.ChannelID.Valid || got.ChannelID.Int64 != channelID ||
			!got.AttemptID.Valid || got.AttemptID.Int64 != attemptID || !got.ScoringSample.Valid || got.ScoringSample.String != "any" {
			t.Fatalf("%s filters = %+v", name, got)
		}
	}
	if store.counted {
		t.Fatal("CountRequestRecords should not run for an empty first page")
	}
}

func TestRequestServiceListCountsOnlyEmptyPageBeyondFirst(t *testing.T) {
	store := &fakeRequestStore{total: 42}
	routeID := int64(7)
	channelID := int64(4)
	attemptID := int64(19)

	items, total, err := query.NewRequestService(store).List(context.Background(), query.RequestListParams{
		RouteID:       &routeID,
		ChannelID:     &channelID,
		AttemptID:     &attemptID,
		ScoringSample: "any",
		Limit:         10,
		Offset:        20,
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 0 || total != 42 {
		t.Fatalf("items = %d, total = %d, want 0 and 42", len(items), total)
	}
	if !store.counted {
		t.Fatal("CountRequestRecords was not called for an empty page beyond the first")
	}
	got := store.countGot
	if !got.RouteID.Valid || got.RouteID.Int64 != routeID || !got.ChannelID.Valid || got.ChannelID.Int64 != channelID ||
		!got.AttemptID.Valid || got.AttemptID.Int64 != attemptID || !got.ScoringSample.Valid || got.ScoringSample.String != "any" {
		t.Fatalf("count filters = %+v", got)
	}
}

func TestRequestServiceGetDerivesAuthoritativeRequestTiming(t *testing.T) {
	store := newFakeStoreWithDetail()
	started := time.Date(2026, 7, 31, 11, 56, 8, 638604000, time.UTC)
	firstToken := started.Add(1957*time.Millisecond + 73*time.Microsecond)
	completed := started.Add(2250*time.Millisecond + 220*time.Microsecond)
	store.record.Stream = true
	store.record.Status = "succeeded"
	store.record.StartedAt = pgtype.Timestamptz{Time: started, Valid: true}
	store.record.GatewayFirstTokenAt = pgtype.Timestamptz{Time: firstToken, Valid: true}
	store.record.CompletedAt = pgtype.Timestamptz{Time: completed, Valid: true}
	store.usage = sqlc.UsageRecord{OutputTokensTotal: 16}
	store.usageErr = nil

	detail, err := query.NewRequestService(store).Get(context.Background(), "req_1", false)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if detail.LatencyMs == nil || *detail.LatencyMs != 2250 || detail.GatewayTTFTMs == nil || *detail.GatewayTTFTMs != 1957 {
		t.Fatalf("request timing = latency %v, gateway ttft %v", detail.LatencyMs, detail.GatewayTTFTMs)
	}
	if detail.TPS == nil || *detail.TPS <= 0 {
		t.Fatalf("request TPS = %v, want positive", detail.TPS)
	}
}

func TestRequestServiceListMapsRoutingSampleLocation(t *testing.T) {
	store := &fakeRequestStore{
		listRows: []sqlc.ListRequestRecordsPageRow{{
			ID:                  1,
			RequestID:           "req_sample",
			Status:              "failed",
			RouteID:             pgtype.Int8{Int64: 7, Valid: true},
			ScoringAttemptID:    19,
			ScoringDimensions:   []string{"ttft", "error"},
			ScoringErrorFailure: true,
		}},
		total: 1,
	}

	items, _, err := query.NewRequestService(store).List(context.Background(), query.RequestListParams{Limit: 1})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	item := items[0]
	if item.RouteID == nil || *item.RouteID != 7 || item.ScoringAttemptID == nil || *item.ScoringAttemptID != 19 ||
		len(item.ScoringDimensions) != 2 || !item.ScoringErrorFailure {
		t.Fatalf("routing sample location = %+v", item)
	}
}

func TestRequestServiceListIgnoresLegacyNonStreamGatewayFirstTokenAt(t *testing.T) {
	started := time.Date(2026, 7, 22, 1, 0, 0, 0, time.UTC)
	responseStarted := started.Add(250 * time.Millisecond)
	completed := started.Add(2 * time.Second)
	store := &fakeRequestStore{
		listRows: []sqlc.ListRequestRecordsPageRow{{
			ID:                  1,
			RequestID:           "req_legacy_non_stream",
			Stream:              false,
			Status:              "succeeded",
			StartedAt:           pgtype.Timestamptz{Time: started, Valid: true},
			GatewayFirstTokenAt: pgtype.Timestamptz{Time: responseStarted, Valid: true},
			CompletedAt:         pgtype.Timestamptz{Time: completed, Valid: true},
			OutputTokensTotal:   100,
		}},
		total: 1,
	}

	items, _, err := query.NewRequestService(store).List(context.Background(), query.RequestListParams{Limit: 1})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	if items[0].LatencyMs == nil || *items[0].LatencyMs != 2000 {
		t.Fatalf("latency = %v, want 2000", items[0].LatencyMs)
	}
	if items[0].GatewayTTFTMs != nil || items[0].TPS != nil {
		t.Fatalf("legacy non-stream timing leaked Gateway TTFT/TPS: ttft=%v tps=%v", items[0].GatewayTTFTMs, items[0].TPS)
	}
}
