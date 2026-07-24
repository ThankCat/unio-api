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
	"github.com/ThankCat/unio-gateway/internal/service/admin/channel"
)

type fakeChannelBreaker struct {
	snapshot breakerstore.ScopeSnapshot

	snapshotErr error
	resetErr    error
	control     breakerstore.ControlSnapshot
	controlErr  error

	snapshotCalls int
	snapshotScope breakerstore.Scope
	snapshotID    int64
	resetCalls    int
	resetScope    breakerstore.Scope
	resetID       int64
}

func (f *fakeChannelBreaker) Snapshot(_ context.Context, scope breakerstore.Scope, id int64) (breakerstore.ScopeSnapshot, error) {
	f.snapshotCalls++
	f.snapshotScope = scope
	f.snapshotID = id
	return f.snapshot, f.snapshotErr
}

func (f *fakeChannelBreaker) Reset(_ context.Context, scope breakerstore.Scope, id int64) (int64, error) {
	f.resetCalls++
	f.resetScope = scope
	f.resetID = id
	if f.resetErr != nil {
		return 0, f.resetErr
	}
	f.snapshot.Scope = breakerstore.ScopeChannel
	f.snapshot.ID = id
	f.snapshot.Exists = true
	f.snapshot.State = breakerstore.StateClosed
	f.snapshot.OpenRemainingMs = 0
	f.snapshot.OpenLevel = 0
	f.snapshot.EligibleSuccesses = 0
	f.snapshot.EligibleFailures = 0
	f.snapshot.ConsecutiveFailures = 0
	f.snapshot.ErrorRate = 0
	f.snapshot.SampleCount = 0
	f.snapshot.TTFTEWMAMs = 0
	f.snapshot.TTFTSamples = 0
	return 9, nil
}

func (f *fakeChannelBreaker) ChannelAdmissionControl(int64) breakerstore.ControlTarget {
	return breakerstore.ControlTarget{}
}

func (f *fakeChannelBreaker) ReadControl(context.Context, breakerstore.ControlTarget, int64) (breakerstore.ControlSnapshot, error) {
	return f.control, f.controlErr
}

func newChannelBreakerRouter(t *testing.T, breaker adminapi.RouterDeps) http.Handler {
	t.Helper()
	authenticator, err := adminauth.NewStaticTokenAuthenticator(testAdminToken)
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}
	breaker.Logger = zap.NewNop()
	breaker.AdminAuthenticator = authenticator
	return adminapi.NewRouter(breaker)
}

