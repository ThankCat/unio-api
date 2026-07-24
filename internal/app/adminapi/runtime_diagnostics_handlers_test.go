package adminapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi"
	"github.com/ThankCat/unio-gateway/internal/service/admin/runtimediagnostics"
)

type fakeRuntimeDiagnosticsService struct {
	result runtimediagnostics.Diagnostics
	err    error
}

func (s *fakeRuntimeDiagnosticsService) Get(context.Context) (runtimediagnostics.Diagnostics, error) {
	return s.result, s.err
}

func TestGetRuntimeDiagnosticsReturnsRedactedMaintenanceView(t *testing.T) {
	originAge := int64(17)
	runtimeAge := int64(29)
	service := &fakeRuntimeDiagnosticsService{result: runtimediagnostics.Diagnostics{
		Readiness:         runtimediagnostics.Readiness{Ready: false, Reason: "marker_mismatch"},
		RuntimeStateEpoch: runtimediagnostics.StateEpoch{State: "ready", Revision: 7, Match: false},
		Operations: runtimediagnostics.Operations{
			OriginRouting: runtimediagnostics.OperationSummary{NonterminalCount: 1, OldestAgeSeconds: &originAge},
			RuntimeControl:  runtimediagnostics.OperationSummary{NonterminalCount: 2, OldestAgeSeconds: &runtimeAge},
		},
	}}
	handler := newQueryRouter(t, adminapi.RouterDeps{RuntimeDiagnosticsService: service})

	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/system/runtime-diagnostics", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "00112233445566778899aabbccddeeff") {
		t.Fatalf("response must not expose the epoch identity: %s", rec.Body.String())
	}
	var response struct {
		Data struct {
			Readiness struct {
				Ready  bool   `json:"ready"`
				Reason string `json:"reason"`
			} `json:"readiness"`
			RuntimeStateEpoch struct {
				State    string `json:"state"`
				Revision int64  `json:"revision"`
				Match    bool   `json:"match"`
			} `json:"runtime_state_epoch"`
			Operations struct {
				OriginRouting struct {
					NonterminalCount int64  `json:"nonterminal_count"`
					OldestAgeSeconds *int64 `json:"oldest_age_seconds"`
				} `json:"origin_routing"`
				RuntimeControl struct {
					NonterminalCount int64  `json:"nonterminal_count"`
					OldestAgeSeconds *int64 `json:"oldest_age_seconds"`
				} `json:"runtime_control"`
			} `json:"operations"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Data.Readiness.Ready || response.Data.Readiness.Reason != "marker_mismatch" ||
		response.Data.RuntimeStateEpoch.State != "ready" || response.Data.RuntimeStateEpoch.Revision != 7 ||
		response.Data.RuntimeStateEpoch.Match {
		t.Fatalf("unexpected diagnostic facts: %+v", response.Data)
	}
	if response.Data.Operations.OriginRouting.NonterminalCount != 1 ||
		response.Data.Operations.OriginRouting.OldestAgeSeconds == nil ||
		*response.Data.Operations.OriginRouting.OldestAgeSeconds != originAge ||
		response.Data.Operations.RuntimeControl.NonterminalCount != 2 ||
		response.Data.Operations.RuntimeControl.OldestAgeSeconds == nil ||
		*response.Data.Operations.RuntimeControl.OldestAgeSeconds != runtimeAge {
		t.Fatalf("unexpected endpoint summaries: %+v", response.Data.Operations)
	}
}

func TestGetRuntimeDiagnosticsReturnsServiceError(t *testing.T) {
	handler := newQueryRouter(t, adminapi.RouterDeps{
		RuntimeDiagnosticsService: &fakeRuntimeDiagnosticsService{err: errors.New("postgres unavailable")},
	})

	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/system/runtime-diagnostics", "", true)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected %d, got %d (%s)", http.StatusInternalServerError, rec.Code, rec.Body.String())
	}
}

func TestRuntimeDiagnosticsRequiresAdminToken(t *testing.T) {
	handler := newQueryRouter(t, adminapi.RouterDeps{
		RuntimeDiagnosticsService: &fakeRuntimeDiagnosticsService{},
	})

	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/system/runtime-diagnostics", "", false)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}
