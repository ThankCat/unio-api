package system

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	messagesadapter "github.com/ThankCat/unio-gateway/internal/core/adapter/anthropic/messages"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/service/appsettings"
)

type fakeProviderSettingsService struct {
	result appsettings.SettingWriteResult
	err    error
}

func (f *fakeProviderSettingsService) List(context.Context) []appsettings.SettingItem { return nil }

func (f *fakeProviderSettingsService) SetRawWithResult(context.Context, string, json.RawMessage) (appsettings.SettingWriteResult, error) {
	return f.result, f.err
}

func (f *fakeProviderSettingsService) GetAnthropicBetaPolicy(context.Context) messagesadapter.BetaPolicy {
	return messagesadapter.DefaultBetaPolicy()
}

func (f *fakeProviderSettingsService) SetAnthropicBetaPolicy(context.Context, messagesadapter.BetaPolicy) error {
	return nil
}

func TestPutSettingReturnsRuntimeActivationState(t *testing.T) {
	service := &fakeProviderSettingsService{result: appsettings.SettingWriteResult{
		Key: appsettings.GatewayRouteRateLimitDefaultsKey, Revision: 4, State: "runtime_sync_pending",
		ActiveRevision: 3, PendingRevision: 4,
	}}
	router := chi.NewRouter()
	router.Put("/settings/{key}", (&providerSettingsHandler{service: service}).putSetting)

	req := httptest.NewRequest(http.MethodPut, "/settings/"+appsettings.GatewayRouteRateLimitDefaultsKey, strings.NewReader(`{"rpm":60,"tpm":0,"rpd":0}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Data appsettings.SettingWriteResult `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Data.State != "runtime_sync_pending" || body.Data.Revision != 4 || body.Data.PendingRevision != 4 {
		t.Fatalf("unexpected response: %+v", body.Data)
	}
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw response: %v", err)
	}
	data, ok := raw["data"].(map[string]any)
	if !ok || data["active_revision"] != float64(3) || data["pending_revision"] != float64(4) {
		t.Fatalf("runtime revision fields must use snake_case: %s", rec.Body.String())
	}
	if _, legacy := data["ActiveRevision"]; legacy {
		t.Fatalf("legacy PascalCase field leaked: %s", rec.Body.String())
	}
}

func TestPutSettingPreservesBreakerStoreUnavailableAs503(t *testing.T) {
	service := &fakeProviderSettingsService{err: failure.Wrap(
		failure.CodeGatewayBreakerStoreUnavailable,
		errors.New("redis unavailable"),
	)}
	router := chi.NewRouter()
	router.Put("/settings/{key}", (&providerSettingsHandler{service: service}).putSetting)

	req := httptest.NewRequest(http.MethodPut, "/settings/gateway.circuit_breaker", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", rec.Code, rec.Body.String())
	}
}
