package adminapi_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ThankCat/unio-api/internal/app/adminapi"
	"github.com/ThankCat/unio-api/internal/core/adminauth"
	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/service/admin/channel"
	"github.com/ThankCat/unio-api/internal/service/admin/provider"
)

type fakeProviderService struct {
	listOut   []provider.Provider
	getOut    provider.Provider
	getErr    error
	createOut provider.Provider
	createErr error
	updateOut provider.Provider
	updateErr error
}

func (s *fakeProviderService) List(context.Context, provider.ListParams) (provider.ListResult, error) {
	return provider.ListResult{Items: s.listOut, Total: int64(len(s.listOut))}, nil
}
func (s *fakeProviderService) Get(context.Context, int64) (provider.Provider, error) {
	return s.getOut, s.getErr
}
func (s *fakeProviderService) Create(context.Context, provider.CreateInput) (provider.Provider, error) {
	return s.createOut, s.createErr
}
func (s *fakeProviderService) Update(context.Context, provider.UpdateInput) (provider.Provider, error) {
	return s.updateOut, s.updateErr
}

type fakeChannelService struct {
	createOut channel.Channel
	createErr error
	rotateErr error
}

func (s *fakeChannelService) List(context.Context, channel.ListParams) (channel.ListResult, error) {
	return channel.ListResult{}, nil
}
func (s *fakeChannelService) Get(context.Context, int64) (channel.Channel, error) {
	return channel.Channel{}, nil
}
func (s *fakeChannelService) Create(context.Context, channel.CreateInput) (channel.Channel, error) {
	return s.createOut, s.createErr
}
func (s *fakeChannelService) Update(context.Context, channel.UpdateInput) (channel.Channel, error) {
	return channel.Channel{}, nil
}
func (s *fakeChannelService) RotateCredential(context.Context, channel.RotateCredentialInput) error {
	return s.rotateErr
}

func newServicesRouter(t *testing.T, ps adminapi.ProviderService, cs adminapi.ChannelService) http.Handler {
	t.Helper()

	authenticator, err := adminauth.NewStaticTokenAuthenticator(testAdminToken)
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}

	return adminapi.NewRouter(adminapi.RouterDeps{
		Logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		AdminAuthenticator: authenticator,
		ProviderService:    ps,
		ChannelService:     cs,
	})
}

func doAdmin(t *testing.T, handler http.Handler, method, path, body string, withToken bool) *httptest.ResponseRecorder {
	t.Helper()

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if withToken {
		req.Header.Set("Authorization", "Bearer "+testAdminToken)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestCreateProviderReturns201(t *testing.T) {
	handler := newServicesRouter(t, &fakeProviderService{createOut: provider.Provider{ID: 1, Slug: "openai", Name: "OpenAI", Status: "enabled"}}, nil)

	rec := doAdmin(t, handler, http.MethodPost, "/admin/v1/providers", `{"slug":"openai","name":"OpenAI","status":"enabled"}`, true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected %d, got %d (%s)", http.StatusCreated, rec.Code, rec.Body.String())
	}
}

func TestGetProviderInvalidIDReturns400(t *testing.T) {
	handler := newServicesRouter(t, &fakeProviderService{}, nil)

	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/providers/abc", "", true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d", http.StatusBadRequest, rec.Code)
	}
}

func TestGetProviderNotFoundReturns404(t *testing.T) {
	handler := newServicesRouter(t, &fakeProviderService{getErr: failure.New(failure.CodeAdminNotFound, failure.WithMessage("provider not found"))}, nil)

	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/providers/9", "", true)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected %d, got %d", http.StatusNotFound, rec.Code)
	}
}

func TestProvidersRequireToken(t *testing.T) {
	handler := newServicesRouter(t, &fakeProviderService{}, nil)

	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/providers", "", false)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestCreateChannelUnsupportedBindingReturns422(t *testing.T) {
	handler := newServicesRouter(t, nil, &fakeChannelService{createErr: failure.New(failure.CodeAdminAdapterBindingUnsupported, failure.WithMessage("unsupported"))})

	body := `{"provider_id":1,"name":"primary","protocol":"openai","adapter_key":"x","base_url":"https://a.test/v1","credential":"sk","status":"enabled","priority":0}`
	rec := doAdmin(t, handler, http.MethodPost, "/admin/v1/channels", body, true)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected %d, got %d (%s)", http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	}
}

func TestRotateChannelCredentialReturns204(t *testing.T) {
	handler := newServicesRouter(t, nil, &fakeChannelService{})

	rec := doAdmin(t, handler, http.MethodPut, "/admin/v1/channels/5/credential", `{"credential":"sk-new"}`, true)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected %d, got %d (%s)", http.StatusNoContent, rec.Code, rec.Body.String())
	}
}
