package auth_test

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
	consolemiddleware "github.com/ThankCat/unio-gateway/internal/app/consoleapi/middleware"
	"github.com/ThankCat/unio-gateway/internal/app/consoleapi/transport"
	consoleservice "github.com/ThankCat/unio-gateway/internal/service/console"
	serviceauth "github.com/ThankCat/unio-gateway/internal/service/console/auth"
)

const (
	accessCookie  = "unio_access_token"
	refreshCookie = "unio_refresh_token"
	userUID       = "0198c9d7-0af1-7c42-a063-91d2922af371"
)

// recordingService 记录 HTTP 层传给 service 的入参，并按预设返回结果/错误。
type recordingService struct {
	calls        []string
	lastArgs     []string
	loginErr     *consoleservice.Error
	refreshErr   *consoleservice.Error
	principalErr *consoleservice.Error
	sessions     []serviceauth.SessionEntry
}

func (s *recordingService) record(name string, args ...string) {
	s.calls = append(s.calls, name)
	s.lastArgs = args
}

func samplePair() serviceauth.TokenPair {
	return serviceauth.TokenPair{AccessToken: "access-1", RefreshToken: "refresh-1", AccessTTL: 15 * time.Minute, RefreshTTL: 720 * time.Hour, SessionID: "sid-1", UserUID: userUID}
}

func sampleUser() serviceauth.User {
	return serviceauth.User{UID: userUID, Email: "user@example.test", DisplayName: "User", PasswordConfigured: true}
}

func (s *recordingService) CheckEmail(_ context.Context, email string) *consoleservice.Error {
	s.record("CheckEmail", email)
	return nil
}
func (s *recordingService) CheckRegistrationEmail(_ context.Context, email string) *consoleservice.Error {
	s.record("CheckRegistrationEmail", email)
	return nil
}
func (s *recordingService) SendChallenge(_ context.Context, email, purpose, ip, locale string) (serviceauth.Challenge, *consoleservice.Error) {
	s.record("SendChallenge", email, purpose, ip, locale)
	return serviceauth.Challenge{ID: "ch-1", ExpiresIn: 600, ResendAfter: 60}, nil
}
func (s *recordingService) Register(_ context.Context, email, password, challengeID, code, ip, userAgent string) (serviceauth.User, serviceauth.TokenPair, *consoleservice.Error) {
	s.record("Register", email, password, challengeID, code, ip, userAgent)
	return sampleUser(), samplePair(), nil
}
func (s *recordingService) PasswordLogin(_ context.Context, email, password, ip, userAgent string) (serviceauth.User, serviceauth.TokenPair, *consoleservice.Error) {
	s.record("PasswordLogin", email, password, ip, userAgent)
	if s.loginErr != nil {
		return serviceauth.User{}, serviceauth.TokenPair{}, s.loginErr
	}
	return sampleUser(), samplePair(), nil
}
func (s *recordingService) EmailCodeLogin(_ context.Context, email, challengeID, code, ip, userAgent string) (serviceauth.User, serviceauth.TokenPair, *consoleservice.Error) {
	s.record("EmailCodeLogin", email, challengeID, code, ip, userAgent)
	return sampleUser(), samplePair(), nil
}
func (s *recordingService) CurrentUser(_ context.Context, token string) (serviceauth.User, *consoleservice.Error) {
	s.record("CurrentUser", token)
	return sampleUser(), nil
}
func (s *recordingService) AuthenticatePrincipal(_ context.Context, token string) (serviceauth.Principal, *consoleservice.Error) {
	s.record("AuthenticatePrincipal", token)
	if s.principalErr != nil {
		return serviceauth.Principal{}, s.principalErr
	}
	return serviceauth.Principal{UserID: 42, UID: userUID}, nil
}
func (s *recordingService) VerifyPasswordResetCode(_ context.Context, email, challengeID, code, ip string) (serviceauth.PasswordResetGrant, *consoleservice.Error) {
	s.record("VerifyPasswordResetCode", email, challengeID, code, ip)
	return serviceauth.PasswordResetGrant{Token: "reset-1", ExpiresIn: 300}, nil
}
func (s *recordingService) ResetPassword(_ context.Context, token, password string) *consoleservice.Error {
	s.record("ResetPassword", token, password)
	return nil
}
func (s *recordingService) Refresh(_ context.Context, token string) (serviceauth.TokenPair, *consoleservice.Error) {
	s.record("Refresh", token)
	if s.refreshErr != nil {
		return serviceauth.TokenPair{}, s.refreshErr
	}
	pair := samplePair()
	pair.AccessToken, pair.RefreshToken = "access-2", "refresh-2"
	return pair, nil
}
func (s *recordingService) Logout(_ context.Context, token string) *consoleservice.Error {
	s.record("Logout", token)
	return nil
}
func (s *recordingService) LogoutAll(_ context.Context, token string) *consoleservice.Error {
	s.record("LogoutAll", token)
	return nil
}
func (s *recordingService) UpdateDisplayName(_ context.Context, token, name string) (serviceauth.User, *consoleservice.Error) {
	s.record("UpdateDisplayName", token, name)
	user := sampleUser()
	user.DisplayName = name
	return user, nil
}
func (s *recordingService) SendPasswordChallenge(_ context.Context, token, ip, locale string) (serviceauth.Challenge, *consoleservice.Error) {
	s.record("SendPasswordChallenge", token, ip, locale)
	return serviceauth.Challenge{ID: "ch-2", ExpiresIn: 600, ResendAfter: 60}, nil
}
func (s *recordingService) UpdatePassword(_ context.Context, token, challengeID, code, password, ip string) *consoleservice.Error {
	s.record("UpdatePassword", token, challengeID, code, password, ip)
	return nil
}
func (s *recordingService) ListSessions(_ context.Context, token string) ([]serviceauth.SessionEntry, *consoleservice.Error) {
	s.record("ListSessions", token)
	return s.sessions, nil
}
func (s *recordingService) RevokeSession(_ context.Context, token, sid string) *consoleservice.Error {
	s.record("RevokeSession", token, sid)
	return nil
}
func (s *recordingService) LogoutOthers(_ context.Context, token string) *consoleservice.Error {
	s.record("LogoutOthers", token)
	return nil
}

