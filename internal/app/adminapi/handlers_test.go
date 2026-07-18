package adminapi_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi"
	achannel "github.com/ThankCat/unio-gateway/internal/app/adminapi/channel"
	amodel "github.com/ThankCat/unio-gateway/internal/app/adminapi/model"
	aprovider "github.com/ThankCat/unio-gateway/internal/app/adminapi/provider"
	aroute "github.com/ThankCat/unio-gateway/internal/app/adminapi/route"
	"github.com/ThankCat/unio-gateway/internal/core/adminauth"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/service/admin/channel"
	"github.com/ThankCat/unio-gateway/internal/service/admin/channelmodel"
	"github.com/ThankCat/unio-gateway/internal/service/admin/channelprice"
	"github.com/ThankCat/unio-gateway/internal/service/admin/model"
	"github.com/ThankCat/unio-gateway/internal/service/admin/provider"
	"github.com/ThankCat/unio-gateway/internal/service/admin/route"
)

type fakeProviderService struct {
	listOut   []provider.Provider
	getOut    provider.Provider
	getErr    error
	createOut provider.Provider
	createErr error
	updateOut provider.Provider
	updateErr error
	deleteErr error
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
func (s *fakeProviderService) Delete(context.Context, int64) error {
	return s.deleteErr
}
func (s *fakeProviderService) Archive(context.Context, int64) error { return nil }
func (s *fakeProviderService) Restore(context.Context, int64) error { return nil }

type fakeChannelService struct {
	createOut         channel.Channel
	createErr         error
	rotateErr         error
	deleteErr         error
	adapterKeyOptions []channel.AdapterKeyOption
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
func (s *fakeChannelService) Delete(context.Context, int64) error {
	return s.deleteErr
}
func (s *fakeChannelService) Archive(context.Context, int64) error { return nil }
func (s *fakeChannelService) Restore(context.Context, int64) error { return nil }
func (s *fakeChannelService) AdapterKeyOptions() []channel.AdapterKeyOption {
	return s.adapterKeyOptions
}

type fakeModelService struct {
	listOut   []model.Model
	getOut    model.Model
	getErr    error
	createOut model.Model
	createErr error
	updateOut model.Model
	updateErr error
	deleteErr error
}

func (s *fakeModelService) List(context.Context, model.ListParams) (model.ListResult, error) {
	return model.ListResult{Items: s.listOut, Total: int64(len(s.listOut))}, nil
}
func (s *fakeModelService) Get(context.Context, int64) (model.Model, error) {
	return s.getOut, s.getErr
}
func (s *fakeModelService) Create(context.Context, model.CreateInput) (model.Model, error) {
	return s.createOut, s.createErr
}
func (s *fakeModelService) Update(context.Context, model.UpdateInput) (model.Model, error) {
	return s.updateOut, s.updateErr
}
func (s *fakeModelService) Delete(context.Context, int64) error {
	return s.deleteErr
}

type fakeChannelModelService struct {
	listOut   []channelmodel.Binding
	createOut channelmodel.Binding
	createErr error
	updateOut channelmodel.Binding
	updateErr error
	deleteErr error
}

func (s *fakeChannelModelService) List(context.Context, int64) ([]channelmodel.Binding, error) {
	return s.listOut, nil
}
func (s *fakeChannelModelService) Create(context.Context, channelmodel.CreateInput) (channelmodel.Binding, error) {
	return s.createOut, s.createErr
}
func (s *fakeChannelModelService) Update(context.Context, channelmodel.UpdateInput) (channelmodel.Binding, error) {
	return s.updateOut, s.updateErr
}
func (s *fakeChannelModelService) Delete(context.Context, int64, int64) error {
	return s.deleteErr
}

func newChannelModelRouter(t *testing.T, cms achannel.ChannelModelService) http.Handler {
	t.Helper()

	authenticator, err := adminauth.NewStaticTokenAuthenticator(testAdminToken)
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}

	return adminapi.NewRouter(adminapi.RouterDeps{
		Logger:              zap.NewNop(),
		AdminAuthenticator:  authenticator,
		ChannelModelService: cms,
	})
}

type fakeChannelPriceService struct {
	listOut   []channelprice.ChannelPrice
	createOut channelprice.ChannelPrice
	createErr error
	updateOut channelprice.ChannelPrice
	updateErr error
}

func (s *fakeChannelPriceService) List(context.Context, int64) ([]channelprice.ChannelPrice, error) {
	return s.listOut, nil
}
func (s *fakeChannelPriceService) Create(context.Context, channelprice.CreateInput) (channelprice.ChannelPrice, error) {
	return s.createOut, s.createErr
}
func (s *fakeChannelPriceService) Update(context.Context, channelprice.UpdateInput) (channelprice.ChannelPrice, error) {
	return s.updateOut, s.updateErr
}

func newChannelPriceRouter(t *testing.T, cps achannel.ChannelPriceService) http.Handler {
	t.Helper()

	authenticator, err := adminauth.NewStaticTokenAuthenticator(testAdminToken)
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}

	return adminapi.NewRouter(adminapi.RouterDeps{
		Logger:              zap.NewNop(),
		AdminAuthenticator:  authenticator,
		ChannelPriceService: cps,
	})
}

