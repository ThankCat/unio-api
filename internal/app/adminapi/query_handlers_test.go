package adminapi_test

import (
	"context"
	"go.uber.org/zap"
	"net/http"
	"strings"
	"testing"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi"
	"github.com/ThankCat/unio-gateway/internal/core/adminauth"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/service/admin/query"
)

type fakeRequestQueryService struct {
	listOut []query.RequestListItem
	getOut  query.RequestDetail
	getErr  error
	// gotInclude 记录最近一次 Get 的 includeInternal，用于断言开关透传。
	gotInclude bool
}

func (s *fakeRequestQueryService) List(context.Context, query.RequestListParams) ([]query.RequestListItem, int64, error) {
	return s.listOut, int64(len(s.listOut)), nil
}

func (s *fakeRequestQueryService) Get(_ context.Context, _ string, includeInternal bool) (query.RequestDetail, error) {
	s.gotInclude = includeInternal
	if s.getErr != nil {
		return query.RequestDetail{}, s.getErr
	}
	detail := s.getOut
	// 模拟真实 service：仅在 includeInternal 时回显内部错误详情。
	if includeInternal {
		v := "boom: upstream 500 raw body"
		detail.InternalErrorDetail = &v
	} else {
		detail.InternalErrorDetail = nil
	}
	return detail, nil
}

type fakeLedgerQueryService struct {
	entries    []query.LedgerEntry
	exceptions []query.BillingException
}

func (s *fakeLedgerQueryService) ListEntries(context.Context, query.EntryListParams) ([]query.LedgerEntry, int64, error) {
	return s.entries, int64(len(s.entries)), nil
}

func (s *fakeLedgerQueryService) ListBillingExceptions(context.Context, query.ExceptionListParams) ([]query.BillingException, int64, error) {
	return s.exceptions, int64(len(s.exceptions)), nil
}

func newQueryRouter(t *testing.T, deps adminapi.RouterDeps) http.Handler {
	t.Helper()

	authenticator, err := adminauth.NewStaticTokenAuthenticator(testAdminToken)
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}
	deps.Logger = zap.NewNop()
	deps.AdminAuthenticator = authenticator
	return adminapi.NewRouter(deps)
}

func TestListRequestsOmitsInternalErrorDetail(t *testing.T) {
	rqs := &fakeRequestQueryService{listOut: []query.RequestListItem{{RequestSummary: query.RequestSummary{ID: 1, RequestID: "req_1", Status: "failed"}}}}
	handler := newQueryRouter(t, adminapi.RouterDeps{RequestQueryService: rqs})

	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/requests", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	// 列表永远不暴露内部错误详情（DTO 无此字段，断言 body 不含该键）。
	if strings.Contains(rec.Body.String(), "internal_error_detail") {
		t.Fatalf("list response must not contain internal_error_detail: %s", rec.Body.String())
	}
}

func TestGetRequestDefaultHidesInternalDetail(t *testing.T) {
	rqs := &fakeRequestQueryService{getOut: query.RequestDetail{RequestSummary: query.RequestSummary{ID: 1, RequestID: "req_1", Status: "failed"}}}
	handler := newQueryRouter(t, adminapi.RouterDeps{RequestQueryService: rqs})

	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/requests/req_1", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	if rqs.gotInclude {
		t.Fatalf("expected includeInternal=false by default")
	}
	if strings.Contains(rec.Body.String(), "internal_error_detail") {
		t.Fatalf("detail without include_internal must omit internal_error_detail: %s", rec.Body.String())
	}
}

func TestGetRequestIncludeInternalReturnsDetail(t *testing.T) {
	rqs := &fakeRequestQueryService{getOut: query.RequestDetail{RequestSummary: query.RequestSummary{ID: 1, RequestID: "req_1", Status: "failed"}}}
	handler := newQueryRouter(t, adminapi.RouterDeps{RequestQueryService: rqs})

	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/requests/req_1?include_internal=true", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	if !rqs.gotInclude {
		t.Fatalf("expected includeInternal=true to be passed through")
	}
	if !strings.Contains(rec.Body.String(), "internal_error_detail") {
		t.Fatalf("detail with include_internal must contain internal_error_detail: %s", rec.Body.String())
	}
}

func TestGetRequestNotFoundReturns404(t *testing.T) {
	rqs := &fakeRequestQueryService{getErr: failure.New(failure.CodeAdminNotFound, failure.WithMessage("request not found"))}
	handler := newQueryRouter(t, adminapi.RouterDeps{RequestQueryService: rqs})

	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/requests/missing", "", true)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected %d, got %d (%s)", http.StatusNotFound, rec.Code, rec.Body.String())
	}
}

func TestListRequestsInvalidUserIDReturns400(t *testing.T) {
	handler := newQueryRouter(t, adminapi.RouterDeps{RequestQueryService: &fakeRequestQueryService{}})

	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/requests?user_id=abc", "", true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d (%s)", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}

func TestListRequestsInvalidTimeReturns400(t *testing.T) {
	handler := newQueryRouter(t, adminapi.RouterDeps{RequestQueryService: &fakeRequestQueryService{}})

	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/requests?from=not-a-time", "", true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d (%s)", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}

func TestRequestsRequireToken(t *testing.T) {
	handler := newQueryRouter(t, adminapi.RouterDeps{RequestQueryService: &fakeRequestQueryService{}})

	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/requests", "", false)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestListLedgerEntriesReturns200(t *testing.T) {
	handler := newQueryRouter(t, adminapi.RouterDeps{LedgerQueryService: &fakeLedgerQueryService{entries: []query.LedgerEntry{{ID: 1, EntryType: "debit", Amount: "1.5", Currency: "USD"}}}})

	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/ledger/entries", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
}

func TestListBillingExceptionsReturns200(t *testing.T) {
	handler := newQueryRouter(t, adminapi.RouterDeps{LedgerQueryService: &fakeLedgerQueryService{exceptions: []query.BillingException{{ID: 1, EventType: "write_off", PlatformAmount: "0.5", Currency: "USD"}}}})

	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/ledger/billing-exceptions", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
}