func newAuthRouter(t *testing.T, service consoleauth.Service, trustedCIDRs ...string) http.Handler {
	t.Helper()
	resolver, err := consolemiddleware.NewClientIPResolver(trustedCIDRs)
	if err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	r.Use(consolemiddleware.ClientIP(resolver))
	r.Route("/v1", func(r chi.Router) {
		consoleauth.Register(r, consoleauth.Deps{
			CookieDomain: "console.example.test",
			CookieSecure: true,
			Service:      service,
			ErrorWriter:  transport.NewErrorWriter(zap.NewNop()),
		})
	})
	return r
}

func jsonRequest(t *testing.T, method, target, body string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.RemoteAddr = "203.0.113.9:40000"
	return req
}

func cookieByName(t *testing.T, rec *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == name {
			return cookie
		}
	}
	return nil
}

func decodeErrorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body %q: %v", rec.Body.String(), err)
	}
	return body.Error.Code
}

// 登录成功：两把令牌落 HttpOnly/Secure Cookie，刷新 Cookie 路径限定 /v1/auth/sessions，响应体不含令牌。
func TestPasswordSessionSetsScopedHttpOnlyCookies(t *testing.T) {
	service := &recordingService{}
	router := newAuthRouter(t, service)

	req := jsonRequest(t, http.MethodPost, "/v1/auth/sessions/password", `{"email":"user@example.test","password":"secret-pass"}`)
	req.Header.Set("User-Agent", "unio-console-test/1.0")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if len(service.calls) != 1 || service.calls[0] != "PasswordLogin" {
		t.Fatalf("calls = %v", service.calls)
	}
	// 来源 IP 由可信代理解析器提供（此处无可信代理，落 RemoteAddr），User-Agent 原样透传给会话记录。
	if service.lastArgs[2] != "203.0.113.9" || service.lastArgs[3] != "unio-console-test/1.0" {
		t.Fatalf("login args = %v", service.lastArgs)
	}
	access := cookieByName(t, rec, accessCookie)
	refresh := cookieByName(t, rec, refreshCookie)
	if access == nil || refresh == nil {
		t.Fatalf("both token cookies must be set: %+v", rec.Result().Cookies())
	}
	if access.Value != "access-1" || !access.HttpOnly || !access.Secure || access.Path != "/" || access.Domain != "console.example.test" || access.SameSite != http.SameSiteLaxMode {
		t.Fatalf("access cookie attributes = %+v", access)
	}
	if refresh.Value != "refresh-1" || !refresh.HttpOnly || !refresh.Secure || refresh.Path != "/v1/auth/sessions" {
		t.Fatalf("refresh cookie attributes = %+v", refresh)
	}
	if refresh.MaxAge != int((720*time.Hour).Seconds()) || access.MaxAge != int((15*time.Minute).Seconds()) {
		t.Fatalf("cookie max-age = access %d refresh %d", access.MaxAge, refresh.MaxAge)
	}
	if body := rec.Body.String(); strings.Contains(body, "access-1") || strings.Contains(body, "refresh-1") {
		t.Fatalf("tokens must never appear in the JSON body: %s", body)
	}
	if !strings.Contains(rec.Body.String(), `"id":"`+userUID+`"`) {
		t.Fatalf("response must carry the public user: %s", rec.Body.String())
	}
}

