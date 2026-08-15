package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ThankCat/unio-gateway/internal/core/auth"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/requestadmission"
)

type positiveBalanceCheckerStub struct {
	positive bool
	err      error
	calls    int
	userID   int64
	onCheck  func()
}

type positiveBalanceMetricsSpy struct {
	protocol string
	reason   string
	calls    int
}

func (s *positiveBalanceMetricsSpy) IncRequestRejected(protocol string, reason string) {
	s.calls++
	s.protocol = protocol
	s.reason = reason
}

func (s *positiveBalanceCheckerStub) HasPositiveAvailableBalance(_ context.Context, userID int64) (bool, error) {
	s.calls++
	s.userID = userID
	if s.onCheck != nil {
		s.onCheck()
	}
	return s.positive, s.err
}

func TestPositiveBalanceGateProtocolResponses(t *testing.T) {
	tests := []struct {
		name          string
		protocol      RequestAdmissionProtocol
		positive      bool
		checkerErr    error
		withPrincipal bool
		wantStatus    int
		wantCode      string
		wantType      string
		wantNext      bool
	}{
		{name: "positive passes", protocol: RequestAdmissionOpenAI, positive: true, withPrincipal: true, wantStatus: http.StatusNoContent, wantNext: true},
		{name: "openai zero balance", protocol: RequestAdmissionOpenAI, withPrincipal: true, wantStatus: http.StatusPaymentRequired, wantCode: "insufficient_quota", wantType: "insufficient_quota"},
		{name: "anthropic zero balance", protocol: RequestAdmissionAnthropic, withPrincipal: true, wantStatus: http.StatusPaymentRequired, wantType: "invalid_request_error"},
		{name: "openai store failure", protocol: RequestAdmissionOpenAI, checkerErr: failure.New(failure.CodeLedgerStoreFailed), withPrincipal: true, wantStatus: http.StatusServiceUnavailable, wantCode: "service_unavailable", wantType: "api_error"},
		{name: "anthropic store failure", protocol: RequestAdmissionAnthropic, checkerErr: failure.New(failure.CodeLedgerStoreFailed), withPrincipal: true, wantStatus: http.StatusServiceUnavailable, wantType: "api_error"},
		{name: "missing principal", protocol: RequestAdmissionOpenAI, positive: true, wantStatus: http.StatusServiceUnavailable, wantCode: "service_unavailable", wantType: "api_error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checker := &positiveBalanceCheckerStub{positive: tt.positive, err: tt.checkerErr}
			nextCalled := false
			handler := PositiveBalanceGate(checker, PositiveBalanceOptions{Protocol: tt.protocol})(
				http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					nextCalled = true
					w.WriteHeader(http.StatusNoContent)
				}),
			)

			req := httptest.NewRequest(http.MethodPost, "/v1/generate", nil)
			if tt.withPrincipal {
				req = req.WithContext(auth.ContextWithAPIKeyPrincipal(req.Context(), &auth.APIKeyPrincipal{UserID: 42, APIKeyID: 7}))
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status=%d, want %d body=%s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if nextCalled != tt.wantNext {
				t.Fatalf("next called=%v, want %v", nextCalled, tt.wantNext)
			}
			if tt.withPrincipal {
				if checker.calls != 1 || checker.userID != 42 {
					t.Fatalf("checker calls=%d user_id=%d", checker.calls, checker.userID)
				}
			} else if checker.calls != 0 {
				t.Fatalf("missing principal called checker %d times", checker.calls)
			}

			if tt.wantCode != "" || tt.wantType != "" {
				var body struct {
					Error struct {
						Code string `json:"code"`
						Type string `json:"type"`
					} `json:"error"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
					t.Fatalf("decode response: %v", err)
				}
				if body.Error.Code != tt.wantCode || body.Error.Type != tt.wantType {
					t.Fatalf("error code=%q type=%q, want code=%q type=%q", body.Error.Code, body.Error.Type, tt.wantCode, tt.wantType)
				}
			}
		})
	}
}

func TestPositiveBalanceGateRunsAfterAdmissionAndFinalizesRejection(t *testing.T) {
	routeID := int64(9)
	events := make([]string, 0, 4)
	session := &orderedAdmissionSession{events: &events}
	acquirer := &orderedAdmissionAcquirer{events: &events, session: session}
	checker := &positiveBalanceCheckerStub{onCheck: func() { events = append(events, "balance") }}

	handler := RequestAdmission(acquirer, RequestAdmissionOptions{
		Scope: "/v1/chat/completions", Protocol: RequestAdmissionOpenAI,
	})(PositiveBalanceGate(checker, PositiveBalanceOptions{Protocol: RequestAdmissionOpenAI})(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			events = append(events, "handler")
		}),
	))
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req = req.WithContext(auth.ContextWithAPIKeyPrincipal(req.Context(), &auth.APIKeyPrincipal{UserID: 42, RouteID: &routeID}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	want := []string{"admission", "balance", "stop", "finalize"}
	if len(events) != len(want) {
		t.Fatalf("events=%v, want %v", events, want)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Fatalf("events=%v, want %v", events, want)
		}
	}
}

func TestPositiveBalanceGateRecordsBoundedRejectionMetric(t *testing.T) {
	checker := &positiveBalanceCheckerStub{}
	metrics := &positiveBalanceMetricsSpy{}
	handler := PositiveBalanceGate(checker, PositiveBalanceOptions{
		Protocol: RequestAdmissionAnthropic,
		Metrics:  metrics,
	})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("zero balance reached downstream handler")
	}))
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req = req.WithContext(auth.ContextWithAPIKeyPrincipal(req.Context(), &auth.APIKeyPrincipal{UserID: 42}))
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if metrics.calls != 1 || metrics.protocol != "anthropic" || metrics.reason != "insufficient_balance" {
		t.Fatalf("metric calls=%d protocol=%q reason=%q", metrics.calls, metrics.protocol, metrics.reason)
	}
}

type orderedAdmissionAcquirer struct {
	events  *[]string
	session *orderedAdmissionSession
}

func (a *orderedAdmissionAcquirer) Acquire(context.Context, requestadmission.Identity) (requestadmission.AcquireResult, error) {
	*a.events = append(*a.events, "admission")
	return requestadmission.AcquireResult{Outcome: breakerstore.RequestAllowed, Session: a.session}, nil
}

type orderedAdmissionSession struct {
	events  *[]string
	request requestSessionStub
}

func (s *orderedAdmissionSession) Request() requestadmission.RequestSession { return &s.request }
func (s *orderedAdmissionSession) StopRenewer() {
	*s.events = append(*s.events, "stop")
}
func (s *orderedAdmissionSession) Finalize(context.Context) error {
	*s.events = append(*s.events, "finalize")
	return nil
}

func TestPositiveBalanceGateNilCheckerPassesThrough(t *testing.T) {
	nextCalled := false
	handler := PositiveBalanceGate(nil, PositiveBalanceOptions{})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusNoContent)
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/generate", nil))
	if !nextCalled || rec.Code != http.StatusNoContent {
		t.Fatalf("nil checker pass-through: called=%v status=%d", nextCalled, rec.Code)
	}
}
