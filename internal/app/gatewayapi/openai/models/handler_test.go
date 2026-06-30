package models

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ThankCat/unio-api/internal/core/auth"
	"github.com/ThankCat/unio-api/internal/core/modelcatalog"
	"github.com/ThankCat/unio-api/internal/platform/ratelimit"
)

// modelsTestAPIKeyAuthenticator 是 models 测试使用的 API Key 认证器。
type modelsTestAPIKeyAuthenticator struct {
	principal *auth.APIKeyPrincipal
	err       error
	token     string
}

// AuthenticateAPIKey 记录收到的 token，并返回测试预设的认证结果。
func (a *modelsTestAPIKeyAuthenticator) AuthenticateAPIKey(ctx context.Context, plaintext string) (*auth.APIKeyPrincipal, error) {
	a.token = plaintext
	return a.principal, a.err
}

func TestRouterModelsRequiresAPIKey(t *testing.T) {
	authenticator := &modelsTestAPIKeyAuthenticator{
		principal: &auth.APIKeyPrincipal{
			APIKeyID:  1,
			UserID:    42,
			KeyPrefix: "unio_sk_test",
		},
	}
	modelCatalogService := &routerTestModelCatalogService{
		models: []modelcatalog.Model{
			{
				ID:      "openai/gpt-4.1",
				OwnedBy: "openai",
			},
		},
	}
	handler := newTestRouter(authenticator, nil, nil, modelCatalogService)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}

	if authenticator.token != "" {
		t.Fatalf("expected authenticator not to receive token, got %q", authenticator.token)
	}
}