func TestPasswordSessionFailureWritesErrorWithoutCookies(t *testing.T) {
	service := &recordingService{loginErr: &consoleservice.Error{Code: "invalid_credentials", Message: "Invalid email or password.", Status: http.StatusUnauthorized}}
	router := newAuthRouter(t, service)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, jsonRequest(t, http.MethodPost, "/v1/auth/sessions/password", `{"email":"user@example.test","password":"nope"}`))
	if rec.Code != http.StatusUnauthorized || decodeErrorCode(t, rec) != "invalid_credentials" {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Fatalf("failed login must not set cookies: %+v", rec.Result().Cookies())
	}
}

func TestPasswordSessionRejectsNonJSONBody(t *testing.T) {
	service := &recordingService{}
	router := newAuthRouter(t, service)

	req := httptest.NewRequest(http.MethodPost, "/v1/auth/sessions/password", strings.NewReader("email=a"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if len(service.calls) != 0 {
		t.Fatalf("malformed body must not reach the service: %v", service.calls)
	}
}

// 刷新只认刷新 Cookie；成功后轮换两把令牌，失败后清空两把 Cookie。
func TestRefreshRotatesCookiesAndClearsThemOnFailure(t *testing.T) {
	service := &recordingService{}
	router := newAuthRouter(t, service)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, jsonRequest(t, http.MethodPost, "/v1/auth/sessions/refresh", ""))
	if rec.Code != http.StatusUnauthorized || decodeErrorCode(t, rec) != serviceauth.CodeRefreshTokenInvalid {
		t.Fatalf("missing refresh cookie: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(service.calls) != 0 {
		t.Fatal("missing cookie must not call the service")
	}

	req := jsonRequest(t, http.MethodPost, "/v1/auth/sessions/refresh", "")
	req.AddCookie(&http.Cookie{Name: refreshCookie, Value: "refresh-1"})
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh status = %d body=%s", rec.Code, rec.Body.String())
	}
	if service.lastArgs[0] != "refresh-1" {
		t.Fatalf("refresh must forward the cookie value, got %v", service.lastArgs)
	}
	if access := cookieByName(t, rec, accessCookie); access == nil || access.Value != "access-2" {
		t.Fatalf("access cookie must rotate: %+v", access)
	}
	if refresh := cookieByName(t, rec, refreshCookie); refresh == nil || refresh.Value != "refresh-2" {
		t.Fatalf("refresh cookie must rotate: %+v", refresh)
	}

	service.refreshErr = &consoleservice.Error{Code: serviceauth.CodeRefreshTokenInvalid, Message: "revoked", Status: http.StatusUnauthorized}
	req = jsonRequest(t, http.MethodPost, "/v1/auth/sessions/refresh", "")
	req.AddCookie(&http.Cookie{Name: refreshCookie, Value: "refresh-2"})
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked refresh status = %d", rec.Code)
	}
	for _, name := range []string{accessCookie, refreshCookie} {
		cookie := cookieByName(t, rec, name)
		if cookie == nil || cookie.Value != "" || cookie.MaxAge != -1 {
			t.Fatalf("failed refresh must expire %s: %+v", name, cookie)
		}
	}
}

