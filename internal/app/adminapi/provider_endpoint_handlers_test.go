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
	"github.com/ThankCat/unio-gateway/internal/service/admin/providerorigin"
)

type fakeProviderOriginService struct {
	createOut providerorigin.ProviderOrigin
	createErr error
	createIn  providerorigin.CreateInput
	getOut    providerorigin.ProviderOrigin
	listOut   []providerorigin.ProviderOrigin
}

func (s *fakeProviderOriginService) List(context.Context, providerorigin.ListParams) (providerorigin.ListResult, error) {
	return providerorigin.ListResult{Items: s.listOut, Total: int64(len(s.listOut))}, nil
}
func (s *fakeProviderOriginService) Get(context.Context, int64) (providerorigin.ProviderOrigin, error) {
	return s.getOut, nil
}
func (s *fakeProviderOriginService) Create(_ context.Context, in providerorigin.CreateInput) (providerorigin.ProviderOrigin, error) {
	s.createIn = in
	return s.createOut, s.createErr
}
func (s *fakeProviderOriginService) UpdateName(context.Context, int64, string) (providerorigin.ProviderOrigin, error) {
	return s.getOut, nil
}
func (s *fakeProviderOriginService) UpdateStatus(context.Context, int64, string) (providerorigin.ProviderOrigin, error) {
	return s.getOut, nil
}
func (s *fakeProviderOriginService) UpdateBaseURL(context.Context, int64, string) (providerorigin.ProviderOrigin, error) {
	return s.getOut, nil
}
func (s *fakeProviderOriginService) UpdateRouting(context.Context, int64, string, string) (providerorigin.ProviderOrigin, error) {
	return s.getOut, nil
}

func newProviderOriginRouter(t *testing.T, svc adminapi.RouterDeps) http.Handler {
	t.Helper()
	authenticator, err := adminauth.NewStaticTokenAuthenticator(testAdminToken)
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}
	svc.Logger = zap.NewNop()
	svc.AdminAuthenticator = authenticator
	return adminapi.NewRouter(svc)
}

