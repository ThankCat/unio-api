package consoleapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/app/consoleapi"
	"github.com/ThankCat/unio-gateway/internal/app/consoleapi/transport"
	"github.com/ThankCat/unio-gateway/internal/platform/config"
	consoleservice "github.com/ThankCat/unio-gateway/internal/service/console"
	serviceauth "github.com/ThankCat/unio-gateway/internal/service/console/auth"
	consolerequests "github.com/ThankCat/unio-gateway/internal/service/console/requests"
)

const testUserUID = "0198c9d7-0af1-7c42-a063-91d2922af371"

type fakeAuthService struct {
	challengeIP              string
	checkedEmail             string
	registrationCheckedEmail string
	loginCalled              bool
	loginIP                  string
	currentAccessToken       string
	resetVerificationEmail   string
	resetVerificationCode    string
	resetToken               string
	resetPassword            string
}

func (s *fakeAuthService) CheckEmail(_ context.Context, email string) *consoleservice.Error {
	s.checkedEmail = email
	return nil
}

func (s *fakeAuthService) CheckRegistrationEmail(_ context.Context, email string) *consoleservice.Error {
	s.registrationCheckedEmail = email
	return nil
}

func (s *fakeAuthService) SendChallenge(_ context.Context, _, _, ip string) (serviceauth.Challenge, *consoleservice.Error) {
	s.challengeIP = ip
	return serviceauth.Challenge{ID: "vch_test", ExpiresIn: 600, ResendAfter: 30}, nil
}

func (s *fakeAuthService) Register(context.Context, string, string, string, string, string) (serviceauth.User, serviceauth.TokenPair, *consoleservice.Error) {
	return serviceauth.User{}, serviceauth.TokenPair{}, nil
}

func (s *fakeAuthService) PasswordLogin(_ context.Context, _, _, ip string) (serviceauth.User, serviceauth.TokenPair, *consoleservice.Error) {
	s.loginCalled = true
	s.loginIP = ip
	return serviceauth.User{UID: testUserUID, Email: "user@example.com", DisplayName: "user"}, serviceauth.TokenPair{
		AccessToken: "access", RefreshToken: "refresh", AccessTTL: 15 * time.Minute, RefreshTTL: 24 * time.Hour,
	}, nil
}

func (s *fakeAuthService) CurrentUser(_ context.Context, accessToken string) (serviceauth.User, *consoleservice.Error) {
	s.currentAccessToken = accessToken
	return serviceauth.User{
		UID:         testUserUID,
		Email:       "user@example.com",
		DisplayName: "user",
		Balance: serviceauth.Balance{
			Currency:  "USD",
			Total:     "12.5",
			Reserved:  "2.25",
			Available: "10.25",
		},
	}, nil
}

func (s *fakeAuthService) AuthenticatePrincipal(_ context.Context, accessToken string) (serviceauth.Principal, *consoleservice.Error) {
	s.currentAccessToken = accessToken
	return serviceauth.Principal{UserID: 42, UID: testUserUID}, nil
}

func (s *fakeAuthService) EmailCodeLogin(context.Context, string, string, string, string) (serviceauth.User, serviceauth.TokenPair, *consoleservice.Error) {
	return serviceauth.User{}, serviceauth.TokenPair{}, nil
}

func (s *fakeAuthService) VerifyPasswordResetCode(
	_ context.Context,
	email, _, code, _ string,
) (serviceauth.PasswordResetGrant, *consoleservice.Error) {
	s.resetVerificationEmail = email
	s.resetVerificationCode = code
	return serviceauth.PasswordResetGrant{Token: "prt_test", ExpiresIn: 600}, nil
}

func (s *fakeAuthService) ResetPassword(_ context.Context, resetToken, password string) *consoleservice.Error {
	s.resetToken = resetToken
	s.resetPassword = password
	return nil
}

func (s *fakeAuthService) Refresh(context.Context, string) (serviceauth.TokenPair, *consoleservice.Error) {
	return serviceauth.TokenPair{}, nil
}

func (s *fakeAuthService) Logout(context.Context, string) *consoleservice.Error { return nil }

func (s *fakeAuthService) LogoutAll(context.Context, string) *consoleservice.Error { return nil }