type fakeRouteService struct {
	listOut   []route.Route
	createOut route.Route
	createErr error
}

func (s *fakeRouteService) List(context.Context) ([]route.Route, error) { return s.listOut, nil }
func (s *fakeRouteService) Get(context.Context, int64) (route.Route, error) {
	return s.createOut, nil
}
func (s *fakeRouteService) Create(context.Context, route.CreateInput) (route.Route, error) {
	return s.createOut, s.createErr
}
func (s *fakeRouteService) Update(context.Context, route.UpdateInput) (route.Route, error) {
	return s.createOut, nil
}
func (s *fakeRouteService) Delete(context.Context, int64) error { return nil }
func (s *fakeRouteService) Archive(context.Context, int64, *int64) ([]route.EmptyRouteWarning, error) {
	return nil, nil
}
func (s *fakeRouteService) Restore(context.Context, int64) error                     { return nil }
func (s *fakeRouteService) MigrateKeys(context.Context, int64, int64) (int64, error) { return 0, nil }
func (s *fakeRouteService) SetChannels(context.Context, int64, []int64) (route.Route, error) {
	return s.createOut, nil
}

func newRouteRouter(t *testing.T, rs aroute.RouteService) http.Handler {
	t.Helper()

	authenticator, err := adminauth.NewStaticTokenAuthenticator(testAdminToken)
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}

	return adminapi.NewRouter(adminapi.RouterDeps{
		Logger:             zap.NewNop(),
		AdminAuthenticator: authenticator,
		RouteService:       rs,
	})
}

func newModelRouter(t *testing.T, ms amodel.ModelService) http.Handler {
	t.Helper()

	authenticator, err := adminauth.NewStaticTokenAuthenticator(testAdminToken)
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}

	return adminapi.NewRouter(adminapi.RouterDeps{
		Logger:             zap.NewNop(),
		AdminAuthenticator: authenticator,
		ModelService:       ms,
	})
}

