package apikeys_test

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

	consoleapikeyshttp "github.com/ThankCat/unio-gateway/internal/app/consoleapi/apikeys"
	consoleauth "github.com/ThankCat/unio-gateway/internal/app/consoleapi/auth"
	"github.com/ThankCat/unio-gateway/internal/app/consoleapi/transport"
	consoleservice "github.com/ThankCat/unio-gateway/internal/service/console"
	consoleapikeys "github.com/ThankCat/unio-gateway/internal/service/console/apikeys"
	serviceauth "github.com/ThankCat/unio-gateway/internal/service/console/auth"
)

const (
	testUID       = "0198c9d7-0af1-7c42-a063-91d2922af371"
	sessionUserID = 42
	window        = "from=2026-07-28T00:00:00Z&to=2026-08-27T00:00:00Z"
)

type fakeAuthService struct{}

func (s *fakeAuthService) CheckEmail(context.Context, string) *consoleservice.Error { return nil }
func (s *fakeAuthService) CheckRegistrationEmail(context.Context, string) *consoleservice.Error {
	return nil
}
func (s *fakeAuthService) SendChallenge(context.Context, string, string, string) (serviceauth.Challenge, *consoleservice.Error) {
	return serviceauth.Challenge{}, nil
}
func (s *fakeAuthService) Register(context.Context, string, string, string, string, string, string) (serviceauth.User, serviceauth.TokenPair, *consoleservice.Error) {
	return serviceauth.User{}, serviceauth.TokenPair{}, nil
}
func (s *fakeAuthService) PasswordLogin(context.Context, string, string, string, string) (serviceauth.User, serviceauth.TokenPair, *consoleservice.Error) {
	return serviceauth.User{}, serviceauth.TokenPair{}, nil
}
func (s *fakeAuthService) EmailCodeLogin(context.Context, string, string, string, string, string) (serviceauth.User, serviceauth.TokenPair, *consoleservice.Error) {
	return serviceauth.User{}, serviceauth.TokenPair{}, nil
}
func (s *fakeAuthService) CurrentUser(context.Context, string) (serviceauth.User, *consoleservice.Error) {
	return serviceauth.User{}, nil
}
func (s *fakeAuthService) AuthenticatePrincipal(context.Context, string) (serviceauth.Principal, *consoleservice.Error) {
	return serviceauth.Principal{UserID: sessionUserID, UID: testUID}, nil
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
func (s *fakeAuthService) UpdateDisplayName(context.Context, string, string) (serviceauth.User, *consoleservice.Error) {
	return serviceauth.User{}, nil
}
func (s *fakeAuthService) ChangePassword(context.Context, string, string, string) *consoleservice.Error {
	return nil
}
func (s *fakeAuthService) ListSessions(context.Context, string) ([]serviceauth.SessionEntry, *consoleservice.Error) {
	return nil, nil
}
func (s *fakeAuthService) RevokeSession(context.Context, string, string) *consoleservice.Error {
	return nil
}
func (s *fakeAuthService) LogoutOthers(context.Context, string) *consoleservice.Error { return nil }

// fakeService 记录服务层收到的每个入参，测试据此断言归属是否被正确注入。
type fakeService struct {
	listParams    consoleapikeys.ListParams
	getParams     consoleapikeys.GetParams
	createParams  consoleapikeys.CreateParams
	updateParams  consoleapikeys.UpdateParams
	summaryUserID int64
	revokeUserID  int64
	revokeKeyID   int64
	deleteUserID  int64
	deleteKeyID   int64
}

func sampleKey() consoleapikeys.Key {
	used := time.Date(2026, 8, 26, 5, 0, 0, 0, time.UTC)
	limit := "2000"
	return consoleapikeys.Key{
		ID:              7,
		Name:            "生产环境",
		KeyPrefix:       "sk-unio-a3f9k2m1",
		Status:          consoleapikeys.StatusActive,
		SpendLimit:      &limit,
		SpentTotal:      "1486.2073",
		PeriodChargeUSD: "612.4419",
		RequestCount:    284913,
		LastUsedAt:      &used,
		CreatedAt:       time.Date(2026, 1, 24, 2, 0, 0, 0, time.UTC),
		UpdatedAt:       time.Date(2026, 8, 20, 2, 0, 0, 0, time.UTC),
		Trend: []consoleapikeys.DailyCharge{
			{Day: time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC), RequestCount: 900, ChargeUSD: "21.5"},
		},
	}
}