func newTestRouter(t *testing.T, service *fakeAuthService) http.Handler {
	t.Helper()
	handler, err := consoleapi.NewRouter(consoleapi.Deps{
		Logger: zap.NewNop(),
		Config: config.ConsoleConfig{
			AllowedOrigins:    []string{"https://console.unioapi.com"},
			TrustedProxyCIDRs: []string{"10.0.0.0/8"},
			CookieSecure:      true,
			CookieDomain:      ".unioapi.com",
		},
		AuthService: service,
	})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func TestPasswordLoginReturnsPublicIDAndSecureCookies(t *testing.T) {
	service := &fakeAuthService{}
	handler := newTestRouter(t, service)
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/sessions/password", strings.NewReader(`{"email":"user@example.com","password":"Password1!"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://console.unioapi.com")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || !service.loginCalled {
		t.Fatalf("expected successful login, status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Data struct {
			User struct {
				ID string `json:"id"`
			} `json:"user"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.User.ID != testUserUID {
		t.Fatalf("expected public id %s, got %s", testUserUID, payload.Data.User.ID)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 2 {
		t.Fatalf("expected two authentication cookies, got %d", len(cookies))
	}
	for _, cookie := range cookies {
		if !cookie.HttpOnly || !cookie.Secure || cookie.Domain != "unioapi.com" {
			t.Fatalf("unexpected cookie attributes: %+v", cookie)
		}
	}
}

func TestCurrentUserRequiresAndPassesAccessCookie(t *testing.T) {
	service := &fakeAuthService{}
	handler := newTestRouter(t, service)

	missingRequest := httptest.NewRequest(http.MethodGet, "/v1/auth/me", nil)
	missingRecorder := httptest.NewRecorder()
	handler.ServeHTTP(missingRecorder, missingRequest)
	if missingRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected missing access cookie to return 401, got %d", missingRecorder.Code)
	}

	request := httptest.NewRequest(http.MethodGet, "/v1/auth/me", nil)
	request.AddCookie(&http.Cookie{Name: "unio_access_token", Value: "access-token"})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected current user 200, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	if service.currentAccessToken != "access-token" {
		t.Fatalf("expected access token to reach service, got %q", service.currentAccessToken)
	}
	var payload struct {
		Data struct {
			User struct {
				ID      string `json:"id"`
				Balance struct {
					Currency  string `json:"currency"`
					Total     string `json:"total"`
					Reserved  string `json:"reserved"`
					Available string `json:"available"`
				} `json:"balance"`
			} `json:"user"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.User.ID != testUserUID ||
		payload.Data.User.Balance.Currency != "USD" ||
		payload.Data.User.Balance.Available != "10.25" {
		t.Fatalf("unexpected current user payload: %+v body=%s", payload.Data.User, recorder.Body.String())
	}
}

func TestPasswordLoginReceivesResolvedClientIP(t *testing.T) {
	service := &fakeAuthService{}
	handler := newTestRouter(t, service)
	request := httptest.NewRequest(http.MethodPost, "/v1/auth/sessions/password", strings.NewReader(`{"email":"user@example.com","password":"Password1!"}`))
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = "10.0.0.3:443"
	request.Header.Set("X-Forwarded-For", "198.51.100.8, 10.0.0.2")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK || service.loginIP != "198.51.100.8" {
		t.Fatalf("expected resolved login IP, status=%d ip=%q", recorder.Code, service.loginIP)
	}
}

func TestInvalidJSONUsesStableErrorEnvelope(t *testing.T) {
	handler := newTestRouter(t, &fakeAuthService{})
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/sessions/password", strings.NewReader(`{"email":`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	var payload struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Error.Code != transport.CodeInvalidJSONBody {
		t.Fatalf("unexpected error code %q", payload.Error.Code)
	}
	if payload.Error.Message != "The JSON request body is invalid." {
		t.Fatalf("unexpected error message %q", payload.Error.Message)
	}
	if payload.Error.Type != "request_error" {
		t.Fatalf("unexpected error type %q", payload.Error.Type)
	}
}

func TestRequestSummaryRequiresAccessCookieWhenRegistered(t *testing.T) {
	service := &fakeAuthService{}
	handler, err := consoleapi.NewRouter(consoleapi.Deps{
		Logger: zap.NewNop(),
		Config: config.ConsoleConfig{
			AllowedOrigins:    []string{"https://console.unioapi.com"},
			TrustedProxyCIDRs: []string{"10.0.0.0/8"},
			CookieSecure:      true,
			CookieDomain:      ".unioapi.com",
		},
		AuthService:    service,
		RequestService: stubRequestService{},
	})
	if err != nil {
		t.Fatal(err)
	}

	missing := httptest.NewRecorder()
	handler.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/v1/requests/summary", nil))
	if missing.Code != http.StatusUnauthorized {
		t.Fatalf("expected missing cookie 401, got %d", missing.Code)
	}

	ok := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/requests/summary", nil)
	req.AddCookie(&http.Cookie{Name: "unio_access_token", Value: "access-token"})
	handler.ServeHTTP(ok, req)
	if ok.Code != http.StatusOK {
		t.Fatalf("expected summary 200, got %d body=%s", ok.Code, ok.Body.String())
	}
	if service.currentAccessToken != "access-token" {
		t.Fatalf("expected access token to reach principal lookup, got %q", service.currentAccessToken)
	}
}

type stubRequestService struct{}

func (stubRequestService) List(context.Context, consolerequests.ListParams) ([]consolerequests.Item, int64, *consoleservice.Error) {
	return []consolerequests.Item{}, 0, nil
}

func (stubRequestService) Summary(context.Context, consolerequests.SummaryParams) (consolerequests.Summary, *consoleservice.Error) {
	return consolerequests.Summary{}, nil
}

func (stubRequestService) Filters(context.Context, int64) (consolerequests.Filters, *consoleservice.Error) {
	return consolerequests.Filters{
		Routes:    []consolerequests.FilterOption{},
		APIKeys:   []consolerequests.FilterOption{},
		Endpoints: []string{},
	}, nil
}

func TestEmailCheckUsesReservedEndpoint(t *testing.T) {
	service := &fakeAuthService{}
	handler := newTestRouter(t, service)
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/email-checks", strings.NewReader(`{"email":"user@example.com"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if service.checkedEmail != "user@example.com" {
		t.Fatalf("expected email check service call, got %q", service.checkedEmail)
	}
	var payload struct {
		Data struct {
			Checked bool `json:"checked"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.Data.Checked {
		t.Fatal("expected checked response")
	}
}

func TestRegistrationEmailCheckUsesDedicatedEndpoint(t *testing.T) {
	service := &fakeAuthService{}
	handler := newTestRouter(t, service)
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/registration-email-checks", strings.NewReader(`{"email":"new@example.com"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if service.registrationCheckedEmail != "new@example.com" {
		t.Fatalf("expected registration email check service call, got %q", service.registrationCheckedEmail)
	}
	if service.checkedEmail != "" {
		t.Fatalf("login email check must not be called, got %q", service.checkedEmail)
	}
}

func TestPasswordResetUsesVerificationThenOneTimeCredential(t *testing.T) {
	service := &fakeAuthService{}
	handler := newTestRouter(t, service)
	verifyRequest := httptest.NewRequest(
		http.MethodPost,
		"/v1/auth/password-reset-verifications",
		strings.NewReader(`{"email":"user@example.com","challenge_id":"vch_test","code":"123456"}`),
	)
	verifyRequest.Header.Set("Content-Type", "application/json")
	verifyRecorder := httptest.NewRecorder()

	handler.ServeHTTP(verifyRecorder, verifyRequest)

	if verifyRecorder.Code != http.StatusOK {
		t.Fatalf("expected password reset verification 200, got %d body=%s", verifyRecorder.Code, verifyRecorder.Body.String())
	}
	if service.resetVerificationEmail != "user@example.com" || service.resetVerificationCode != "123456" {
		t.Fatalf("unexpected reset verification input: email=%q code=%q", service.resetVerificationEmail, service.resetVerificationCode)
	}
	var verificationPayload struct {
		Data serviceauth.PasswordResetGrant `json:"data"`
	}
	if err := json.Unmarshal(verifyRecorder.Body.Bytes(), &verificationPayload); err != nil {
		t.Fatal(err)
	}
	if verificationPayload.Data.Token != "prt_test" || verificationPayload.Data.ExpiresIn != 600 {
		t.Fatalf("unexpected reset grant response: %+v", verificationPayload.Data)
	}

	resetRequest := httptest.NewRequest(
		http.MethodPost,
		"/v1/auth/password-resets",
		strings.NewReader(`{"reset_token":"prt_test","new_password":"Password1!"}`),
	)
	resetRequest.Header.Set("Content-Type", "application/json")
	resetRecorder := httptest.NewRecorder()
	handler.ServeHTTP(resetRecorder, resetRequest)

	if resetRecorder.Code != http.StatusOK {
		t.Fatalf("expected password reset 200, got %d body=%s", resetRecorder.Code, resetRecorder.Body.String())
	}
	if service.resetToken != "prt_test" || service.resetPassword != "Password1!" {
		t.Fatalf("unexpected password reset input: token=%q password=%q", service.resetToken, service.resetPassword)
	}
}

func TestDisallowedOriginIsRejectedBeforeAuthentication(t *testing.T) {
	service := &fakeAuthService{}
	handler := newTestRouter(t, service)
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/sessions/password", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://attacker.example")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden || service.loginCalled {
		t.Fatalf("expected rejected origin before service call, status=%d", rec.Code)
	}
}

func TestCORSPreflightAllowsAuthenticatedGet(t *testing.T) {
	handler := newTestRouter(t, &fakeAuthService{})
	req := httptest.NewRequest(http.MethodOptions, "/v1/auth/me", nil)
	req.Header.Set("Origin", "https://console.unioapi.com")
	req.Header.Set("Access-Control-Request-Method", http.MethodGet)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected preflight 204, got %d", rec.Code)
	}
	if methods := rec.Header().Get("Access-Control-Allow-Methods"); methods != "GET, POST, OPTIONS" {
		t.Fatalf("unexpected allowed methods %q", methods)
	}
}

func TestTrustedProxyClientIPIsPassedToChallengeService(t *testing.T) {
	service := &fakeAuthService{}
	handler := newTestRouter(t, service)
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/email-challenges", strings.NewReader(`{"email":"user@example.com","purpose":"login"}`))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "10.0.0.3:443"
	req.Header.Set("X-Forwarded-For", "198.51.100.8, 10.0.0.2")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	if service.challengeIP != "198.51.100.8" {
		t.Fatalf("expected client IP 198.51.100.8, got %s", service.challengeIP)
	}
}
