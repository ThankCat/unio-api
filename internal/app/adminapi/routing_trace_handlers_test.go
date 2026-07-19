package adminapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/service/admin/routeruntime"
	"github.com/ThankCat/unio-gateway/internal/service/admin/routingtrace"
)

type fakeRoutingTraceService struct {
	listOut     []routingtrace.Decision
	listTotal   int64
	listRouteID int64
	listLimit   int32
	listOffset  int32
	getOut      routingtrace.Decision
	getErr      error
	getID       string
}

type fakeRouteRuntimeService struct {
	out    routeruntime.Runtime
	params routeruntime.Params
}

func (s *fakeRouteRuntimeService) Get(_ context.Context, params routeruntime.Params) (routeruntime.Runtime, error) {
	s.params = params
	return s.out, nil
}

func (s *fakeRoutingTraceService) ListByRoute(_ context.Context, routeID int64, limit, offset int32) ([]routingtrace.Decision, int64, error) {
	s.listRouteID, s.listLimit, s.listOffset = routeID, limit, offset
	return s.listOut, s.listTotal, nil
}

func (s *fakeRoutingTraceService) GetByRequestID(_ context.Context, requestID string) (routingtrace.Decision, error) {
	s.getID = requestID
	return s.getOut, s.getErr
}

func TestListRouteRoutingDecisions(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	svc := &fakeRoutingTraceService{
		listTotal: 3,
		listOut: []routingtrace.Decision{{
			ID: 8, RequestID: "req-trace", RouteID: 42, Mode: "balanced",
			CandidateScores: json.RawMessage(`[{"channel_id":7,"weight":0.5}]`),
			FallbackChain:   json.RawMessage(`[7,9]`), SelectedOrder: []int64{7, 9},
			AbnormalReasons: []string{"fallback"}, CreatedAt: now, UpdatedAt: now,
		}},
	}
	handler := newQueryRouter(t, adminapi.RouterDeps{RoutingTraceService: svc})
	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/routes/42/ops/decisions?page=2&page_size=1", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if svc.listRouteID != 42 || svc.listLimit != 1 || svc.listOffset != 1 {
		t.Fatalf("unexpected list params: route=%d limit=%d offset=%d", svc.listRouteID, svc.listLimit, svc.listOffset)
	}
	var body struct {
		Data []struct {
			RequestID       string           `json:"request_id"`
			CandidateScores []map[string]any `json:"candidate_scores"`
		} `json:"data"`
		Meta struct {
			Total int64 `json:"total"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Meta.Total != 3 || len(body.Data) != 1 || body.Data[0].RequestID != "req-trace" || len(body.Data[0].CandidateScores) != 1 {
		t.Fatalf("unexpected response: %s", rec.Body.String())
	}
}

func TestGetRequestRoutingDecisionAndAuth(t *testing.T) {
	svc := &fakeRoutingTraceService{getOut: routingtrace.Decision{
		ID: 9, RequestID: "req/with-space", RouteID: 2,
		CandidateScores: json.RawMessage(`[]`), FallbackChain: json.RawMessage(`[]`),
		AbnormalReasons: []string{}, SelectedOrder: []int64{},
	}}
	handler := newQueryRouter(t, adminapi.RouterDeps{RoutingTraceService: svc})

	unauthorized := doAdmin(t, handler, http.MethodGet, "/admin/v1/requests/req-9/routing-decision", "", false)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", unauthorized.Code)
	}
	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/requests/req-9/routing-decision", "", true)
	if rec.Code != http.StatusOK || svc.getID != "req-9" {
		t.Fatalf("unexpected response/code: id=%q code=%d body=%s", svc.getID, rec.Code, rec.Body.String())
	}
}

func TestGetRequestRoutingDecisionNotFound(t *testing.T) {
	svc := &fakeRoutingTraceService{getErr: failure.New(failure.CodeAdminNotFound, failure.WithMessage("routing decision trace not found"))}
	handler := newQueryRouter(t, adminapi.RouterDeps{RoutingTraceService: svc})
	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/requests/missing/routing-decision", "", true)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestGetRouteRuntime(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	svc := &fakeRouteRuntimeService{out: routeruntime.Runtime{
		RouteID: 42, Mode: "balanced", RouteStatus: "enabled", ObservedAt: now,
		PoolSize: 2, CandidateCount: 1, NoRedundancy: true,
		Sources:  []routeruntime.Source{{Name: "redis", Available: true, ObservedAt: now}},
		Channels: []routeruntime.Channel{{ChannelID: 7, Eligible: true, FinalWeight: 0.4}},
	}}
	handler := newQueryRouter(t, adminapi.RouterDeps{RouteRuntimeService: svc})
	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/routes/42/ops/runtime?model_id=openai%2Fgpt&protocol=openai", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if svc.params.RouteID != 42 || svc.params.ModelID != "openai/gpt" || svc.params.Protocol != "openai" {
		t.Fatalf("unexpected runtime params: %+v", svc.params)
	}
	var body struct {
		Data struct {
			NoRedundancy bool `json:"no_redundancy"`
			Channels     []struct {
				FinalWeight float64 `json:"final_weight"`
			} `json:"channels"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode runtime response: %v", err)
	}
	if !body.Data.NoRedundancy || len(body.Data.Channels) != 1 || body.Data.Channels[0].FinalWeight != 0.4 {
		t.Fatalf("unexpected runtime response: %s", rec.Body.String())
	}
}
