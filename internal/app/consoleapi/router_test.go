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
)

const testUserUID = "0198c9d7-0af1-7c42-a063-91d2922af371"

type fakeAuthService struct {
	challengeIP              string
	checkedEmail             string
	registrationCheckedEmail string
	loginCalled              bool
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

func (s *fakeAuthService) PasswordLogin(context.Context, string, string) (serviceauth.User, serviceauth.TokenPair, *consoleservice.Error) {
	s.loginCalled = true
	return serviceauth.User{UID: testUserUID, Email: "user@example.com", DisplayName: "user"}, serviceauth.TokenPair{
		AccessToken: "access", RefreshToken: "refresh", AccessTTL: 15 * time.Minute, RefreshTTL: 24 * time.Hour,
	}, nil
}

func (s *fakeAuthService) EmailCodeLogin(context.Context, string, string, string, string) (serviceauth.User, serviceauth.TokenPair, *consoleservice.Error) {
	return serviceauth.User{}, serviceauth.TokenPair{}, nil
}

func (s *fakeAuthService) ResetPassword(context.Context, string, string, string, string, string) *consoleservice.Error {
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