func (s *fakeService) List(_ context.Context, params consoleapikeys.ListParams) ([]consoleapikeys.Key, int64, *consoleservice.Error) {
	s.listParams = params
	return []consoleapikeys.Key{sampleKey()}, 1, nil
}

func (s *fakeService) Summary(_ context.Context, userID int64, _ consoleapikeys.Window) (consoleapikeys.Summary, *consoleservice.Error) {
	s.summaryUserID = userID
	return consoleapikeys.Summary{
		KeyTotal:     6,
		KeyActive:    4,
		NearLimit:    2,
		RequestCount: 424012,
		ChargeUSD:    "1922.41",
	}, nil
}

func (s *fakeService) Get(_ context.Context, params consoleapikeys.GetParams) (consoleapikeys.Detail, *consoleservice.Error) {
	s.getParams = params
	return consoleapikeys.Detail{
		Key: sampleKey(),
		TopModels: []consoleapikeys.TopModel{
			{ModelID: "claude-sonnet-5", DisplayName: "Claude Sonnet 5", RequestCount: 900, ChargeUSD: "300"},
		},
	}, nil
}

func (s *fakeService) Create(_ context.Context, params consoleapikeys.CreateParams) (consoleapikeys.CreatedKey, *consoleservice.Error) {
	s.createParams = params
	return consoleapikeys.CreatedKey{
		Key:       sampleKey(),
		Plaintext: "sk-unio-a3f9k2m1x7bq4vzn8dht",
	}, nil
}

func (s *fakeService) Update(_ context.Context, params consoleapikeys.UpdateParams) (consoleapikeys.Key, *consoleservice.Error) {
	s.updateParams = params
	return sampleKey(), nil
}

func (s *fakeService) Revoke(_ context.Context, userID, keyID int64) (consoleapikeys.Key, *consoleservice.Error) {
	s.revokeUserID = userID
	s.revokeKeyID = keyID
	key := sampleKey()
	key.Status = consoleapikeys.StatusRevoked
	return key, nil
}

func (s *fakeService) Delete(_ context.Context, userID, keyID int64) *consoleservice.Error {
	s.deleteUserID = userID
	s.deleteKeyID = keyID
	return nil
}

func newHandler(t *testing.T, service *fakeService) http.Handler {
	t.Helper()
	errorWriter := transport.NewErrorWriter(zap.NewNop())
	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(consoleauth.RequireAuth(&fakeAuthService{}, errorWriter))
			consoleapikeyshttp.Register(r, consoleapikeyshttp.Deps{
				Service:     service,
				ErrorWriter: errorWriter,
			})
		})
	})
	return r
}

func do(t *testing.T, handler http.Handler, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "unio_access_token", Value: "access-token"})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestAPIKeyRoutesRequireSession(t *testing.T) {
	handler := newHandler(t, &fakeService{})
	for _, tc := range []struct {
		method string
		target string
	}{
		{http.MethodGet, "/v1/api-keys?" + window},
		{http.MethodPost, "/v1/api-keys"},
		{http.MethodGet, "/v1/api-keys/summary?" + window},
		{http.MethodGet, "/v1/api-keys/7?" + window},
		{http.MethodPatch, "/v1/api-keys/7"},
		{http.MethodPost, "/v1/api-keys/7/revoke"},
		{http.MethodDelete, "/v1/api-keys/7"},
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.target, strings.NewReader("{}")))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s: status = %d body=%s", tc.method, tc.target, rec.Code, rec.Body.String())
		}
	}
}

