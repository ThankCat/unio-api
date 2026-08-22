package requests_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	consoleauth "github.com/ThankCat/unio-gateway/internal/app/consoleapi/auth"
	consolerequestshttp "github.com/ThankCat/unio-gateway/internal/app/consoleapi/requests"
	"github.com/ThankCat/unio-gateway/internal/app/consoleapi/transport"
	consoleservice "github.com/ThankCat/unio-gateway/internal/service/console"
	serviceauth "github.com/ThankCat/unio-gateway/internal/service/console/auth"
	consolerequests "github.com/ThankCat/unio-gateway/internal/service/console/requests"
)

const handlerTestUID = "0198c9d7-0af1-7c42-a063-91d2922af371"

type fakeAuthService struct {
	accessToken string
}

func (s *fakeAuthService) CheckEmail(context.Context, string) *consoleservice.Error { return nil }
func (s *fakeAuthService) CheckRegistrationEmail(context.Context, string) *consoleservice.Error {
	return nil
}
func (s *fakeAuthService) SendChallenge(context.Context, string, string, string) (serviceauth.Challenge, *consoleservice.Error) {
	return serviceauth.Challenge{}, nil
}
func (s *fakeAuthService) Register(context.Context, string, string, string, string, string) (serviceauth.User, serviceauth.TokenPair, *consoleservice.Error) {
	return serviceauth.User{}, serviceauth.TokenPair{}, nil
}
func (s *fakeAuthService) PasswordLogin(context.Context, string, string, string) (serviceauth.User, serviceauth.TokenPair, *consoleservice.Error) {
	return serviceauth.User{}, serviceauth.TokenPair{}, nil
}
func (s *fakeAuthService) EmailCodeLogin(context.Context, string, string, string, string) (serviceauth.User, serviceauth.TokenPair, *consoleservice.Error) {
	return serviceauth.User{}, serviceauth.TokenPair{}, nil
}
func (s *fakeAuthService) CurrentUser(context.Context, string) (serviceauth.User, *consoleservice.Error) {
	return serviceauth.User{}, nil
}
func (s *fakeAuthService) AuthenticatePrincipal(_ context.Context, accessToken string) (serviceauth.Principal, *consoleservice.Error) {
	s.accessToken = accessToken
	return serviceauth.Principal{UserID: 42, UID: handlerTestUID}, nil
}
func (s *fakeAuthService) VerifyPasswordResetCode(context.Context, string, string, string, string) (serviceauth.PasswordResetGrant, *consoleservice.Error) {
	return serviceauth.PasswordResetGrant{}, nil
}
func (s *fakeAuthService) ResetPassword(context.Context, string, string) *consoleservice.Error {
	return nil
}
func (s *fakeAuthService) Refresh(context.Context, string) (serviceauth.TokenPair, *consoleservice.Error) {
	return serviceauth.TokenPair{}, nil
}
func (s *fakeAuthService) Logout(context.Context, string) *consoleservice.Error    { return nil }
func (s *fakeAuthService) LogoutAll(context.Context, string) *consoleservice.Error { return nil }

type fakeRequestService struct {
	listParams    consolerequests.ListParams
	summaryParams consolerequests.SummaryParams
	filtersID     int64
}

func (s *fakeRequestService) List(_ context.Context, params consolerequests.ListParams) ([]consolerequests.Item, int64, *consoleservice.Error) {
	s.listParams = params
	routeID := int64(3)
	reasoning := "medium"
	latency := int64(1500)
	firstToken := int64(400)
	tps := 18.1818
	return []consolerequests.Item{{
		ID:                      11,
		RequestID:               "req_safe",
		CreatedAt:               time.Date(2026, 8, 20, 8, 0, 0, 0, time.UTC),
		ClientIP:                "203.0.113.10",
		RouteID:                 &routeID,
		RouteName:               "Claude",
		APIKeyID:                9,
		APIKeyName:              "prod",
		APIKeyPrefix:            "unio_sk_XhE8wL5D",
		APIKeyPlaintext:         strPtr("unio_sk_XhE8wL5Dabcdefghijklmnopqrstuvwxyz012345"),
		Endpoint:                "/chat/completions",
		Stream:                  true,
		RequestedModelID:        "claude-sonnet-4-5",
		ModelDisplayName:        "Claude Sonnet 4.5",
		IngressProtocol:         "anthropic",
		InputPricePer1M:         strPtr("3"),
		OutputPricePer1M:        strPtr("15"),
		ReasoningEffort:         &reasoning,
		UncachedInputTokens:     80,
		CacheReadInputTokens:    10,
		CacheWrite5mInputTokens: 10,
		InputTokens:             100,
		OutputTokens:            20,
		ReasoningOutputTokens:   5,
		LatencyMs:               &latency,
		FirstTokenMs:            &firstToken,
		TPS:                     &tps,
		UserChargeUSD:           "0.15",
	}}, 1, nil
}

