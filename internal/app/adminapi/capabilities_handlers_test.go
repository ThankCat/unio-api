package adminapi_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/ThankCat/unio-api/internal/app/adminapi"
	"github.com/ThankCat/unio-api/internal/core/adminauth"
	corecap "github.com/ThankCat/unio-api/internal/core/capability"
	"github.com/ThankCat/unio-api/internal/core/modelcatalog"
	"github.com/ThankCat/unio-api/internal/platform/failure"
	capsvc "github.com/ThankCat/unio-api/internal/service/admin/capability"
)

type fakeCapabilityService struct {
	keys          []string
	setModelErr   error
	setChannelErr error
}

func (f *fakeCapabilityService) Keys() []string { return f.keys }
func (f *fakeCapabilityService) ListModelCapabilities(context.Context, int64) ([]corecap.ModelCapability, error) {
	return nil, nil
}
func (f *fakeCapabilityService) SetModelCapability(context.Context, capsvc.SetModelCapabilityInput) (corecap.ModelCapability, error) {
	return corecap.ModelCapability{}, f.setModelErr
}
func (f *fakeCapabilityService) DeleteModelCapability(context.Context, int64, string) error {
	return nil
}
func (f *fakeCapabilityService) ListChannelOverrides(context.Context, int64) ([]corecap.ChannelOverride, error) {
	return nil, nil
}
func (f *fakeCapabilityService) SetChannelOverride(context.Context, capsvc.SetChannelOverrideInput) (corecap.ChannelOverride, error) {
	return corecap.ChannelOverride{}, f.setChannelErr
}
func (f *fakeCapabilityService) DeleteChannelOverride(context.Context, int64, string) error {
	return nil
}
func (f *fakeCapabilityService) ListSuggestions(context.Context, string) ([]corecap.CapabilitySuggestion, error) {
	return nil, nil
}
func (f *fakeCapabilityService) AcceptSuggestion(context.Context, int64, string, string) (corecap.ModelCapability, error) {
	return corecap.ModelCapability{}, nil
}
func (f *fakeCapabilityService) DismissSuggestion(context.Context, int64, string, string) error {
	return nil
}
func (f *fakeCapabilityService) GetAutocalibrateMode(context.Context, int64) (string, error) {
	return "suggest", nil
}
func (f *fakeCapabilityService) SetAutocalibrateMode(context.Context, int64, string) error {
	return nil
}

type fakeCapabilitySyncService struct{}

func (fakeCapabilitySyncService) ListJobs(context.Context, int32) ([]corecap.SyncJob, error) {
	return []corecap.SyncJob{}, nil
}
func (fakeCapabilitySyncService) Trigger(_ context.Context, dryRun bool) (modelcatalog.Result, error) {
	return modelcatalog.Result{DryRun: dryRun}, nil
}

type fakeCapabilitySeedService struct{}

func (fakeCapabilitySeedService) Profiles() []capsvc.Profile { return []capsvc.Profile{} }
func (fakeCapabilitySeedService) Materialize(context.Context, int64, string, string) (capsvc.MaterializeResult, error) {
	return capsvc.MaterializeResult{}, nil
}

type fakeCapabilityEnforcementService struct{}

func (fakeCapabilityEnforcementService) Modes() []capsvc.EnforcementMode {
	return []capsvc.EnforcementMode{}
}
func (fakeCapabilityEnforcementService) ObserveSummary(context.Context, *time.Time, *time.Time) ([]capsvc.ResultCount, error) {
	return []capsvc.ResultCount{}, nil
}

func newCapabilityRouter(
	t *testing.T,
	cap adminapi.CapabilityService,
	sync adminapi.CapabilitySyncService,
	seed adminapi.CapabilitySeedService,
	enf adminapi.CapabilityEnforcementService,
) http.Handler {
	t.Helper()

	authenticator, err := adminauth.NewStaticTokenAuthenticator(testAdminToken)
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}

	return adminapi.NewRouter(adminapi.RouterDeps{
		Logger:                       slog.New(slog.NewTextHandler(io.Discard, nil)),
		AdminAuthenticator:           authenticator,
		CapabilityService:            cap,
		CapabilitySyncService:        sync,
		CapabilitySeedService:        seed,
		CapabilityEnforcementService: enf,
	})
}

