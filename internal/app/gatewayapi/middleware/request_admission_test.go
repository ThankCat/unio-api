package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/ThankCat/unio-gateway/internal/core/auth"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/requestadmission"
)

type acquiredSessionStub struct {
	mu             sync.Mutex
	usage          usageSessionStub
	finalizeCalls  int
	handlerDone    bool
	renewerStopped bool
}

func (s *acquiredSessionStub) Usage() requestadmission.UsageSession { return &s.usage }

func (s *acquiredSessionStub) StopRenewer() {
	s.mu.Lock()
	s.renewerStopped = true
	s.mu.Unlock()
}

func (s *acquiredSessionStub) Finalize(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.handlerDone {
		panic("finalize ran before handler returned")
	}
	if !s.renewerStopped {
		panic("finalize ran before renewer stopped")
	}
	s.finalizeCalls++
	return nil
}

type usageSessionStub struct{}

func (*usageSessionStub) Reserve(context.Context, int64) error { return nil }
func (*usageSessionStub) PublishAuthoritativeUsage(int64) bool { return true }

type acquirerStub struct {
	mu       sync.Mutex
	identity requestadmission.Identity
	calls    int
	result   requestadmission.AcquireResult
	err      error
}

func (a *acquirerStub) Acquire(_ context.Context, identity requestadmission.Identity) (requestadmission.AcquireResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.identity = identity
	a.calls++
	return a.result, a.err
}

func TestRequestAdmissionCurrentRouteMatrix(t *testing.T) {
	routeID := int64(9)
	principal := &auth.APIKeyPrincipal{APIKeyID: 1, UserID: 8, RouteID: &routeID}
	tests := []struct {
		method   string
		path     string
		scope    string
		protocol RequestAdmissionProtocol
	}{
		{http.MethodGet, "/v1/models", "/v1/models", RequestAdmissionOpenAI},
		{http.MethodPost, "/v1/chat/completions", "/v1/chat/completions", RequestAdmissionOpenAI},
		{http.MethodPost, "/v1/responses", "/v1/responses", RequestAdmissionOpenAI},
		{http.MethodPost, "/v1/responses/compact", "/v1/responses/compact", RequestAdmissionOpenAI},
		{http.MethodPost, "/v1/responses/input_tokens", "/v1/responses/input_tokens", RequestAdmissionOpenAI},
		{http.MethodGet, "/v1/responses/resp_1", "/v1/responses/{response_id}", RequestAdmissionOpenAI},
		{http.MethodDelete, "/v1/responses/resp_1", "/v1/responses/{response_id}", RequestAdmissionOpenAI},
		{http.MethodGet, "/v1/responses/resp_1/input_items", "/v1/responses/{response_id}/input_items", RequestAdmissionOpenAI},
		{http.MethodPost, "/v1/responses/resp_1/cancel", "/v1/responses/{response_id}/cancel", RequestAdmissionOpenAI},
		{http.MethodPost, "/v1/messages", "/v1/messages", RequestAdmissionAnthropic},
	}

	for _, tt := range tests {
		t.Run(tt.method+" "+tt.scope, func(t *testing.T) {
			session := &acquiredSessionStub{}
			acquirer := &acquirerStub{result: requestadmission.AcquireResult{
				Outcome: breakerstore.RequestAllowed,
				Session: session,
			}}
			handler := RequestAdmission(acquirer, RequestAdmissionOptions{Scope: tt.scope, Protocol: tt.protocol})(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if _, ok := requestadmission.UsageSessionFromContext(r.Context()); !ok {
						t.Fatal("usage session is missing from handler context")
					}
					session.mu.Lock()
					session.handlerDone = true
					session.mu.Unlock()
					w.WriteHeader(http.StatusNoContent)
				}),
			)
			req := httptest.NewRequest(tt.method, tt.path, nil)
			req = req.WithContext(auth.ContextWithAPIKeyPrincipal(req.Context(), principal))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusNoContent {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if acquirer.calls != 1 || acquirer.identity.Scope != tt.method+" "+tt.scope || acquirer.identity.RouteID != 9 || acquirer.identity.UserID != 8 {
				t.Fatalf("acquire calls=%d identity=%+v", acquirer.calls, acquirer.identity)
			}
			if session.finalizeCalls != 1 {
				t.Fatalf("finish calls=%d", session.finalizeCalls)
			}
		})
	}
}

func TestRequestAdmissionDeniedMapsProtocolAndSkipsFinish(t *testing.T) {
	routeID := int64(9)
	principal := &auth.APIKeyPrincipal{UserID: 8, RouteID: &routeID}
	tests := []struct {
		name       string
		protocol   RequestAdmissionProtocol
		outcome    breakerstore.RequestAdmissionOutcome
		wantStatus int
	}{
		{"openai limited", RequestAdmissionOpenAI, breakerstore.RequestLimited, http.StatusTooManyRequests},
		{"anthropic limited", RequestAdmissionAnthropic, breakerstore.RequestLimited, http.StatusTooManyRequests},
		{"openai runtime", RequestAdmissionOpenAI, breakerstore.RequestRuntimeSyncReq, http.StatusServiceUnavailable},
		{"anthropic runtime", RequestAdmissionAnthropic, breakerstore.RequestRuntimeSyncReq, http.StatusServiceUnavailable},
		{"openai store unavailable", RequestAdmissionOpenAI, breakerstore.RequestStoreUnavailable, http.StatusServiceUnavailable},
		{"anthropic store unavailable", RequestAdmissionAnthropic, breakerstore.RequestStoreUnavailable, http.StatusServiceUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			acquirer := &acquirerStub{result: requestadmission.AcquireResult{Outcome: tt.outcome}}
			handler := RequestAdmission(acquirer, RequestAdmissionOptions{Scope: "/v1/messages", Protocol: tt.protocol})(
				http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("denied request reached handler") }),
			)
			req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			req = req.WithContext(auth.ContextWithAPIKeyPrincipal(req.Context(), principal))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestRequestAdmissionStoreFailureReturns503WithoutCallingHandler(t *testing.T) {
	routeID := int64(9)
	principal := &auth.APIKeyPrincipal{UserID: 8, RouteID: &routeID}
	acquirer := &acquirerStub{err: breakerstore.ErrStoreUnavailable}
	transportCalls := 0
	handler := RequestAdmission(acquirer, RequestAdmissionOptions{
		Scope: "/v1/chat/completions", Protocol: RequestAdmissionOpenAI,
	})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		transportCalls++
	}))
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req = req.WithContext(auth.ContextWithAPIKeyPrincipal(req.Context(), principal))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if transportCalls != 0 {
		t.Fatalf("store failure reached downstream transport %d times", transportCalls)
	}
}
