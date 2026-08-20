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
	listParams consolerequests.ListParams
	summaryID  int64
	filtersID  int64
}

func (s *fakeRequestService) List(_ context.Context, params consolerequests.ListParams) ([]consolerequests.Item, int64, *consoleservice.Error) {
	s.listParams = params
	routeID := int64(3)
	reasoning := "medium"
	latency := int64(1500)
	return []consolerequests.Item{{
		ID:               11,
		RequestID:        "req_safe",
		CreatedAt:        time.Date(2026, 8, 20, 8, 0, 0, 0, time.UTC),
		ClientIP:         "203.0.113.10",
		RouteID:          &routeID,
		RouteName:        "Claude",
		APIKeyID:         9,
		APIKeyName:       "prod",
		Endpoint:         "/v1/chat/completions",
		Stream:           true,
		RequestedModelID: "claude-sonnet-4-5",
		ReasoningEffort:  &reasoning,
		InputTokens:      100,
		OutputTokens:     20,
		LatencyMs:        &latency,
		UserChargeUSD:    "0.15",
		Status:           "2xx",
	}}, 1, nil
}

func (s *fakeRequestService) Summary(_ context.Context, userID int64) (consolerequests.Summary, *consoleservice.Error) {
	s.summaryID = userID
	return consolerequests.Summary{
		RequestCount:     4,
		TokenCount:       180,
		ChargeUSD:        "1.25",
		AverageLatencyMs: 750,
	}, nil
}

func (s *fakeRequestService) Filters(_ context.Context, userID int64) (consolerequests.Filters, *consoleservice.Error) {
	s.filtersID = userID
	return consolerequests.Filters{
		Routes:    []consolerequests.FilterOption{{ID: 3, Name: "Claude"}},
		APIKeys:   []consolerequests.FilterOption{{ID: 9, Name: "prod"}},
		Endpoints: []string{"/v1/chat/completions"},
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
	if auth.accessToken != "access-token" || service.summaryID != 42 {
		t.Fatalf("token=%q user=%d", auth.accessToken, service.summaryID)
	}
	var payload struct {
		Data struct {
			RequestCount     int64   `json:"request_count"`
			TokenCount       int64   `json:"token_count"`
			ChargeUSD        string  `json:"charge_usd"`
			AverageLatencyMs float64 `json:"average_latency_ms"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.RequestCount != 4 || payload.Data.ChargeUSD != "1.25" {
		t.Fatalf("payload = %+v", payload.Data)
	}
}

func TestRequestListOmitsInternalFieldsAndScopesToUser(t *testing.T) {
	service := &fakeRequestService{}
	handler := newRequestHandler(t, &fakeAuthService{}, service)
	req := httptest.NewRequest(
		http.MethodGet,
		"/v1/requests?page=1&page_size=20&route_id=3&endpoint=/v1/chat/completions&status=2xx&sort=-created_at",
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
		"api_key_plaintext",
		"total_cost",
		"channel_chain",
		"error_code",
		"internal_error",
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
	if item["status"] != "2xx" || item["endpoint"] != "/v1/chat/completions" || item["api_key_name"] != "prod" {
		t.Fatalf("item = %#v", item)
	}
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
}

func TestRequestListRejectsInvalidSort(t *testing.T) {
	handler := newRequestHandler(t, &fakeAuthService{}, &fakeRequestService{})
	req := httptest.NewRequest(http.MethodGet, "/v1/requests?sort=cost", nil)
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