func (s *fakeRequestService) Summary(_ context.Context, params consolerequests.SummaryParams) (consolerequests.Summary, *consoleservice.Error) {
	s.summaryParams = params
	return consolerequests.Summary{
		RequestCount:            4,
		StreamCount:             3,
		TokenCount:              180,
		InputTokenCount:         120,
		OutputTokenCount:        60,
		UncachedInputTokenCount: 90,
		CacheReadTokenCount:     20,
		CacheWriteTokenCount:    10,
		ChargeUSD:               "1.25",
		UncachedInputChargeUSD:  "0.9",
		OutputChargeUSD:         "0.24",
		CacheReadChargeUSD:      "0.04",
		CacheWriteChargeUSD:     "0.07",
		ListChargeUSD:           "2.5",
		AverageLatencyMs:        750,
		AverageFirstTokenMs:     400,
		MedianLatencyMs:         620,
		AverageTPS:              18.1818,
		TopModels: []consolerequests.SummaryModel{
			{ModelID: "gpt-5.2", DisplayName: "GPT-5.2", RequestCount: 3},
			{ModelID: "gpt-4.1", DisplayName: "GPT-4.1", RequestCount: 1},
		},
	}, nil
}

func (s *fakeRequestService) Filters(_ context.Context, userID int64) (consolerequests.Filters, *consoleservice.Error) {
	s.filtersID = userID
	return consolerequests.Filters{
		Routes:      []consolerequests.FilterOption{{ID: 3, Name: "Claude"}},
		APIKeys:     []consolerequests.FilterOption{{ID: 9, Name: "prod"}},
		Endpoints:   []string{"/chat/completions"},
		StreamTypes: []string{"stream"},
	}, nil
}

func newRequestHandler(t *testing.T, auth *fakeAuthService, service *fakeRequestService) http.Handler {
	t.Helper()
	errorWriter := transport.NewErrorWriter(zap.NewNop())
	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(consoleauth.RequireAuth(auth, errorWriter))
			consolerequestshttp.Register(r, consolerequestshttp.Deps{
				Service:     service,
				ErrorWriter: errorWriter,
			})
		})
	})
	return r
}