// 归属只能来自会话。这条守的是「传别人的 user_id 也没用」——
// handler 压根不读请求里的 user_id，服务层拿到的永远是会话那个。
func TestAPIKeyHandlersTakeUserIDFromSessionOnly(t *testing.T) {
	service := &fakeService{}
	handler := newHandler(t, service)

	if rec := do(t, handler, http.MethodGet, "/v1/api-keys?user_id=9999&"+window, ""); rec.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", rec.Code, rec.Body.String())
	}
	if service.listParams.UserID != sessionUserID {
		t.Fatalf("list user id = %d, want %d", service.listParams.UserID, sessionUserID)
	}

	if rec := do(t, handler, http.MethodGet, "/v1/api-keys/7?user_id=9999&"+window, ""); rec.Code != http.StatusOK {
		t.Fatalf("get status = %d body=%s", rec.Code, rec.Body.String())
	}
	if service.getParams.UserID != sessionUserID || service.getParams.KeyID != 7 {
		t.Fatalf("get params = %+v", service.getParams)
	}

	body := `{"name":"ci","user_id":9999}`
	if rec := do(t, handler, http.MethodPost, "/v1/api-keys", body); rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", rec.Code, rec.Body.String())
	}
	if service.createParams.UserID != sessionUserID {
		t.Fatalf("create user id = %d", service.createParams.UserID)
	}

	if rec := do(t, handler, http.MethodPatch, "/v1/api-keys/7", `{"name":"renamed"}`); rec.Code != http.StatusOK {
		t.Fatalf("update status = %d body=%s", rec.Code, rec.Body.String())
	}
	if service.updateParams.UserID != sessionUserID || service.updateParams.KeyID != 7 {
		t.Fatalf("update params = %+v", service.updateParams)
	}

	if rec := do(t, handler, http.MethodPost, "/v1/api-keys/7/revoke", ""); rec.Code != http.StatusOK {
		t.Fatalf("revoke status = %d body=%s", rec.Code, rec.Body.String())
	}
	if service.revokeUserID != sessionUserID || service.revokeKeyID != 7 {
		t.Fatalf("revoke = user=%d key=%d", service.revokeUserID, service.revokeKeyID)
	}

	if rec := do(t, handler, http.MethodDelete, "/v1/api-keys/7", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d body=%s", rec.Code, rec.Body.String())
	}
	if service.deleteUserID != sessionUserID || service.deleteKeyID != 7 {
		t.Fatalf("delete = user=%d key=%d", service.deleteUserID, service.deleteKeyID)
	}

	if service.summaryUserID != 0 {
		t.Fatal("summary should not have been called yet")
	}
	if rec := do(t, handler, http.MethodGet, "/v1/api-keys/summary?"+window, ""); rec.Code != http.StatusOK {
		t.Fatalf("summary status = %d body=%s", rec.Code, rec.Body.String())
	}
	if service.summaryUserID != sessionUserID {
		t.Fatalf("summary user id = %d", service.summaryUserID)
	}
}