func newServicesRouter(t *testing.T, ps aprovider.ProviderService, cs achannel.ChannelService) http.Handler {
	t.Helper()

	authenticator, err := adminauth.NewStaticTokenAuthenticator(testAdminToken)
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}

	return adminapi.NewRouter(adminapi.RouterDeps{
		Logger:             zap.NewNop(),
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

func TestProvidersRequireToken(t *testing.T) {
	handler := newServicesRouter(t, &fakeProviderService{}, nil)

	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/providers", "", false)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestDeleteProviderReturns204(t *testing.T) {
	handler := newServicesRouter(t, &fakeProviderService{}, nil)

	rec := doAdmin(t, handler, http.MethodDelete, "/admin/v1/providers/9", "", true)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected %d, got %d (%s)", http.StatusNoContent, rec.Code, rec.Body.String())
	}
}

// 仍有渠道/被引用时 service 回 conflict，HTTP 层映射为 409，引导改用停用。
func TestDeleteProviderConflictReturns409(t *testing.T) {
	handler := newServicesRouter(t, &fakeProviderService{
		deleteErr: failure.New(failure.CodeAdminConflict, failure.WithMessage("still referenced")),
	}, nil)

	rec := doAdmin(t, handler, http.MethodDelete, "/admin/v1/providers/9", "", true)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected %d, got %d (%s)", http.StatusConflict, rec.Code, rec.Body.String())
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

func TestListAdapterKeysReturnsOptions(t *testing.T) {
	handler := newServicesRouter(t, nil, &fakeChannelService{adapterKeyOptions: []channel.AdapterKeyOption{
		{Protocol: "openai", AdapterKey: "openai", IsDefault: true},
		{Protocol: "openai", AdapterKey: "deepseek"},
	}})

	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/channels/adapter-keys", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, `"adapter_key":"openai"`) ||
		!strings.Contains(body, `"is_default":true`) {
		t.Fatalf("unexpected adapter-keys body: %s", body)
	}
}

func TestRotateChannelCredentialReturns204(t *testing.T) {
	handler := newServicesRouter(t, nil, &fakeChannelService{})

	rec := doAdmin(t, handler, http.MethodPut, "/admin/v1/channels/5/credential", `{"credential":"sk-new"}`, true)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected %d, got %d (%s)", http.StatusNoContent, rec.Code, rec.Body.String())
	}
}

func TestCreateModelReturns201(t *testing.T) {
	handler := newModelRouter(t, &fakeModelService{createOut: model.Model{ID: 1, ModelID: "deepseek-chat", DisplayName: "DeepSeek Chat", OwnedBy: "deepseek", Status: "enabled", Source: "manual"}})

	rec := doAdmin(t, handler, http.MethodPost, "/admin/v1/models", `{"model_id":"deepseek-chat","display_name":"DeepSeek Chat","owned_by":"deepseek","status":"enabled"}`, true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected %d, got %d (%s)", http.StatusCreated, rec.Code, rec.Body.String())
	}
}

func TestGetModelInvalidIDReturns400(t *testing.T) {
	handler := newModelRouter(t, &fakeModelService{})

	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/models/abc", "", true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d", http.StatusBadRequest, rec.Code)
	}
}

func TestGetModelNotFoundReturns404(t *testing.T) {
	handler := newModelRouter(t, &fakeModelService{getErr: failure.New(failure.CodeAdminNotFound, failure.WithMessage("model not found"))})

	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/models/9", "", true)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected %d, got %d", http.StatusNotFound, rec.Code)
	}
}

func TestModelsRequireToken(t *testing.T) {
	handler := newModelRouter(t, &fakeModelService{})

	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/models", "", false)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestDeleteModelReturns204(t *testing.T) {
	handler := newModelRouter(t, &fakeModelService{})

	rec := doAdmin(t, handler, http.MethodDelete, "/admin/v1/models/9", "", true)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected %d, got %d (%s)", http.StatusNoContent, rec.Code, rec.Body.String())
	}
}

func TestDeleteModelConflictReturns409(t *testing.T) {
	handler := newModelRouter(t, &fakeModelService{
		deleteErr: failure.New(failure.CodeAdminConflict, failure.WithMessage("referenced by billing history")),
	})

	rec := doAdmin(t, handler, http.MethodDelete, "/admin/v1/models/9", "", true)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected %d, got %d (%s)", http.StatusConflict, rec.Code, rec.Body.String())
	}
}

func TestCreateChannelModelReturns201(t *testing.T) {
	handler := newChannelModelRouter(t, &fakeChannelModelService{createOut: channelmodel.Binding{ID: 1, ChannelID: 5, ModelID: 2, UpstreamModel: "gpt-4o", Status: "enabled"}})

	rec := doAdmin(t, handler, http.MethodPost, "/admin/v1/channels/5/models", `{"model_id":2,"upstream_model":"gpt-4o","status":"enabled"}`, true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected %d, got %d (%s)", http.StatusCreated, rec.Code, rec.Body.String())
	}
}

func TestDeleteChannelModelConflictReturns409(t *testing.T) {
	handler := newChannelModelRouter(t, &fakeChannelModelService{deleteErr: failure.New(failure.CodeAdminConflict, failure.WithMessage("referenced by billing history"))})

	rec := doAdmin(t, handler, http.MethodDelete, "/admin/v1/channels/5/models/2", "", true)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected %d, got %d (%s)", http.StatusConflict, rec.Code, rec.Body.String())
	}
}