func TestRequestHandlersRequireAccessCookie(t *testing.T) {
	handler := newRequestHandler(t, &fakeAuthService{}, &fakeRequestService{})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/requests/summary", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestRequestSummaryUsesAuthenticatedUserID(t *testing.T) {
	auth := &fakeAuthService{}
	service := &fakeRequestService{}
	handler := newRequestHandler(t, auth, service)
	req := httptest.NewRequest(http.MethodGet, "/v1/requests/summary", nil)
	req.AddCookie(&http.Cookie{Name: "unio_access_token", Value: "access-token"})
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if auth.accessToken != "access-token" || service.summaryParams.UserID != 42 {
		t.Fatalf("token=%q user=%d", auth.accessToken, service.summaryParams.UserID)
	}
	if service.summaryParams.From != nil || service.summaryParams.To != nil {
		t.Fatalf("all-time summary should omit time window: %+v", service.summaryParams)
	}
	var payload struct {
		Data struct {
			RequestCount            int64   `json:"request_count"`
			StreamCount             int64   `json:"stream_count"`
			TokenCount              int64   `json:"token_count"`
			InputTokenCount         int64   `json:"input_token_count"`
			OutputTokenCount        int64   `json:"output_token_count"`
			UncachedInputTokenCount int64   `json:"uncached_input_token_count"`
			CacheReadTokenCount     int64   `json:"cache_read_token_count"`
			CacheWriteTokenCount    int64   `json:"cache_write_token_count"`
			ChargeUSD               string  `json:"charge_usd"`
			ListChargeUSD           string  `json:"list_charge_usd"`
			AverageLatencyMs        float64 `json:"average_latency_ms"`
			TopModels               []struct {
				ModelID      string `json:"model_id"`
				DisplayName  string `json:"display_name"`
				RequestCount int64  `json:"request_count"`
			} `json:"top_models"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.RequestCount != 4 || payload.Data.StreamCount != 3 || payload.Data.ChargeUSD != "1.25" || payload.Data.ListChargeUSD != "2.5" || payload.Data.InputTokenCount != 120 || payload.Data.OutputTokenCount != 60 || payload.Data.UncachedInputTokenCount != 90 || payload.Data.CacheReadTokenCount != 20 || payload.Data.CacheWriteTokenCount != 10 {
		t.Fatalf("payload = %+v", payload.Data)
	}
	if len(payload.Data.TopModels) != 2 || payload.Data.TopModels[0].ModelID != "gpt-5.2" || payload.Data.TopModels[0].RequestCount != 3 {
		t.Fatalf("top models = %+v", payload.Data.TopModels)
	}
}

func TestRequestSummaryForwardsSearchQuery(t *testing.T) {
	service := &fakeRequestService{}
	handler := newRequestHandler(t, &fakeAuthService{}, service)
	req := httptest.NewRequest(http.MethodGet, "/v1/requests/summary?q=gpt-5.6-terra", nil)
	req.AddCookie(&http.Cookie{Name: "unio_access_token", Value: "access-token"})
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if service.summaryParams.Q != "gpt-5.6-terra" {
		t.Fatalf("search not forwarded: %+v", service.summaryParams)
	}
}

func TestRequestSummaryForwardsOptionalTimeWindow(t *testing.T) {
	service := &fakeRequestService{}
	handler := newRequestHandler(t, &fakeAuthService{}, service)
	req := httptest.NewRequest(http.MethodGet, "/v1/requests/summary?from=2026-08-19T16:00:00Z&to=2026-08-20T16:00:00Z", nil)
	req.AddCookie(&http.Cookie{Name: "unio_access_token", Value: "access-token"})
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if service.summaryParams.From == nil || service.summaryParams.To == nil {
		t.Fatalf("optional time window not forwarded: %+v", service.summaryParams)
	}
}

func TestRequestListOmitsInternalFieldsAndScopesToUser(t *testing.T) {
	service := &fakeRequestService{}
	handler := newRequestHandler(t, &fakeAuthService{}, service)
	req := httptest.NewRequest(
		http.MethodGet,
		"/v1/requests?page=1&page_size=20&route_id=3&endpoint=/chat/completions&sort=-created_at",
		nil,
	)
	req.AddCookie(&http.Cookie{Name: "unio_access_token", Value: "access-token"})
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if service.listParams.UserID != 42 {
		t.Fatalf("user id = %d", service.listParams.UserID)
	}
	body := rec.Body.String()
	for _, leaked := range []string{
		"total_cost",
		"channel_chain",
		"error_code",
		"internal_error",
		`"status"`,
	} {
		if strings.Contains(body, leaked) {
			t.Fatalf("response leaked %s: %s", leaked, body)
		}
	}
	var payload struct {
		Data struct {
			Items []map[string]any `json:"items"`
			Page  int              `json:"page"`
			Total int64            `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.Page != 1 || payload.Data.Total != 1 || len(payload.Data.Items) != 1 {
		t.Fatalf("list = %+v", payload.Data)
	}
	item := payload.Data.Items[0]
	if _, ok := item["status"]; ok {
		t.Fatalf("status should be omitted: %#v", item)
	}
	if item["endpoint"] != "/chat/completions" || item["api_key_name"] != "prod" {
		t.Fatalf("item = %#v", item)
	}
	if item["api_key_prefix"] != "unio_sk_XhE8wL5D" {
		t.Fatalf("api_key_prefix = %#v", item["api_key_prefix"])
	}
	if item["model_display_name"] != "Claude Sonnet 4.5" {
		t.Fatalf("model_display_name = %#v", item["model_display_name"])
	}
	if item["ingress_protocol"] != "anthropic" {
		t.Fatalf("ingress_protocol = %#v", item["ingress_protocol"])
	}
	if item["input_price_per_1m"] != "3" || item["output_price_per_1m"] != "15" {
		t.Fatalf("prices = %#v", item)
	}
	if item["uncached_input_tokens"] != float64(80) || item["cache_write_5m_input_tokens"] != float64(10) {
		t.Fatalf("token breakdown = %#v", item)
	}
	if item["first_token_ms"] != float64(400) || item["tps"] == nil {
		t.Fatalf("timing = %#v", item)
	}
}

func strPtr(value string) *string {
	return &value
}

func TestRequestFiltersUsesAuthenticatedUserID(t *testing.T) {
	service := &fakeRequestService{}
	handler := newRequestHandler(t, &fakeAuthService{}, service)
	req := httptest.NewRequest(http.MethodGet, "/v1/requests/filters", nil)
	req.AddCookie(&http.Cookie{Name: "unio_access_token", Value: "access-token"})
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || service.filtersID != 42 {
		t.Fatalf("status=%d user=%d body=%s", rec.Code, service.filtersID, rec.Body.String())
	}
	var payload struct {
		Data struct {
			APIKeys     []map[string]any `json:"api_keys"`
			Endpoints   []string         `json:"endpoints"`
			StreamTypes []string         `json:"stream_types"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Data.APIKeys) != 1 || payload.Data.APIKeys[0]["name"] != "prod" {
		t.Fatalf("api_keys = %#v", payload.Data.APIKeys)
	}
	if len(payload.Data.Endpoints) != 1 || payload.Data.Endpoints[0] != "/chat/completions" {
		t.Fatalf("endpoints = %#v", payload.Data.Endpoints)
	}
	if len(payload.Data.StreamTypes) != 1 || payload.Data.StreamTypes[0] != "stream" {
		t.Fatalf("stream_types = %#v", payload.Data.StreamTypes)
	}
}

func TestRequestListRejectsInvalidSort(t *testing.T) {
	handler := newRequestHandler(t, &fakeAuthService{}, &fakeRequestService{})
	req := httptest.NewRequest(http.MethodGet, "/v1/requests?sort=unknown", nil)
	req.AddCookie(&http.Cookie{Name: "unio_access_token", Value: "access-token"})
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"invalid_argument"`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
}