func TestListCapabilityKeysReturns200(t *testing.T) {
	handler := newCapabilityRouter(t, &fakeCapabilityService{keys: []string{"text.input"}}, nil, nil, nil)

	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/capability/keys", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
}

func TestCapabilityKeysRequireToken(t *testing.T) {
	handler := newCapabilityRouter(t, &fakeCapabilityService{}, nil, nil, nil)

	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/capability/keys", "", false)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestSetModelCapabilityReturns200(t *testing.T) {
	handler := newCapabilityRouter(t, &fakeCapabilityService{}, nil, nil, nil)

	rec := doAdmin(t, handler, http.MethodPut, "/admin/v1/models/1/capabilities/tools.function", `{"support_level":"full"}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
}

func TestSetModelCapabilityInvalidArgReturns400(t *testing.T) {
	handler := newCapabilityRouter(t, &fakeCapabilityService{
		setModelErr: failure.New(failure.CodeAdminInvalidArgument, failure.WithMessage("support_level must be full, limited or unsupported")),
	}, nil, nil, nil)

	rec := doAdmin(t, handler, http.MethodPut, "/admin/v1/models/1/capabilities/tools.function", `{"support_level":"weird"}`, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d (%s)", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}

func TestDeleteModelCapabilityReturns204(t *testing.T) {
	handler := newCapabilityRouter(t, &fakeCapabilityService{}, nil, nil, nil)

	rec := doAdmin(t, handler, http.MethodDelete, "/admin/v1/models/1/capabilities/tools.function", "", true)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected %d, got %d (%s)", http.StatusNoContent, rec.Code, rec.Body.String())
	}
}

func TestSetChannelOverrideReturns200(t *testing.T) {
	handler := newCapabilityRouter(t, &fakeCapabilityService{}, nil, nil, nil)

	rec := doAdmin(t, handler, http.MethodPut, "/admin/v1/channels/5/capability-overrides/tools.builtin.web_search", `{"support_level":"unsupported","reason":"upstream lacks it"}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
}

func TestTriggerSyncReturns200(t *testing.T) {
	handler := newCapabilityRouter(t, nil, fakeCapabilitySyncService{}, nil, nil)

	rec := doAdmin(t, handler, http.MethodPost, "/admin/v1/capability/sync-jobs", `{"dry_run":true}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
}

func TestListSyncJobsReturns200(t *testing.T) {
	handler := newCapabilityRouter(t, nil, fakeCapabilitySyncService{}, nil, nil)

	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/capability/sync-jobs", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
}

func TestSyncJobsPreflightReturns204(t *testing.T) {
	handler := newCapabilityRouter(t, nil, fakeCapabilitySyncService{}, nil, nil)

	rec := doAdmin(t, handler, http.MethodOptions, "/admin/v1/capability/sync-jobs", "", false)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected %d, got %d (%s)", http.StatusNoContent, rec.Code, rec.Body.String())
	}
}

func TestListAdapterProfilesReturns200(t *testing.T) {
	handler := newCapabilityRouter(t, nil, nil, fakeCapabilitySeedService{}, nil)

	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/capability/adapter-profiles", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
}

func TestMaterializeAdapterSeedReturns200(t *testing.T) {
	handler := newCapabilityRouter(t, nil, nil, fakeCapabilitySeedService{}, nil)

	rec := doAdmin(t, handler, http.MethodPost, "/admin/v1/capability/adapter-seed-jobs", `{"model_id":1,"profile_key":"deepseek:openai"}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
}

func TestEnforcementReturns200(t *testing.T) {
	handler := newCapabilityRouter(t, nil, nil, nil, fakeCapabilityEnforcementService{})

	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/capability/enforcement", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
}

func TestObserveSummaryInvalidTimeReturns400(t *testing.T) {
	handler := newCapabilityRouter(t, nil, nil, nil, fakeCapabilityEnforcementService{})

	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/capability/observe-summary?from=notatime", "", true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d (%s)", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}
