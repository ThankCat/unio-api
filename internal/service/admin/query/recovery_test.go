package query_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-api/internal/service/admin/query"
)

type fakeRecoveryStore struct {
	listRows  []sqlc.ListSettlementRecoveryJobsPageRow
	listErr   error
	total     int64
	countErr  error
	job       sqlc.GetSettlementRecoveryJobByIDRow
	getErr    error
	lastList  sqlc.ListSettlementRecoveryJobsPageParams
	lastCount sqlc.CountSettlementRecoveryJobsParams
}

func (f *fakeRecoveryStore) ListSettlementRecoveryJobsPage(_ context.Context, arg sqlc.ListSettlementRecoveryJobsPageParams) ([]sqlc.ListSettlementRecoveryJobsPageRow, error) {
	f.lastList = arg
	return f.listRows, f.listErr
}

func (f *fakeRecoveryStore) CountSettlementRecoveryJobs(_ context.Context, arg sqlc.CountSettlementRecoveryJobsParams) (int64, error) {
	f.lastCount = arg
	return f.total, f.countErr
}

func (f *fakeRecoveryStore) GetSettlementRecoveryJobByID(context.Context, int64) (sqlc.GetSettlementRecoveryJobByIDRow, error) {
	return f.job, f.getErr
}

func numeric(s string) pgtype.Numeric {
	var n pgtype.Numeric
	if err := n.Scan(s); err != nil {
		panic(err)
	}
	return n
}