func TestChannelBreakerRuntimeAndReset(t *testing.T) {
	channelFacts := channel.Channel{
		ID: 17, ProviderOriginID: 23,
		ProviderOriginBaseURLRevision: 4, ProviderOriginStatusRevision: 5,
		ConfigRevision: 6, AdmissionLimitsRevision: 7,
	}
	breaker := &fakeChannelBreaker{snapshot: breakerstore.ScopeSnapshot{
		Scope:                 breakerstore.ScopeChannel,
		ID:                    17,
		Exists:                true,
		State:                 breakerstore.StateOpen,
		OpenRemainingMs:       12_000,
		OpenLevel:             2,
		EligibleSuccesses:     8,
		EligibleFailures:      12,
		ConsecutiveFailures:   3,
		ErrorRate:             0.6,
		SampleCount:           20,
		TTFTEWMAMs:            321.5,
		TTFTSamples:           4,
		ProviderOriginID:    23,
		BaseURLRevision:       4,
		StatusRevision:        5,
		ChannelConfigRevision: 6,
	}, control: breakerstore.ControlSnapshot{
		ActiveRevision: 7,
		ActivePayload:  `{"rpm":null,"rpd":null,"tpm":null,"concurrency":null}`,
		SyncState:      "active",
	}}
	handler := newChannelBreakerRouter(t, adminapi.RouterDeps{
		ChannelService: &fakeChannelService{getOut: channelFacts},
		ChannelBreaker: breaker,
	})

	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/channels/17/ops/runtime", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("runtime want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var runtimeResponse struct {
		Data struct {
			ID                             int64  `json:"id"`
			ProviderOriginID             int64  `json:"provider_origin_id"`
			OriginBaseURLRevision        int64  `json:"origin_base_url_revision"`
			OriginStatusRevision         int64  `json:"origin_status_revision"`
			ConfigRevision                 int64  `json:"config_revision"`
			AdmissionLimitsRevision        int64  `json:"admission_limits_revision"`
			RuntimeSyncState               string `json:"runtime_sync_state"`
			RuntimeProviderOriginID      *int64 `json:"runtime_provider_origin_id"`
			RuntimeConfigRevision          *int64 `json:"runtime_config_revision"`
			RuntimeAdmissionActiveRevision *int64 `json:"runtime_admission_active_revision"`
			AdmissionPayloadMatches        bool   `json:"admission_payload_matches"`
			Breaker                        *struct {
				Scope               string  `json:"scope"`
				ID                  int64   `json:"id"`
				Exists              bool    `json:"exists"`
				State               string  `json:"state"`
				OpenRemainingMs     int64   `json:"open_remaining_ms"`
				OpenLevel           int     `json:"open_level"`
				EligibleSuccesses   int64   `json:"eligible_successes"`
				EligibleFailures    int64   `json:"eligible_failures"`
				ConsecutiveFailures int64   `json:"consecutive_failures"`
				ErrorRate           float64 `json:"error_rate"`
				SampleCount         int64   `json:"sample_count"`
				TTFTEWMAMs          float64 `json:"ttft_ewma_ms"`
				TTFTSamples         int64   `json:"ttft_samples"`
				TTFTSampleSource    string  `json:"ttft_sample_source"`
			} `json:"breaker"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &runtimeResponse); err != nil {
		t.Fatalf("decode runtime response: %v", err)
	}
	got := runtimeResponse.Data
	if got.ID != 17 || got.ProviderOriginID != 23 || got.ConfigRevision != 6 || got.AdmissionLimitsRevision != 7 || got.RuntimeSyncState != "active" {
		t.Fatalf("unexpected runtime identity/state: %+v", got)
	}
	if got.RuntimeProviderOriginID == nil || *got.RuntimeProviderOriginID != 23 || got.RuntimeConfigRevision == nil || *got.RuntimeConfigRevision != 6 ||
		got.RuntimeAdmissionActiveRevision == nil || *got.RuntimeAdmissionActiveRevision != 7 || !got.AdmissionPayloadMatches {
		t.Fatalf("unexpected runtime revisions: %+v", got)
	}
	if got.Breaker == nil || got.Breaker.Scope != "channel" || got.Breaker.ID != 17 || !got.Breaker.Exists || got.Breaker.State != "open" {
		t.Fatalf("unexpected breaker identity/state: %+v", got.Breaker)
	}
	if got.Breaker.OpenRemainingMs != 12_000 || got.Breaker.OpenLevel != 2 || got.Breaker.EligibleSuccesses != 8 || got.Breaker.EligibleFailures != 12 || got.Breaker.ConsecutiveFailures != 3 {
		t.Fatalf("unexpected runtime breaker counters: %+v", got)
	}
	if got.Breaker.ErrorRate != 0.6 || got.Breaker.SampleCount != 20 || got.Breaker.TTFTEWMAMs != 321.5 || got.Breaker.TTFTSamples != 4 || got.Breaker.TTFTSampleSource != "stream_only" {
		t.Fatalf("unexpected runtime samples: %+v", got)
	}
	if breaker.snapshotCalls != 1 || breaker.snapshotScope != breakerstore.ScopeChannel || breaker.snapshotID != 17 {
		t.Fatalf("runtime snapshot called with wrong target: calls=%d scope=%q id=%d", breaker.snapshotCalls, breaker.snapshotScope, breaker.snapshotID)
	}

	rec = doAdmin(t, handler, http.MethodDelete, "/admin/v1/channels/17/ops/circuit-breaker", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("reset want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if breaker.resetCalls != 1 || breaker.resetScope != breakerstore.ScopeChannel || breaker.resetID != 17 {
		t.Fatalf("reset called with wrong target: calls=%d scope=%q id=%d", breaker.resetCalls, breaker.resetScope, breaker.resetID)
	}
	if breaker.snapshotCalls != 2 {
		t.Fatalf("reset must return a fresh post-reset snapshot, calls=%d", breaker.snapshotCalls)
	}
	var resetResponse struct {
		Data struct {
			RuntimeSyncState string `json:"runtime_sync_state"`
			Breaker          *struct {
				State            string  `json:"state"`
				SampleCount      int64   `json:"sample_count"`
				TTFTEWMAMs       float64 `json:"ttft_ewma_ms"`
				TTFTSamples      int64   `json:"ttft_samples"`
				TTFTSampleSource string  `json:"ttft_sample_source"`
			} `json:"breaker"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resetResponse); err != nil {
		t.Fatalf("decode reset response: %v", err)
	}
	if resetResponse.Data.RuntimeSyncState != "active" || resetResponse.Data.Breaker == nil ||
		resetResponse.Data.Breaker.State != "closed" || resetResponse.Data.Breaker.SampleCount != 0 ||
		resetResponse.Data.Breaker.TTFTEWMAMs != 0 || resetResponse.Data.Breaker.TTFTSamples != 0 ||
		resetResponse.Data.Breaker.TTFTSampleSource != "stream_only" {
		t.Fatalf("reset must return closed/no-sample state: %+v", resetResponse.Data)
	}
}

func TestChannelBreakerRuntimeUnavailable(t *testing.T) {
	t.Run("not wired", func(t *testing.T) {
		handler := newChannelBreakerRouter(t, adminapi.RouterDeps{})
		for _, methodAndPath := range []struct {
			method string
			path   string
		}{
			{method: http.MethodGet, path: "/admin/v1/channels/17/ops/runtime"},
			{method: http.MethodDelete, path: "/admin/v1/channels/17/ops/circuit-breaker"},
		} {
			rec := doAdmin(t, handler, methodAndPath.method, methodAndPath.path, "", true)
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("%s %s want 503, got %d body=%s", methodAndPath.method, methodAndPath.path, rec.Code, rec.Body.String())
			}
		}
	})

	t.Run("redis snapshot failed", func(t *testing.T) {
		breaker := &fakeChannelBreaker{snapshotErr: failure.New(failure.CodeDependencyRedisUnavailable)}
		handler := newChannelBreakerRouter(t, adminapi.RouterDeps{
			ChannelService: &fakeChannelService{getOut: channel.Channel{ID: 17}},
			ChannelBreaker: breaker,
		})
		rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/channels/17/ops/runtime", "", true)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("want 503, got %d body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("redis reset failed", func(t *testing.T) {
		breaker := &fakeChannelBreaker{resetErr: failure.New(failure.CodeDependencyRedisUnavailable)}
		handler := newChannelBreakerRouter(t, adminapi.RouterDeps{
			ChannelService: &fakeChannelService{getOut: channel.Channel{ID: 17}},
			ChannelBreaker: breaker,
		})
		rec := doAdmin(t, handler, http.MethodDelete, "/admin/v1/channels/17/ops/circuit-breaker", "", true)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("want 503, got %d body=%s", rec.Code, rec.Body.String())
		}
		if breaker.snapshotCalls != 0 {
			t.Fatalf("failed reset must not return a stale snapshot, calls=%d", breaker.snapshotCalls)
		}
	})
}

func TestChannelRuntimeDoesNotExposeStaleBreakerSamples(t *testing.T) {
	breaker := &fakeChannelBreaker{
		snapshot: breakerstore.ScopeSnapshot{
			Scope: breakerstore.ScopeChannel, ID: 17, Exists: true, State: breakerstore.StateOpen,
			ProviderOriginID: 23, BaseURLRevision: 4, StatusRevision: 5,
			ChannelConfigRevision: 5, TTFTSamples: 9, TTFTEWMAMs: 999,
		},
		control: breakerstore.ControlSnapshot{
			ActiveRevision: 7,
			ActivePayload:  `{"rpm":null,"rpd":null,"tpm":null,"concurrency":null}`,
			SyncState:      "active",
		},
	}
	handler := newChannelBreakerRouter(t, adminapi.RouterDeps{
		ChannelService: &fakeChannelService{getOut: channel.Channel{
			ID: 17, ProviderOriginID: 23,
			ProviderOriginBaseURLRevision: 4, ProviderOriginStatusRevision: 5,
			ConfigRevision: 6, AdmissionLimitsRevision: 7,
		}},
		ChannelBreaker: breaker,
	})
	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/channels/17/ops/runtime", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("runtime want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Data struct {
			RuntimeSyncState      string `json:"runtime_sync_state"`
			RuntimeConfigRevision *int64 `json:"runtime_config_revision"`
			Breaker               *any   `json:"breaker"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Data.RuntimeSyncState != "stale" || response.Data.RuntimeConfigRevision == nil || *response.Data.RuntimeConfigRevision != 5 {
		t.Fatalf("stale revision must remain diagnosable: %+v", response.Data)
	}
	if response.Data.Breaker != nil {
		t.Fatalf("stale open/TTFT facts must be hidden, got %+v", response.Data.Breaker)
	}
}

func TestChannelRuntimeTreatsMissingStateAsNoSample(t *testing.T) {
	breaker := &fakeChannelBreaker{
		snapshot: breakerstore.ScopeSnapshot{Scope: breakerstore.ScopeChannel, ID: 17},
		control: breakerstore.ControlSnapshot{
			ActiveRevision: 7,
			ActivePayload:  `{"rpm":null,"rpd":null,"tpm":null,"concurrency":null}`,
			SyncState:      "active",
		},
	}
	handler := newChannelBreakerRouter(t, adminapi.RouterDeps{
		ChannelService: &fakeChannelService{getOut: channel.Channel{ID: 17, AdmissionLimitsRevision: 7}},
		ChannelBreaker: breaker,
	})
	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/channels/17/ops/runtime", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("runtime want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Data struct {
			RuntimeSyncState string `json:"runtime_sync_state"`
			Breaker          struct {
				Exists bool `json:"exists"`
			} `json:"breaker"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Data.RuntimeSyncState != "active" || response.Data.Breaker.Exists {
		t.Fatalf("missing Redis state must be active/no-sample, got %+v", response.Data)
	}
}

func TestChannelBreakerRejectsInvalidIDBeforeRedis(t *testing.T) {
	breaker := &fakeChannelBreaker{}
	handler := newChannelBreakerRouter(t, adminapi.RouterDeps{ChannelBreaker: breaker})

	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/channels/not-an-id/ops/runtime", "", true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	if breaker.snapshotCalls != 0 || breaker.resetCalls != 0 {
		t.Fatalf("invalid id must be rejected before Redis: snapshot=%d reset=%d", breaker.snapshotCalls, breaker.resetCalls)
	}
}
