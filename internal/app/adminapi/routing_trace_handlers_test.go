package adminapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/service/admin/routingtrace"
)

type fakeRoutingTraceService struct {
	getOut routingtrace.Decision
	getErr error
	getID  string
}

func (s *fakeRoutingTraceService) GetByRequestID(_ context.Context, requestID string) (routingtrace.Decision, error) {
	s.getID = requestID
	return s.getOut, s.getErr
}

func TestGetRequestRoutingDecisionAndAuth(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	svc := &fakeRoutingTraceService{getOut: routingtrace.Decision{
		ID: 9, RequestID: "req/with-space", TraceStatus: "complete", SchemaVersion: 1,
		AlgorithmVersion: "objective_v1", Process: json.RawMessage(`{"candidates":[{"channel_id":7}]}`),
		Summary:   routingtrace.Summary{PoolSize: 2, EligibleCount: 1, BaselineOrder: []int64{7, 9}, FallbackCount: 1},
		CreatedAt: now, UpdatedAt: now,
	}}
	handler := newQueryRouter(t, adminapi.RouterDeps{RoutingTraceService: svc})

	unauthorized := doAdmin(t, handler, http.MethodGet, "/v1/requests/req-9/routing-decision", "", false)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", unauthorized.Code)
	}
	rec := doAdmin(t, handler, http.MethodGet, "/v1/requests/req-9/routing-decision", "", true)
	if rec.Code != http.StatusOK || svc.getID != "req-9" {
		t.Fatalf("unexpected response/code: id=%q code=%d body=%s", svc.getID, rec.Code, rec.Body.String())
	}
	var body struct {
		Data struct {
			TraceStatus string `json:"trace_status"`
			Summary     struct {
				PoolSize      int32 `json:"pool_size"`
				FallbackCount int32 `json:"fallback_count"`
			} `json:"summary"`
			Process struct {
				Candidates []map[string]any `json:"candidates"`
			} `json:"process"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Data.TraceStatus != "complete" || body.Data.Summary.PoolSize != 2 ||
		body.Data.Summary.FallbackCount != 1 || len(body.Data.Process.Candidates) != 1 {
		t.Fatalf("unexpected structured routing trace: %s", rec.Body.String())
	}
}

func TestGetRequestRoutingDecisionNotFound(t *testing.T) {
	svc := &fakeRoutingTraceService{getErr: failure.New(failure.CodeAdminNotFound, failure.WithMessage("routing decision trace not found"))}
	handler := newQueryRouter(t, adminapi.RouterDeps{RoutingTraceService: svc})
	rec := doAdmin(t, handler, http.MethodGet, "/v1/requests/missing/routing-decision", "", true)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d (%s)", rec.Code, rec.Body.String())
	}
}