func TestLogoutClearsCookiesEvenWithoutRefreshCookie(t *testing.T) {
	service := &recordingService{}
	router := newAuthRouter(t, service)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, jsonRequest(t, http.MethodPost, "/v1/auth/sessions/logout", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("logout status = %d body=%s", rec.Code, rec.Body.String())
	}
	if service.calls[0] != "Logout" || service.lastArgs[0] != "" {
		t.Fatalf("logout without cookie must still call the service with an empty token: %v %v", service.calls, service.lastArgs)
	}
	if access := cookieByName(t, rec, accessCookie); access == nil || access.MaxAge != -1 {
		t.Fatalf("logout must expire the access cookie: %+v", access)
	}
	if refresh := cookieByName(t, rec, refreshCookie); refresh == nil || refresh.MaxAge != -1 || refresh.Path != "/v1/auth/sessions" {
		t.Fatalf("logout must expire the refresh cookie on its original path: %+v", refresh)
	}
}

// 访问令牌门：/me、PATCH /me、密码修改、会话管理都要求访问 Cookie，缺失时不触碰 service。
func TestAccessCookieGuardedEndpointsRejectMissingCookie(t *testing.T) {
	service := &recordingService{}
	router := newAuthRouter(t, service)
	cases := []struct {
		method, target, body string
	}{
		{http.MethodGet, "/v1/auth/me", ""},
		{http.MethodPatch, "/v1/auth/me", `{"display_name":"x"}`},
		{http.MethodPost, "/v1/auth/password-challenges", ""},
		{http.MethodPut, "/v1/auth/password", `{"challenge_id":"c","code":"1","new_password":"p"}`},
		{http.MethodGet, "/v1/auth/sessions", ""},
		{http.MethodDelete, "/v1/auth/sessions/sid-9", ""},
		{http.MethodPost, "/v1/auth/sessions/logout-all", ""},
		{http.MethodPost, "/v1/auth/sessions/logout-others", ""},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, jsonRequest(t, tc.method, tc.target, tc.body))
		if rec.Code != http.StatusUnauthorized || decodeErrorCode(t, rec) != serviceauth.CodeSessionInvalid {
			t.Fatalf("%s %s: status=%d body=%s", tc.method, tc.target, rec.Code, rec.Body.String())
		}
	}
	if len(service.calls) != 0 {
		t.Fatalf("guarded endpoints must not reach the service without a cookie: %v", service.calls)
	}
}

