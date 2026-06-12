package adminapi_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ThankCat/unio-api/internal/app/adminapi"
	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/service/admin/query"
)

type fakeRecoveryJobService struct {
	listOut        []query.RecoveryJobSummary
	total          int64
	getOut         query.RecoveryJobDetail
	getErr         error
	gotInclude     bool
	gotListParams  query.RecoveryJobListParams
	gotGetID       int64
	gotGetIDCalled bool
}

func (s *fakeRecoveryJobService) List(_ context.Context, params query.RecoveryJobListParams) ([]query.RecoveryJobSummary, int64, error) {
	s.gotListParams = params
	return s.listOut, s.total, nil
}

func (s *fakeRecoveryJobService) Get(_ context.Context, id int64, includeInternal bool) (query.RecoveryJobDetail, error) {
	s.gotGetID, s.gotGetIDCalled, s.gotInclude = id, true, includeInternal
	if s.getErr != nil {
		return query.RecoveryJobDetail{}, s.getErr
	}
	return s.getOut, nil
}

type fakeChannelHealthService struct {
	out     []query.ChannelHealth
	gotFrom *time.Time
	gotTo   *time.Time
}

func (s *fakeChannelHealthService) List(_ context.Context, from, to *time.Time) ([]query.ChannelHealth, error) {
	s.gotFrom, s.gotTo = from, to
	return s.out, nil
}

func TestListRecoveryJobsReturns200AndOmitsInternalDetail(t *testing.T) {
	svc := &fakeRecoveryJobService{
		listOut: []query.RecoveryJobSummary{{ID: 1, Status: "dead", UserID: 7}},
		total:   1,
	}
	handler := newQueryRouter(t, adminapi.RouterDeps{RecoveryJobQueryService: svc})

	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/system/settlement-recovery-jobs?status=dead", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	// 列表 DTO 无内部诊断字段，断言 body 不含该键。
	if strings.Contains(rec.Body.String(), "last_internal_error_detail") {
		t.Fatalf("list must not contain last_internal_error_detail: %s", rec.Body.String())
	}
	if svc.gotListParams.Status != "dead" {
		t.Fatalf("status filter not forwarded: %q", svc.gotListParams.Status)
	}
}

func TestListRecoveryJobsForwardsUserIDFilter(t *testing.T) {
	svc := &fakeRecoveryJobService{}
	handler := newQueryRouter(t, adminapi.RouterDeps{RecoveryJobQueryService: svc})

	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/system/settlement-recovery-jobs?user_id=7", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	if svc.gotListParams.UserID == nil || *svc.gotListParams.UserID != 7 {
		t.Fatalf("user_id filter not forwarded: %+v", svc.gotListParams.UserID)
	}
}

func TestListRecoveryJobsInvalidUserIDReturns400(t *testing.T) {
	handler := newQueryRouter(t, adminapi.RouterDeps{RecoveryJobQueryService: &fakeRecoveryJobService{}})

	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/system/settlement-recovery-jobs?user_id=abc", "", true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d (%s)", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}

func TestGetRecoveryJobDefaultHidesInternalDetail(t *testing.T) {
	svc := &fakeRecoveryJobService{getOut: query.RecoveryJobDetail{RecoveryJobSummary: query.RecoveryJobSummary{ID: 11, Status: "dead"}}}
	handler := newQueryRouter(t, adminapi.RouterDeps{RecoveryJobQueryService: svc})

	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/system/settlement-recovery-jobs/11", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	if svc.gotGetID != 11 || svc.gotInclude {
		t.Fatalf("expected id=11 include_internal=false, got id=%d include=%v", svc.gotGetID, svc.gotInclude)
	}
}

func TestGetRecoveryJobIncludeInternalForwarded(t *testing.T) {
	svc := &fakeRecoveryJobService{getOut: query.RecoveryJobDetail{RecoveryJobSummary: query.RecoveryJobSummary{ID: 11}}}
	handler := newQueryRouter(t, adminapi.RouterDeps{RecoveryJobQueryService: svc})

	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/system/settlement-recovery-jobs/11?include_internal=true", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	if !svc.gotInclude {
		t.Fatalf("expected include_internal=true forwarded")
	}
}

func TestGetRecoveryJobInvalidIDReturns400(t *testing.T) {
	handler := newQueryRouter(t, adminapi.RouterDeps{RecoveryJobQueryService: &fakeRecoveryJobService{}})

	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/system/settlement-recovery-jobs/not-an-int", "", true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d (%s)", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}

func TestGetRecoveryJobNotFoundReturns404(t *testing.T) {
	svc := &fakeRecoveryJobService{getErr: failure.New(failure.CodeAdminNotFound, failure.WithMessage("not found"))}
	handler := newQueryRouter(t, adminapi.RouterDeps{RecoveryJobQueryService: svc})

	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/system/settlement-recovery-jobs/999", "", true)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected %d, got %d (%s)", http.StatusNotFound, rec.Code, rec.Body.String())
	}
}

func TestRecoveryJobsRequireToken(t *testing.T) {
	handler := newQueryRouter(t, adminapi.RouterDeps{RecoveryJobQueryService: &fakeRecoveryJobService{}})

	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/system/settlement-recovery-jobs", "", false)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestChannelHealthReturns200AndForwardsRange(t *testing.T) {
	svc := &fakeChannelHealthService{out: []query.ChannelHealth{{ChannelID: 1, Bucket: "healthy"}}}
	handler := newQueryRouter(t, adminapi.RouterDeps{ChannelHealthQueryService: svc})

	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/system/channel-health?from=2026-06-01T00:00:00Z", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	if svc.gotFrom == nil {
		t.Fatalf("expected from forwarded")
	}
	if svc.gotTo != nil {
		t.Fatalf("expected to nil when unset")
	}
}

func TestChannelHealthInvalidTimeReturns400(t *testing.T) {
	handler := newQueryRouter(t, adminapi.RouterDeps{ChannelHealthQueryService: &fakeChannelHealthService{}})

	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/system/channel-health?to=not-a-time", "", true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d (%s)", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}