func TestRouterModelsSuccess(t *testing.T) {
	authenticator := &modelsTestAPIKeyAuthenticator{
		principal: &auth.APIKeyPrincipal{
			APIKeyID:  1,
			UserID:    42,
			KeyPrefix: "unio_sk_test",
		},
	}
	modelCatalogService := &routerTestModelCatalogService{
		models: []modelcatalog.Model{
			{
				ID:           "openai/gpt-4.1",
				OwnedBy:      "openai",
				Capabilities: []string{"text.input", "text.output", "tools.function"},
			},
		},
	}
	handler := newTestRouter(authenticator, nil, nil, modelCatalogService)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer unio_sk_test")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	if authenticator.token != "unio_sk_test" {
		t.Fatalf("expected token %q, got %q", "unio_sk_test", authenticator.token)
	}

	var body struct {
		Object string `json:"object"`
		Data   []struct {
			ID           string   `json:"id"`
			Object       string   `json:"object"`
			OwnedBy      string   `json:"owned_by"`
			Capabilities []string `json:"capabilities"`
		} `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response body: %v", err)
	}

	if body.Object != "list" {
		t.Fatalf("expected object %q, got %q", "list", body.Object)
	}

	if !modelCatalogService.called {
		t.Fatal("expected model catalog service to be called")
	}

	if modelCatalogService.projectID != 42 {
		t.Fatalf("expected project id %d, got %d", int64(42), modelCatalogService.projectID)
	}

	if modelCatalogService.requiredCapabilities != nil {
		t.Fatalf("expected no capability filter, got %v", modelCatalogService.requiredCapabilities)
	}

	if len(body.Data) != 1 {
		t.Fatalf("expected 1 model, got %d items", len(body.Data))
	}

	if body.Data[0].ID != "openai/gpt-4.1" {
		t.Fatalf("expected model id %q, got %q", "openai/gpt-4.1", body.Data[0].ID)
	}

	if body.Data[0].Object != "model" {
		t.Fatalf("expected model object %q, got %q", "model", body.Data[0].Object)
	}

	if body.Data[0].OwnedBy != "openai" {
		t.Fatalf("expected owned_by %q, got %q", "openai", body.Data[0].OwnedBy)
	}

	wantCaps := []string{"text.input", "text.output", "tools.function"}
	if len(body.Data[0].Capabilities) != len(wantCaps) {
		t.Fatalf("expected capabilities %v, got %v", wantCaps, body.Data[0].Capabilities)
	}
	for i, want := range wantCaps {
		if body.Data[0].Capabilities[i] != want {
			t.Fatalf("capability[%d] = %q, want %q", i, body.Data[0].Capabilities[i], want)
		}
	}
}

func TestRouterModelsCapabilityFilterParsed(t *testing.T) {
	authenticator := &modelsTestAPIKeyAuthenticator{
		principal: &auth.APIKeyPrincipal{
			APIKeyID:  1,
			UserID:    42,
			KeyPrefix: "unio_sk_test",
		},
	}
	modelCatalogService := &routerTestModelCatalogService{}
	handler := newTestRouter(authenticator, nil, nil, modelCatalogService)

	req := httptest.NewRequest(http.MethodGet, "/v1/models?capability=image.input,%20tools.function%20,", nil)
	req.Header.Set("Authorization", "Bearer unio_sk_test")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	want := []string{"image.input", "tools.function"}
	if len(modelCatalogService.requiredCapabilities) != len(want) {
		t.Fatalf("expected capability filter %v, got %v", want, modelCatalogService.requiredCapabilities)
	}
	for i, w := range want {
		if modelCatalogService.requiredCapabilities[i] != w {
			t.Fatalf("filter[%d] = %q, want %q", i, modelCatalogService.requiredCapabilities[i], w)
		}
	}
}

func TestRouterModelsServiceError(t *testing.T) {
	authenticator := &modelsTestAPIKeyAuthenticator{
		principal: &auth.APIKeyPrincipal{
			APIKeyID:  1,
			UserID:    1,
			KeyPrefix: "unio_sk_test",
		},
	}
	handler := newTestRouter(authenticator, nil, nil, &routerTestModelCatalogService{
		err: errors.New("database unavailable"),
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer unio_sk_test")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected status %d, got %d", http.StatusInternalServerError, rec.Code)
	}

	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response body: %v", err)
	}

	if body.Error.Code != "internal_error" {
		t.Fatalf("expected error code %q, got %q", "internal_error", body.Error.Code)
	}
}

func TestRouterModelsUsesRateLimit(t *testing.T) {
	authenticator := &modelsTestAPIKeyAuthenticator{
		principal: &auth.APIKeyPrincipal{
			APIKeyID:  1,
			UserID:    1,
			KeyPrefix: "unio_sk_test",
		},
	}
	limiter := &routerTestRateLimiter{
		decision: ratelimit.Decision{
			Allowed:   true,
			Limit:     60,
			Remaining: 59,
			ResetAt:   time.Date(2026, 5, 8, 10, 1, 0, 0, time.UTC),
		},
	}
	handler := newTestRouter(authenticator, nil, limiter)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer unio_sk_test")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	if limiter.apiKeyID != 1 {
		t.Fatalf("expected rate limit api key id %d, got %d", 1, limiter.apiKeyID)
	}
}

func TestRouterModelsRateLimited(t *testing.T) {
	authenticator := &modelsTestAPIKeyAuthenticator{
		principal: &auth.APIKeyPrincipal{
			APIKeyID:  1,
			UserID:    1,
			KeyPrefix: "unio_sk_test",
		},
	}
	limiter := &routerTestRateLimiter{
		decision: ratelimit.Decision{
			Allowed:   false,
			Limit:     60,
			Remaining: 0,
			ResetAt:   time.Date(2026, 5, 8, 10, 1, 0, 0, time.UTC),
		},
	}
	handler := newTestRouter(authenticator, nil, limiter)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer unio_sk_test")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected status %d, got %d", http.StatusTooManyRequests, rec.Code)
	}

	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response body: %v", err)
	}

	if body.Error.Code != "rate_limited" {
		t.Fatalf("expected error code %q, got %q", "rate_limited", body.Error.Code)
	}
}
