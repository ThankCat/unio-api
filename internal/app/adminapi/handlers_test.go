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
	"github.com/ThankCat/unio-api/internal/service/admin/channelmodel"
	"github.com/ThankCat/unio-api/internal/service/admin/costprice"
	"github.com/ThankCat/unio-api/internal/service/admin/model"
	"github.com/ThankCat/unio-api/internal/service/admin/price"
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
	createOut         channel.Channel
	createErr         error
	rotateErr         error
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

func newChannelModelRouter(t *testing.T, cms adminapi.ChannelModelService) http.Handler {
	t.Helper()

	authenticator, err := adminauth.NewStaticTokenAuthenticator(testAdminToken)
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}

	return adminapi.NewRouter(adminapi.RouterDeps{
		Logger:              slog.New(slog.NewTextHandler(io.Discard, nil)),
		AdminAuthenticator:  authenticator,
		ChannelModelService: cms,
	})
}

type fakeCostPriceService struct {
	listOut   []costprice.CostPrice
	createOut costprice.CostPrice
	createErr error
	updateOut costprice.CostPrice
	updateErr error
}

func (s *fakeCostPriceService) List(context.Context, int64) ([]costprice.CostPrice, error) {
	return s.listOut, nil
}
func (s *fakeCostPriceService) Create(context.Context, costprice.CreateInput) (costprice.CostPrice, error) {
	return s.createOut, s.createErr
}
func (s *fakeCostPriceService) Update(context.Context, costprice.UpdateInput) (costprice.CostPrice, error) {
	return s.updateOut, s.updateErr
}

func newCostPriceRouter(t *testing.T, cps adminapi.CostPriceService) http.Handler {
	t.Helper()

	authenticator, err := adminauth.NewStaticTokenAuthenticator(testAdminToken)
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}

	return adminapi.NewRouter(adminapi.RouterDeps{
		Logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		AdminAuthenticator: authenticator,
		CostPriceService:   cps,
	})
}

type fakePriceService struct {
	listOut   []price.Price
	createOut price.Price
	createErr error
	updateOut price.Price
	updateErr error
}

func (s *fakePriceService) List(context.Context, int64) ([]price.Price, error) {
	return s.listOut, nil
}
func (s *fakePriceService) Create(context.Context, price.CreateInput) (price.Price, error) {
	return s.createOut, s.createErr
}
func (s *fakePriceService) Update(context.Context, price.UpdateInput) (price.Price, error) {
	return s.updateOut, s.updateErr
}

func newPriceRouter(t *testing.T, ps adminapi.PriceService) http.Handler {
	t.Helper()

	authenticator, err := adminauth.NewStaticTokenAuthenticator(testAdminToken)
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}

	return adminapi.NewRouter(adminapi.RouterDeps{
		Logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		AdminAuthenticator: authenticator,
		PriceService:       ps,
	})
}

func newModelRouter(t *testing.T, ms adminapi.ModelService) http.Handler {
	t.Helper()

	authenticator, err := adminauth.NewStaticTokenAuthenticator(testAdminToken)
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}

	return adminapi.NewRouter(adminapi.RouterDeps{
		Logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		AdminAuthenticator: authenticator,
		ModelService:       ms,
	})
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

func TestCreateCostPriceReturns201(t *testing.T) {
	handler := newCostPriceRouter(t, &fakeCostPriceService{createOut: costprice.CostPrice{ID: 1, ChannelID: 5, ModelID: 2, Currency: "USD", PricingUnit: "per_1m_tokens", UncachedInputCost: "1.25", OutputCost: "2.5", Status: "enabled"}})

	body := `{"model_id":2,"currency":"USD","pricing_unit":"per_1m_tokens","uncached_input_cost":"1.25","output_cost":"2.5","status":"enabled","effective_from":"2026-01-01T00:00:00Z"}`
	rec := doAdmin(t, handler, http.MethodPost, "/admin/v1/channels/5/cost-prices", body, true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected %d, got %d (%s)", http.StatusCreated, rec.Code, rec.Body.String())
	}
}

func TestCreateCostPriceBadEffectiveFromReturns400(t *testing.T) {
	handler := newCostPriceRouter(t, &fakeCostPriceService{})

	body := `{"model_id":2,"currency":"USD","pricing_unit":"per_1m_tokens","uncached_input_cost":"1.25","output_cost":"2.5","status":"enabled","effective_from":"not-a-time"}`
	rec := doAdmin(t, handler, http.MethodPost, "/admin/v1/channels/5/cost-prices", body, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d (%s)", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}

func TestCreateCostPriceOverlapReturns422(t *testing.T) {
	handler := newCostPriceRouter(t, &fakeCostPriceService{createErr: failure.New(failure.CodeAdminPricingWindowOverlap, failure.WithMessage("overlap"))})

	body := `{"model_id":2,"currency":"USD","pricing_unit":"per_1m_tokens","uncached_input_cost":"1.25","output_cost":"2.5","status":"enabled","effective_from":"2026-01-01T00:00:00Z"}`
	rec := doAdmin(t, handler, http.MethodPost, "/admin/v1/channels/5/cost-prices", body, true)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected %d, got %d (%s)", http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	}
}

func TestCostPricesRequireToken(t *testing.T) {
	handler := newCostPriceRouter(t, &fakeCostPriceService{})

	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/channels/5/cost-prices", "", false)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestCreatePriceReturns201(t *testing.T) {
	handler := newPriceRouter(t, &fakePriceService{createOut: price.Price{ID: 1, ModelID: 2, Currency: "USD", PricingUnit: "per_1m_tokens", UncachedInputPrice: "3", OutputPrice: "9", Status: "enabled"}})

	body := `{"currency":"USD","pricing_unit":"per_1m_tokens","uncached_input_price":"3","output_price":"9","status":"enabled","effective_from":"2026-01-01T00:00:00Z"}`
	rec := doAdmin(t, handler, http.MethodPost, "/admin/v1/models/2/prices", body, true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected %d, got %d (%s)", http.StatusCreated, rec.Code, rec.Body.String())
	}
}

func TestCreatePriceOverlapReturns422(t *testing.T) {
	handler := newPriceRouter(t, &fakePriceService{createErr: failure.New(failure.CodeAdminPricingWindowOverlap, failure.WithMessage("overlap"))})

	body := `{"currency":"USD","pricing_unit":"per_1m_tokens","uncached_input_price":"3","output_price":"9","status":"enabled","effective_from":"2026-01-01T00:00:00Z"}`
	rec := doAdmin(t, handler, http.MethodPost, "/admin/v1/models/2/prices", body, true)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected %d, got %d (%s)", http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	}
}

func TestUpdatePriceInvalidIDReturns400(t *testing.T) {
	handler := newPriceRouter(t, &fakePriceService{})

	rec := doAdmin(t, handler, http.MethodPatch, "/admin/v1/prices/abc", `{"status":"disabled"}`, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d (%s)", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}

func TestPricesRequireToken(t *testing.T) {
	handler := newPriceRouter(t, &fakePriceService{})

	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/models/2/prices", "", false)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}