func TestUpdateChannelModelInvalidModelIDReturns400(t *testing.T) {
	handler := newChannelModelRouter(t, &fakeChannelModelService{})

	rec := doAdmin(t, handler, http.MethodPatch, "/admin/v1/channels/5/models/abc", `{"upstream_model":"gpt-4o","status":"enabled"}`, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d (%s)", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}

func TestChannelModelsRequireToken(t *testing.T) {
	handler := newChannelModelRouter(t, &fakeChannelModelService{})

	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/channels/5/models", "", false)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestCreateChannelPriceReturns201(t *testing.T) {
	handler := newChannelPriceRouter(t, &fakeChannelPriceService{createOut: channelprice.ChannelPrice{ID: 1, ChannelID: 5, ModelID: 2, Currency: "USD", PricingUnit: "per_1m_tokens", UncachedInputCost: "1.25", OutputCost: "2.5", Status: "enabled"}})

	body := `{"currency":"USD","pricing_unit":"per_1m_tokens","uncached_input_cost":"1.25","output_cost":"2.5","status":"enabled","effective_from":"2026-01-01T00:00:00Z"}`
	rec := doAdmin(t, handler, http.MethodPost, "/admin/v1/channels/5/models/2/prices", body, true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected %d, got %d (%s)", http.StatusCreated, rec.Code, rec.Body.String())
	}
}

func TestCreateChannelPriceBadEffectiveFromReturns400(t *testing.T) {
	handler := newChannelPriceRouter(t, &fakeChannelPriceService{})

	body := `{"currency":"USD","pricing_unit":"per_1m_tokens","uncached_input_price":"3","output_price":"9","status":"enabled","effective_from":"not-a-time"}`
	rec := doAdmin(t, handler, http.MethodPost, "/admin/v1/channels/5/models/2/prices", body, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d (%s)", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}

func TestCreateChannelPriceOverlapReturns422(t *testing.T) {
	handler := newChannelPriceRouter(t, &fakeChannelPriceService{createErr: failure.New(failure.CodeAdminPricingWindowOverlap, failure.WithMessage("overlap"))})

	body := `{"currency":"USD","pricing_unit":"per_1m_tokens","uncached_input_price":"3","output_price":"9","status":"enabled","effective_from":"2026-01-01T00:00:00Z"}`
	rec := doAdmin(t, handler, http.MethodPost, "/admin/v1/channels/5/models/2/prices", body, true)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected %d, got %d (%s)", http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	}
}

func TestUpdateChannelPriceInvalidIDReturns400(t *testing.T) {
	handler := newChannelPriceRouter(t, &fakeChannelPriceService{})

	rec := doAdmin(t, handler, http.MethodPatch, "/admin/v1/channel-prices/abc", `{"status":"disabled"}`, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d (%s)", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}

func TestChannelPricesRequireToken(t *testing.T) {
	handler := newChannelPriceRouter(t, &fakeChannelPriceService{})

	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/channels/5/prices", "", false)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestCreateRouteReturns201(t *testing.T) {
	handler := newRouteRouter(t, &fakeRouteService{createOut: route.Route{ID: 3, Name: "C-line", Mode: "fixed", PoolKind: "explicit", Status: "enabled"}})

	body := `{"name":"C-line","mode":"fixed","pool_kind":"explicit","status":"enabled","channel_ids":[5]}`
	rec := doAdmin(t, handler, http.MethodPost, "/admin/v1/routes", body, true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected %d, got %d (%s)", http.StatusCreated, rec.Code, rec.Body.String())
	}
}

func TestCreateRouteFixedValidationReturns400(t *testing.T) {
	handler := newRouteRouter(t, &fakeRouteService{createErr: failure.New(failure.CodeAdminInvalidArgument, failure.WithMessage("fixed route must list exactly one channel"), failure.WithField("field", "channel_ids"))})

	body := `{"name":"C-line","mode":"fixed","pool_kind":"explicit","status":"enabled","channel_ids":[]}`
	rec := doAdmin(t, handler, http.MethodPost, "/admin/v1/routes", body, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d (%s)", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}

func TestRoutesRequireToken(t *testing.T) {
	handler := newRouteRouter(t, &fakeRouteService{})

	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/routes", "", false)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}
