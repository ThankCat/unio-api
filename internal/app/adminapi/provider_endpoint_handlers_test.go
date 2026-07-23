package adminapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi"
	"github.com/ThankCat/unio-gateway/internal/core/adminauth"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/service/admin/providerendpoint"
)

type fakeProviderEndpointService struct {
	createOut providerendpoint.ProviderEndpoint
	createErr error
	createIn  providerendpoint.CreateInput
	getOut    providerendpoint.ProviderEndpoint
	listOut   []providerendpoint.ProviderEndpoint
}

func (s *fakeProviderEndpointService) List(context.Context, providerendpoint.ListParams) (providerendpoint.ListResult, error) {
	return providerendpoint.ListResult{Items: s.listOut, Total: int64(len(s.listOut))}, nil
}
func (s *fakeProviderEndpointService) Get(context.Context, int64) (providerendpoint.ProviderEndpoint, error) {
	return s.getOut, nil
}
func (s *fakeProviderEndpointService) Create(_ context.Context, in providerendpoint.CreateInput) (providerendpoint.ProviderEndpoint, error) {
	s.createIn = in
	return s.createOut, s.createErr
}
func (s *fakeProviderEndpointService) UpdateName(context.Context, int64, string) (providerendpoint.ProviderEndpoint, error) {
	return s.getOut, nil
}
func (s *fakeProviderEndpointService) UpdateStatus(context.Context, int64, string) (providerendpoint.ProviderEndpoint, error) {
	return s.getOut, nil
}
func (s *fakeProviderEndpointService) UpdateBaseURL(context.Context, int64, string) (providerendpoint.ProviderEndpoint, error) {
	return s.getOut, nil
}
func (s *fakeProviderEndpointService) UpdateRouting(context.Context, int64, string, string) (providerendpoint.ProviderEndpoint, error) {
	return s.getOut, nil
}

func newProviderEndpointRouter(t *testing.T, svc adminapi.RouterDeps) http.Handler {
	t.Helper()
	authenticator, err := adminauth.NewStaticTokenAuthenticator(testAdminToken)
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}
	svc.Logger = zap.NewNop()
	svc.AdminAuthenticator = authenticator
	return adminapi.NewRouter(svc)
}