func TestCreateProviderOriginReturns201(t *testing.T) {
	fake := &fakeProviderOriginService{createOut: providerorigin.ProviderOrigin{
		ID: 5, ProviderID: 1, Name: "StarAPI", BaseURL: "https://open.codex521.cc",
		BaseURLRevision: 1, Status: "enabled", StatusRevision: 1,
	}}
	handler := newProviderOriginRouter(t, adminapi.RouterDeps{ProviderOriginService: fake})

	rec := doAdmin(t, handler, http.MethodPost, "/admin/v1/provider-origins",
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

func TestCreateProviderOriginRequiresAuth(t *testing.T) {
	handler := newProviderOriginRouter(t, adminapi.RouterDeps{ProviderOriginService: &fakeProviderOriginService{}})
	rec := doAdmin(t, handler, http.MethodPost, "/admin/v1/provider-origins", `{"provider_id":1}`, false)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401 without token, got %d", rec.Code)
	}
}

type fakeOriginBreaker struct {
	snap        breakerstore.ScopeSnapshot
	snapByID    map[int64]breakerstore.ScopeSnapshot
	snapshotErr error
	resetCall   int
}

func (f *fakeOriginBreaker) Snapshot(_ context.Context, _ breakerstore.Scope, id int64) (breakerstore.ScopeSnapshot, error) {
	if f.snapshotErr != nil {
		return breakerstore.ScopeSnapshot{}, f.snapshotErr
	}
	if snapshot, ok := f.snapByID[id]; ok {
		return snapshot, nil
	}
	return f.snap, nil
}
func (f *fakeOriginBreaker) Reset(context.Context, breakerstore.Scope, int64) (int64, error) {
	f.resetCall++
	return 2, nil
}

func TestOriginRuntimeAndReset(t *testing.T) {
	brk := &fakeOriginBreaker{snap: breakerstore.ScopeSnapshot{Scope: breakerstore.ScopeOrigin, ID: 7, Exists: true, State: breakerstore.StateOpen, OpenRemainingMs: 12000}}
	handler := newProviderOriginRouter(t, adminapi.RouterDeps{
		ProviderOriginService: &fakeProviderOriginService{},
		ProviderOriginBreaker: brk,
	})
	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/provider-origins/7/ops/runtime", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("runtime want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	rec = doAdmin(t, handler, http.MethodDelete, "/admin/v1/provider-origins/7/ops/circuit-breaker", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("reset want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if brk.resetCall != 1 {
		t.Fatalf("reset should call breaker Reset once, got %d", brk.resetCall)
	}
}

func TestOriginRuntimeUnavailableWhenNoBreaker(t *testing.T) {
	handler := newProviderOriginRouter(t, adminapi.RouterDeps{ProviderOriginService: &fakeProviderOriginService{}})
	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/provider-origins/7/ops/runtime", "", true)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 when breaker unavailable, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestGetProviderOriginReturnsData(t *testing.T) {
	fake := &fakeProviderOriginService{getOut: providerorigin.ProviderOrigin{
		ID: 7, ProviderID: 2, Name: "EP", BaseURL: "https://x.example", Status: "enabled",
	}}
	handler := newProviderOriginRouter(t, adminapi.RouterDeps{ProviderOriginService: fake})
	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/provider-origins/7", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestGetProviderOriginClassifiesRuntimeSync(t *testing.T) {
	origin := providerorigin.ProviderOrigin{
		ID: 7, ProviderID: 2, Name: "EP", BaseURL: "https://x.example",
		BaseURLRevision: 3, Status: "enabled", StatusRevision: 4,
	}
	tests := []struct {
		name      string
		breaker   *fakeOriginBreaker
		wantState string
	}{
		{
			name: "active",
			breaker: &fakeOriginBreaker{snap: breakerstore.ScopeSnapshot{
				Scope: breakerstore.ScopeOrigin, ID: 7, Exists: true, ControlPresent: true,
				BaseURLRevision: 3, StatusRevision: 4, EffectiveStatus: "enabled",
				BaseURLRevisionState: "active", StatusRevisionState: "active",
			}},
			wantState: "active",
		},
		{
			name: "pending",
			breaker: &fakeOriginBreaker{snap: breakerstore.ScopeSnapshot{
				Scope: breakerstore.ScopeOrigin, ID: 7, Exists: true, ControlPresent: true,
				BaseURLRevision: 2, PendingBaseURLRevision: 3,
				StatusRevision: 4, EffectiveStatus: "enabled",
				BaseURLRevisionState: "pending", StatusRevisionState: "active",
			}},
			wantState: "runtime_sync_pending",
		},
		{
			name: "required",
			breaker: &fakeOriginBreaker{snap: breakerstore.ScopeSnapshot{
				Scope: breakerstore.ScopeOrigin, ID: 7,
			}},
			wantState: "runtime_sync_required",
		},
		{
			name: "stale",
			breaker: &fakeOriginBreaker{snap: breakerstore.ScopeSnapshot{
				Scope: breakerstore.ScopeOrigin, ID: 7, Exists: true, ControlPresent: true,
				BaseURLRevision: 2, StatusRevision: 4, EffectiveStatus: "enabled",
				BaseURLRevisionState: "active", StatusRevisionState: "active",
			}},
			wantState: "stale",
		},
		{
			name:      "store unavailable",
			breaker:   &fakeOriginBreaker{snapshotErr: failure.New(failure.CodeDependencyRedisUnavailable)},
			wantState: "store_unavailable",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			handler := newProviderOriginRouter(t, adminapi.RouterDeps{
				ProviderOriginService: &fakeProviderOriginService{getOut: origin},
				ProviderOriginBreaker: tc.breaker,
			})
			rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/provider-origins/7", "", true)
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

func TestListProviderOriginsIncludesPerOriginRuntimeSync(t *testing.T) {
	service := &fakeProviderOriginService{listOut: []providerorigin.ProviderOrigin{
		{ID: 7, ProviderID: 2, BaseURLRevision: 3, Status: "enabled", StatusRevision: 4},
		{ID: 8, ProviderID: 2, BaseURLRevision: 1, Status: "disabled", StatusRevision: 2},
	}}
	breaker := &fakeOriginBreaker{snapByID: map[int64]breakerstore.ScopeSnapshot{
		7: {
			Scope: breakerstore.ScopeOrigin, ID: 7, Exists: true, ControlPresent: true,
			BaseURLRevision: 3, StatusRevision: 4, EffectiveStatus: "enabled",
			BaseURLRevisionState: "active", StatusRevisionState: "active",
		},
		8: {Scope: breakerstore.ScopeOrigin, ID: 8},
	}}
	handler := newProviderOriginRouter(t, adminapi.RouterDeps{
		ProviderOriginService: service,
		ProviderOriginBreaker: breaker,
	})
	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/provider-origins", "", true)
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
