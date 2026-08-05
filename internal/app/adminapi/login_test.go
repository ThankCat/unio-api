package adminapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/core/adminauth"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

type fakeLoginAuthenticator struct {
	err   error
	calls int
}

func (a *fakeLoginAuthenticator) AuthenticateCredentials(context.Context, string, string) (*adminauth.Principal, error) {
	a.calls++
	if a.err != nil {
		return nil, a.err
	}
	return &adminauth.Principal{Subject: adminauth.SubjectAdmin}, nil
}

type fakeLoginLimiter struct {
	allowed      bool
	retryAfter   time.Duration
	allowErr     error
	resetErr     error
	allowCalls   int
	resetCalls   int
	lastUsername string
	lastRemote   string
}

func (l *fakeLoginLimiter) Allow(_ context.Context, username, remoteAddr string) (bool, time.Duration, error) {
	l.allowCalls++
	l.lastUsername = username
	l.lastRemote = remoteAddr
	return l.allowed, l.retryAfter, l.allowErr
}

func (l *fakeLoginLimiter) Reset(_ context.Context, username, remoteAddr string) error {
	l.resetCalls++
	l.lastUsername = username
	l.lastRemote = remoteAddr
	return l.resetErr
}

type fakeLoginSessions struct {
	issueErr   error
	issueCalls int
}

func (s *fakeLoginSessions) Issue(context.Context) (string, error) {
	s.issueCalls++
	if s.issueErr != nil {
		return "", s.issueErr
	}
	return "session-token", nil
}

func (*fakeLoginSessions) Revoke(context.Context, string) error { return nil }

func performLoginRequest(t *testing.T, handler http.Handler) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(loginRequest{Username: " admin ", Password: "secret"})
	if err != nil {
		t.Fatalf("marshal login request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/v1/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "192.0.2.10:1234"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestLoginInvalidCredentialsRemainUnauthorized(t *testing.T) {
	authenticator := &fakeLoginAuthenticator{err: adminauth.ErrInvalidCredentials}
	limiter := &fakeLoginLimiter{allowed: true}
	sessions := &fakeLoginSessions{}

	rec := performLoginRequest(t, handleLogin(authenticator, limiter, sessions, 3600))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if limiter.allowCalls != 1 || limiter.resetCalls != 0 || sessions.issueCalls != 0 {
		t.Fatalf("calls = allow %d reset %d issue %d", limiter.allowCalls, limiter.resetCalls, sessions.issueCalls)
	}
}

func TestLoginRateLimitedReturnsRetryAfterWithoutCheckingPassword(t *testing.T) {
	authenticator := &fakeLoginAuthenticator{}
	limiter := &fakeLoginLimiter{allowed: false, retryAfter: 90*time.Second + time.Millisecond}
	sessions := &fakeLoginSessions{}

	rec := performLoginRequest(t, handleLogin(authenticator, limiter, sessions, 3600))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Retry-After"); got != "91" {
		t.Fatalf("Retry-After = %q", got)
	}
	if authenticator.calls != 0 || sessions.issueCalls != 0 {
		t.Fatalf("calls = authenticate %d issue %d", authenticator.calls, sessions.issueCalls)
	}
}

func TestLoginSuccessResetsLimiterBeforeIssuingSession(t *testing.T) {
	authenticator := &fakeLoginAuthenticator{}
	limiter := &fakeLoginLimiter{allowed: true}
	sessions := &fakeLoginSessions{}

	rec := performLoginRequest(t, handleLogin(authenticator, limiter, sessions, 3600))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if limiter.resetCalls != 1 || sessions.issueCalls != 1 {
		t.Fatalf("calls = reset %d issue %d", limiter.resetCalls, sessions.issueCalls)
	}
	if limiter.lastUsername != "admin" || limiter.lastRemote != "192.0.2.10:1234" {
		t.Fatalf("limiter subject = %q %q", limiter.lastUsername, limiter.lastRemote)
	}
}

func TestLoginLimiterStoreFailureReturnsServiceUnavailable(t *testing.T) {
	storeErr := failure.New(failure.CodeAdminAuthLoginRateLimitStoreFailed, failure.WithMessage("redis down"))
	authenticator := &fakeLoginAuthenticator{}
	limiter := &fakeLoginLimiter{allowErr: storeErr}
	sessions := &fakeLoginSessions{}

	rec := performLoginRequest(t, handleLogin(authenticator, limiter, sessions, 3600))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if authenticator.calls != 0 || sessions.issueCalls != 0 {
		t.Fatalf("calls = authenticate %d issue %d", authenticator.calls, sessions.issueCalls)
	}
}

func TestLoginResetFailureDoesNotIssueSession(t *testing.T) {
	authenticator := &fakeLoginAuthenticator{}
	limiter := &fakeLoginLimiter{
		allowed:  true,
		resetErr: failure.New(failure.CodeAdminAuthLoginRateLimitStoreFailed, failure.WithMessage("redis down")),
	}
	sessions := &fakeLoginSessions{issueErr: errors.New("must not be called")}

	rec := performLoginRequest(t, handleLogin(authenticator, limiter, sessions, 3600))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if sessions.issueCalls != 0 {
		t.Fatalf("issue calls = %d", sessions.issueCalls)
	}
}