func TestCreateProviderEndpointReturns201(t *testing.T) {
	fake := &fakeProviderEndpointService{createOut: providerendpoint.ProviderEndpoint{
		ID: 5, ProviderID: 1, Name: "StarAPI", BaseURL: "https://open.codex521.cc",
		BaseURLRevision: 1, Status: "enabled", StatusRevision: 1,
	}}
	handler := newProviderEndpointRouter(t, adminapi.RouterDeps{ProviderEndpointService: fake})

	rec := doAdmin(t, handler, http.MethodPost, "/admin/v1/provider-endpoints",
		`{"provider_id":1,"name":"StarAPI","base_url":"https://Open.Codex521.cc/","status":"enabled"}`, true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.createIn.BaseURL != "https://Open.Codex521.cc/" {
		t.Fatalf("handler must pass raw base_url to service (service normalizes), got %q", fake.createIn.BaseURL)
	}
	var resp struct {
		Data struct {
			ID      int64  `json:"id"`
			BaseURL string `json:"base_url"`
			Status  string `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Data.ID != 5 || resp.Data.BaseURL != "https://open.codex521.cc" {
		t.Fatalf("unexpected response data: %+v", resp.Data)
	}
}

func TestCreateProviderEndpointRequiresAuth(t *testing.T) {
	handler := newProviderEndpointRouter(t, adminapi.RouterDeps{ProviderEndpointService: &fakeProviderEndpointService{}})
	rec := doAdmin(t, handler, http.MethodPost, "/admin/v1/provider-endpoints", `{"provider_id":1}`, false)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401 without token, got %d", rec.Code)
	}
}

type fakeEndpointBreaker struct {
	snap        breakerstore.ScopeSnapshot
	snapByID    map[int64]breakerstore.ScopeSnapshot
	snapshotErr error
	resetCall   int
}

func (f *fakeEndpointBreaker) Snapshot(_ context.Context, _ breakerstore.Scope, id int64) (breakerstore.ScopeSnapshot, error) {
	if f.snapshotErr != nil {
		return breakerstore.ScopeSnapshot{}, f.snapshotErr
	}
	if snapshot, ok := f.snapByID[id]; ok {
		return snapshot, nil
	}
	return f.snap, nil
}
func (f *fakeEndpointBreaker) Reset(context.Context, breakerstore.Scope, int64) (int64, error) {
	f.resetCall++
	return 2, nil
}

func TestEndpointRuntimeAndReset(t *testing.T) {
	brk := &fakeEndpointBreaker{snap: breakerstore.ScopeSnapshot{Scope: breakerstore.ScopeEndpoint, ID: 7, Exists: true, State: breakerstore.StateOpen, OpenRemainingMs: 12000}}
	handler := newProviderEndpointRouter(t, adminapi.RouterDeps{
		ProviderEndpointService: &fakeProviderEndpointService{},
		ProviderEndpointBreaker: brk,
	})
	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/provider-endpoints/7/ops/runtime", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("runtime want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	rec = doAdmin(t, handler, http.MethodDelete, "/admin/v1/provider-endpoints/7/ops/circuit-breaker", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("reset want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if brk.resetCall != 1 {
		t.Fatalf("reset should call breaker Reset once, got %d", brk.resetCall)
	}
}

func TestEndpointRuntimeUnavailableWhenNoBreaker(t *testing.T) {
	handler := newProviderEndpointRouter(t, adminapi.RouterDeps{ProviderEndpointService: &fakeProviderEndpointService{}})
	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/provider-endpoints/7/ops/runtime", "", true)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 when breaker unavailable, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestGetProviderEndpointReturnsData(t *testing.T) {
	fake := &fakeProviderEndpointService{getOut: providerendpoint.ProviderEndpoint{
		ID: 7, ProviderID: 2, Name: "EP", BaseURL: "https://x.example", Status: "enabled",
	}}
	handler := newProviderEndpointRouter(t, adminapi.RouterDeps{ProviderEndpointService: fake})
	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/provider-endpoints/7", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestGetProviderEndpointClassifiesRuntimeSync(t *testing.T) {
	endpoint := providerendpoint.ProviderEndpoint{
		ID: 7, ProviderID: 2, Name: "EP", BaseURL: "https://x.example",
		BaseURLRevision: 3, Status: "enabled", StatusRevision: 4,
	}
	tests := []struct {
		name      string
		breaker   *fakeEndpointBreaker
		wantState string
	}{
		{
			name: "active",
			breaker: &fakeEndpointBreaker{snap: breakerstore.ScopeSnapshot{
				Scope: breakerstore.ScopeEndpoint, ID: 7, Exists: true, ControlPresent: true,
				BaseURLRevision: 3, StatusRevision: 4, EffectiveStatus: "enabled",
				BaseURLRevisionState: "active", StatusRevisionState: "active",
			}},
			wantState: "active",
		},
		{
			name: "pending",
			breaker: &fakeEndpointBreaker{snap: breakerstore.ScopeSnapshot{
				Scope: breakerstore.ScopeEndpoint, ID: 7, Exists: true, ControlPresent: true,
				BaseURLRevision: 2, PendingBaseURLRevision: 3,
				StatusRevision: 4, EffectiveStatus: "enabled",
				BaseURLRevisionState: "pending", StatusRevisionState: "active",
			}},
			wantState: "runtime_sync_pending",
		},
		{
			name: "required",
			breaker: &fakeEndpointBreaker{snap: breakerstore.ScopeSnapshot{
				Scope: breakerstore.ScopeEndpoint, ID: 7,
			}},
			wantState: "runtime_sync_required",
		},
		{
			name: "stale",
			breaker: &fakeEndpointBreaker{snap: breakerstore.ScopeSnapshot{
				Scope: breakerstore.ScopeEndpoint, ID: 7, Exists: true, ControlPresent: true,
				BaseURLRevision: 2, StatusRevision: 4, EffectiveStatus: "enabled",
				BaseURLRevisionState: "active", StatusRevisionState: "active",
			}},
			wantState: "stale",
		},
		{
			name:      "store unavailable",
			breaker:   &fakeEndpointBreaker{snapshotErr: failure.New(failure.CodeDependencyRedisUnavailable)},
			wantState: "store_unavailable",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			handler := newProviderEndpointRouter(t, adminapi.RouterDeps{
				ProviderEndpointService: &fakeProviderEndpointService{getOut: endpoint},
				ProviderEndpointBreaker: tc.breaker,
			})
			rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/provider-endpoints/7", "", true)
			if rec.Code != http.StatusOK {
				t.Fatalf("business row must remain readable, got %d body=%s", rec.Code, rec.Body.String())
			}
			var response struct {
				Data struct {
					RuntimeSyncState              string `json:"runtime_sync_state"`
					RuntimeSyncPending            bool   `json:"runtime_sync_pending"`
					RuntimeActiveBaseURLRevision  *int64 `json:"runtime_active_base_url_revision"`
					RuntimePendingBaseURLRevision *int64 `json:"runtime_pending_base_url_revision"`
				} `json:"data"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if response.Data.RuntimeSyncState != tc.wantState {
				t.Fatalf("want state %q, got %+v", tc.wantState, response.Data)
			}
			if response.Data.RuntimeSyncPending != (tc.wantState == "runtime_sync_pending") {
				t.Fatalf("compatibility pending flag disagrees with state: %+v", response.Data)
			}
		})
	}
}

func TestListProviderEndpointsIncludesPerEndpointRuntimeSync(t *testing.T) {
	service := &fakeProviderEndpointService{listOut: []providerendpoint.ProviderEndpoint{
		{ID: 7, ProviderID: 2, BaseURLRevision: 3, Status: "enabled", StatusRevision: 4},
		{ID: 8, ProviderID: 2, BaseURLRevision: 1, Status: "disabled", StatusRevision: 2},
	}}
	breaker := &fakeEndpointBreaker{snapByID: map[int64]breakerstore.ScopeSnapshot{
		7: {
			Scope: breakerstore.ScopeEndpoint, ID: 7, Exists: true, ControlPresent: true,
			BaseURLRevision: 3, StatusRevision: 4, EffectiveStatus: "enabled",
			BaseURLRevisionState: "active", StatusRevisionState: "active",
		},
		8: {Scope: breakerstore.ScopeEndpoint, ID: 8},
	}}
	handler := newProviderEndpointRouter(t, adminapi.RouterDeps{
		ProviderEndpointService: service,
		ProviderEndpointBreaker: breaker,
	})
	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/provider-endpoints", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Data []struct {
			ID               int64  `json:"id"`
			RuntimeSyncState string `json:"runtime_sync_state"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(response.Data) != 2 || response.Data[0].RuntimeSyncState != "active" || response.Data[1].RuntimeSyncState != "runtime_sync_required" {
		t.Fatalf("unexpected list runtime states: %+v", response.Data)
	}
}