func TestSessionManagementForwardsAccessTokenAndFormatsTimes(t *testing.T) {
	created := time.Date(2026, 9, 1, 8, 0, 0, 0, time.FixedZone("CST", 8*3600))
	service := &recordingService{sessions: []serviceauth.SessionEntry{
		{SessionInfo: serviceauth.SessionInfo{SID: "sid-1", IP: "198.51.100.7", UserAgent: "ua", CreatedAt: created, LastSeenAt: created.Add(time.Hour)}, Current: true},
		{SessionInfo: serviceauth.SessionInfo{SID: "sid-2"}},
	}}
	router := newAuthRouter(t, service)

	req := jsonRequest(t, http.MethodGet, "/v1/auth/sessions", "")
	req.AddCookie(&http.Cookie{Name: accessCookie, Value: "access-1"})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || service.lastArgs[0] != "access-1" {
		t.Fatalf("list sessions: status=%d args=%v", rec.Code, service.lastArgs)
	}
	var body struct {
		Data struct {
			Items []struct {
				ID         string `json:"id"`
				CreatedAt  string `json:"created_at"`
				LastSeenAt string `json:"last_seen_at"`
				Current    bool   `json:"current"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode sessions: %v", err)
	}
	if len(body.Data.Items) != 2 || !body.Data.Items[0].Current || body.Data.Items[0].CreatedAt != "2026-09-01T00:00:00Z" || body.Data.Items[1].CreatedAt != "" {
		t.Fatalf("sessions body = %+v", body.Data.Items)
	}

	req = jsonRequest(t, http.MethodDelete, "/v1/auth/sessions/sid-2", "")
	req.AddCookie(&http.Cookie{Name: accessCookie, Value: "access-1"})
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || service.calls[len(service.calls)-1] != "RevokeSession" || service.lastArgs[1] != "sid-2" {
		t.Fatalf("revoke session: status=%d calls=%v args=%v", rec.Code, service.calls, service.lastArgs)
	}
}

// 验证码邮件语言由 Accept-Language 推断（zh* → zh，其余 → en），来源 IP 沿可信代理链回溯。
func TestEmailChallengeForwardsLocaleAndTrustedClientIP(t *testing.T) {
	service := &recordingService{}
	router := newAuthRouter(t, service, "203.0.113.0/24")

	req := jsonRequest(t, http.MethodPost, "/v1/auth/email-challenges", `{"email":"user@example.test","purpose":"register"}`)
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	req.Header.Set("X-Forwarded-For", "198.51.100.23")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if service.lastArgs[0] != "user@example.test" || service.lastArgs[1] != "register" || service.lastArgs[2] != "198.51.100.23" || service.lastArgs[3] != "zh" {
		t.Fatalf("challenge args = %v", service.lastArgs)
	}
	if !strings.Contains(rec.Body.String(), `"challenge_id":"ch-1"`) {
		t.Fatalf("challenge body = %s", rec.Body.String())
	}

	req = jsonRequest(t, http.MethodPost, "/v1/auth/email-challenges", `{"email":"user@example.test","purpose":"register"}`)
	req.Header.Set("Accept-Language", "fr-FR")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if service.lastArgs[3] != "en" {
		t.Fatalf("unsupported language must fall back to en, got %v", service.lastArgs)
	}
}

func TestPasswordResetClearsCookiesAfterSuccess(t *testing.T) {
	service := &recordingService{}
	router := newAuthRouter(t, service)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, jsonRequest(t, http.MethodPost, "/v1/auth/password-resets", `{"reset_token":"reset-1","new_password":"new-secret"}`))
	if rec.Code != http.StatusOK || service.calls[0] != "ResetPassword" || service.lastArgs[0] != "reset-1" {
		t.Fatalf("reset: status=%d calls=%v args=%v", rec.Code, service.calls, service.lastArgs)
	}
	// 重置口令后旧会话不可继续使用：两把 Cookie 立即过期。
	for _, name := range []string{accessCookie, refreshCookie} {
		if cookie := cookieByName(t, rec, name); cookie == nil || cookie.MaxAge != -1 {
			t.Fatalf("password reset must expire %s: %+v", name, cookie)
		}
	}
}

// RequireAuth 中间件：无 Cookie 401、service 拒绝原样透出、通过后主体进入上下文。
func TestRequireAuthMiddleware(t *testing.T) {
	service := &recordingService{}
	var seen serviceauth.Principal
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = consoleauth.PrincipalFromContext(r.Context())
		w.WriteHeader(http.StatusNoContent)
	})
	guarded := consoleauth.RequireAuth(service, transport.NewErrorWriter(zap.NewNop()))(next)

	rec := httptest.NewRecorder()
	guarded.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/protected", nil))
	if rec.Code != http.StatusUnauthorized || decodeErrorCode(t, rec) != serviceauth.CodeSessionInvalid {
		t.Fatalf("missing cookie: status=%d body=%s", rec.Code, rec.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/protected", nil)
	req.AddCookie(&http.Cookie{Name: accessCookie, Value: "access-1"})
	rec = httptest.NewRecorder()
	guarded.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent || seen.UserID != 42 || seen.UID != userUID {
		t.Fatalf("authenticated request: status=%d principal=%+v", rec.Code, seen)
	}

	service.principalErr = &consoleservice.Error{Code: serviceauth.CodeSessionInvalid, Message: "expired", Status: http.StatusUnauthorized}
	rec = httptest.NewRecorder()
	guarded.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("service rejection must propagate, got %d", rec.Code)
	}
	if _, ok := consoleauth.PrincipalFromContext(context.Background()); ok {
		t.Fatal("empty context must not yield a principal")
	}
}