// 明文只在 201 响应里。任何读接口冒出 plaintext 都意味着前端会长出「稍后再复制」的入口。
func TestOnlyCreateResponseCarriesPlaintext(t *testing.T) {
	handler := newHandler(t, &fakeService{})

	created := do(t, handler, http.MethodPost, "/v1/api-keys", `{"name":"ci"}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", created.Code, created.Body.String())
	}
	if !strings.Contains(created.Body.String(), "sk-unio-a3f9k2m1x7bq4vzn8dht") {
		t.Fatalf("create must return the one-time plaintext: %s", created.Body.String())
	}

	for _, tc := range []struct {
		name   string
		method string
		target string
		body   string
	}{
		{"list", http.MethodGet, "/v1/api-keys?" + window, ""},
		{"detail", http.MethodGet, "/v1/api-keys/7?" + window, ""},
		{"update", http.MethodPatch, "/v1/api-keys/7", `{"name":"renamed"}`},
		{"revoke", http.MethodPost, "/v1/api-keys/7/revoke", ""},
	} {
		rec := do(t, handler, tc.method, tc.target, tc.body)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d body=%s", tc.name, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "plaintext") {
			t.Fatalf("%s must not expose plaintext: %s", tc.name, rec.Body.String())
		}
	}
}

// key_hash 是认证凭据的等价物，任何接口都不许出现。
func TestAPIKeyResponsesNeverLeakHash(t *testing.T) {
	handler := newHandler(t, &fakeService{})
	rec := do(t, handler, http.MethodGet, "/v1/api-keys?"+window, "")
	for _, leaked := range []string{"key_hash", "hash"} {
		if strings.Contains(rec.Body.String(), leaked) {
			t.Fatalf("response leaked %s: %s", leaked, rec.Body.String())
		}
	}
}

func TestAPIKeyListShape(t *testing.T) {
	handler := newHandler(t, &fakeService{})
	rec := do(t, handler, http.MethodGet, "/v1/api-keys?page=1&page_size=20&"+window, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Data struct {
			Items []struct {
				ID              int64   `json:"id"`
				Name            string  `json:"name"`
				KeyPrefix       string  `json:"key_prefix"`
				Status          string  `json:"status"`
				SpendLimit      *string `json:"spend_limit"`
				SpentTotal      string  `json:"spent_total"`
				PeriodChargeUSD string  `json:"period_charge_usd"`
				RequestCount    int64   `json:"request_count"`
				Trend           []struct {
					Day       string `json:"day"`
					ChargeUSD string `json:"charge_usd"`
				} `json:"trend"`
			} `json:"items"`
			Page     int   `json:"page"`
			PageSize int   `json:"page_size"`
			Total    int64 `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.Page != 1 || payload.Data.PageSize != 20 || payload.Data.Total != 1 {
		t.Fatalf("paging = %+v", payload.Data)
	}
	if len(payload.Data.Items) != 1 {
		t.Fatalf("items = %+v", payload.Data.Items)
	}
	item := payload.Data.Items[0]
	if item.KeyPrefix != "sk-unio-a3f9k2m1" || item.Status != "active" {
		t.Fatalf("item = %+v", item)
	}
	if item.PeriodChargeUSD != "612.4419" || item.RequestCount != 284913 {
		t.Fatalf("window stats = %+v", item)
	}
	if len(item.Trend) != 1 || item.Trend[0].ChargeUSD != "21.5" {
		t.Fatalf("trend = %+v", item.Trend)
	}
}

// null 和字段缺省在 JSON 里长得很像，但语义相反：
// null=清空额度上限，缺省=不动它。这条守住这个区分。
func TestUpdateDistinguishesNullFromOmitted(t *testing.T) {
	service := &fakeService{}
	handler := newHandler(t, service)

	if rec := do(t, handler, http.MethodPatch, "/v1/api-keys/7", `{"spend_limit":null}`); rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !service.updateParams.SpendLimitProvided || service.updateParams.SpendLimit != nil {
		t.Fatalf("explicit null should clear the limit: %+v", service.updateParams)
	}

	service.updateParams = consoleapikeys.UpdateParams{}
	if rec := do(t, handler, http.MethodPatch, "/v1/api-keys/7", `{"name":"renamed"}`); rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if service.updateParams.SpendLimitProvided {
		t.Fatalf("omitted spend_limit must not be touched: %+v", service.updateParams)
	}

	service.updateParams = consoleapikeys.UpdateParams{}
	if rec := do(t, handler, http.MethodPatch, "/v1/api-keys/7", `{"expires_at":null}`); rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !service.updateParams.ExpiresProvided || service.updateParams.ExpiresAt != nil {
		t.Fatalf("explicit null should clear the expiry: %+v", service.updateParams)
	}
}

func TestListRejectsBadQuery(t *testing.T) {
	handler := newHandler(t, &fakeService{})
	for _, target := range []string{
		"/v1/api-keys",
		"/v1/api-keys?from=2026-07-28T00:00:00Z",
		"/v1/api-keys?" + window + "&status=nonsense",
		"/v1/api-keys?" + window + "&page=0",
	} {
		rec := do(t, handler, http.MethodGet, target, "")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d body=%s", target, rec.Code, rec.Body.String())
		}
	}
}

func TestPathIDMustBePositive(t *testing.T) {
	handler := newHandler(t, &fakeService{})
	for _, target := range []string{"/v1/api-keys/0", "/v1/api-keys/-1", "/v1/api-keys/abc"} {
		rec := do(t, handler, http.MethodGet, target+"?"+window, "")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d body=%s", target, rec.Code, rec.Body.String())
		}
	}
}