func TestRecoveryServiceListMapsFiltersAndTotal(t *testing.T) {
	store := &fakeRecoveryStore{
		listRows: []sqlc.ListSettlementRecoveryJobsPageRow{
			{
				ID: 5, UserID: 7, Status: "dead",
				EstimatedAmount: numeric("1.5"), AuthorizedAmount: numeric("1.0"),
				RequestPublicID:           pgtype.Text{String: "req_abc", Valid: true},
				ReservationStatus:         pgtype.Text{String: "captured", Valid: true},
				ReservationCapturedAmount: numeric("0.8"),
				ReservationReleasedAmount: numeric("0.2"),
				OverageAmount:             numeric("0.1"),
			},
			{ID: 4, UserID: 7, Status: "dead", EstimatedAmount: numeric("0.20"), AuthorizedAmount: numeric("0.20")},
		},
		total: 9,
	}
	svc := query.NewRecoveryService(store)

	uid := int64(7)
	from := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	items, total, err := svc.List(context.Background(), query.RecoveryJobListParams{
		Status: "dead",
		UserID: &uid,
		From:   &from,
		Limit:  20,
		Offset: 40,
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 9 {
		t.Fatalf("expected total 9, got %d", total)
	}
	if len(items) != 2 || items[0].ID != 5 {
		t.Fatalf("unexpected items: %+v", items)
	}
	// query 包的 numericString 精确保留 scale、不去尾零（与 M6 一致）。
	if items[0].EstimatedAmount != "1.5" || items[1].AuthorizedAmount != "0.20" {
		t.Fatalf("unexpected amount mapping: %+v", items)
	}
	// 资金闭环字段(JOIN 预授权行 + 超额补扣流水)映射。
	if items[0].ReservationStatus != "captured" || items[0].CapturedAmount != "0.8" ||
		items[0].ReleasedAmount != "0.2" || items[0].OverageAmount != "0.1" {
		t.Fatalf("money-loop fields not mapped: %+v", items[0])
	}
	if items[0].RequestPublicID != "req_abc" {
		t.Fatalf("request public id not mapped: %q", items[0].RequestPublicID)
	}
	// 关联缺失(理论不可达,LEFT JOIN 兜底)时金额回 "0"、状态回空。
	if items[1].ReservationStatus != "" || items[1].CapturedAmount != "0" {
		t.Fatalf("nullable money-loop fields not defaulted: %+v", items[1])
	}

	if !store.lastList.Status.Valid || store.lastList.Status.String != "dead" {
		t.Fatalf("status filter not forwarded: %+v", store.lastList.Status)
	}
	if !store.lastList.UserID.Valid || store.lastList.UserID.Int64 != 7 {
		t.Fatalf("user_id filter not forwarded: %+v", store.lastList.UserID)
	}
	if !store.lastList.FromTime.Valid || !store.lastList.FromTime.Time.Equal(from) {
		t.Fatalf("from filter not forwarded: %+v", store.lastList.FromTime)
	}
	if store.lastList.ToTime.Valid {
		t.Fatalf("to should be NULL when unset: %+v", store.lastList.ToTime)
	}
	if store.lastList.PageLimit != 20 || store.lastList.PageOffset != 40 {
		t.Fatalf("pagination not forwarded: %+v", store.lastList)
	}
	// Count 必须沿用同一过滤条件。
	if store.lastCount.Status.String != "dead" || store.lastCount.UserID.Int64 != 7 {
		t.Fatalf("count filters mismatch: %+v", store.lastCount)
	}
}

func baseRecoveryJob() sqlc.GetSettlementRecoveryJobByIDRow {
	now := time.Date(2026, 6, 2, 3, 4, 5, 0, time.UTC)
	return sqlc.GetSettlementRecoveryJobByIDRow{
		ID:                      11,
		UserID:                  7,
		RequestRecordID:         100,
		AttemptID:               200,
		ReservationID:           300,
		ResponseProtocol:        "openai",
		ResponseID:              "resp_1",
		ResponseModelID:         "gpt-5.5",
		ModelID:                 1,
		ProviderID:              2,
		ChannelID:               4,
		UpstreamProtocol:        "openai",
		UpstreamResponseID:      "up_resp_1",
		UpstreamModel:           "deepseek-chat",
		FinishClass:             "stop",
		UpstreamFinishReason:    "stop",
		UpstreamStatusCode:      200,
		UsageOutputTokensTotal:  42,
		UsageSource:             "upstream_response",
		UsageMappingVersion:     "v1",
		Currency:                "USD",
		PricingUnit:             "per_1m_tokens",
		FormulaVersion:          "v1",
		EstimatedAmount:         numeric("2.5"),
		AuthorizedAmount:        numeric("2.5"),
		Status:                  "dead",
		AttemptCount:            10,
		MaxAttempts:             10,
		NextRunAt:               pgtype.Timestamptz{Time: now, Valid: true},
		LastInternalErrorDetail: pgtype.Text{String: "settlement panic: raw stack", Valid: true},
		CompletedAt:             pgtype.Timestamptz{Time: now, Valid: true},
		CreatedAt:               pgtype.Timestamptz{Time: now, Valid: true},
		UpdatedAt:               pgtype.Timestamptz{Time: now, Valid: true},
		// dead 收口:全额释放、零实扣。
		RequestPublicID:           pgtype.Text{String: "req_detail", Valid: true},
		ReservationStatus:         pgtype.Text{String: "released", Valid: true},
		ReservationCapturedAmount: numeric("0"),
		ReservationReleasedAmount: numeric("2.5"),
		OverageAmount:             numeric("0"),
	}
}

func TestRecoveryServiceGetHidesInternalByDefault(t *testing.T) {
	svc := query.NewRecoveryService(&fakeRecoveryStore{job: baseRecoveryJob()})

	detail, err := svc.Get(context.Background(), 11, false)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if detail.LastInternalErrorDetail != nil {
		t.Fatalf("expected internal detail hidden, got %q", *detail.LastInternalErrorDetail)
	}
	if detail.OutputTokensTotal != 42 || detail.UpstreamResponseID != "up_resp_1" {
		t.Fatalf("audit fields not mapped: %+v", detail)
	}
	if detail.EstimatedAmount != "2.5" {
		t.Fatalf("amount mapping wrong: %q", detail.EstimatedAmount)
	}
	if detail.Status != "dead" || detail.AttemptCount != 10 || detail.MaxAttempts != 10 {
		t.Fatalf("status/retry fields not mapped: %+v", detail.RecoveryJobSummary)
	}
	if detail.ReservationStatus != "released" || detail.ReleasedAmount != "2.5" || detail.CapturedAmount != "0" {
		t.Fatalf("money-loop fields not mapped in detail: %+v", detail.RecoveryJobSummary)
	}
}

func TestRecoveryServiceGetIncludeInternal(t *testing.T) {
	svc := query.NewRecoveryService(&fakeRecoveryStore{job: baseRecoveryJob()})

	detail, err := svc.Get(context.Background(), 11, true)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if detail.LastInternalErrorDetail == nil || *detail.LastInternalErrorDetail != "settlement panic: raw stack" {
		t.Fatalf("expected internal detail surfaced, got %v", detail.LastInternalErrorDetail)
	}
}

func TestRecoveryServiceGetNotFound(t *testing.T) {
	svc := query.NewRecoveryService(&fakeRecoveryStore{getErr: pgx.ErrNoRows})

	_, err := svc.Get(context.Background(), 999, false)
	if failure.CodeOf(err) != failure.CodeAdminNotFound {
		t.Fatalf("expected admin_not_found, got %v", failure.CodeOf(err))
	}
}

func TestRecoveryServiceGetInvalidID(t *testing.T) {
	svc := query.NewRecoveryService(&fakeRecoveryStore{})

	_, err := svc.Get(context.Background(), 0, false)
	if failure.CodeOf(err) != failure.CodeAdminInvalidArgument {
		t.Fatalf("expected admin_invalid_argument, got %v", failure.CodeOf(err))
	}
}
