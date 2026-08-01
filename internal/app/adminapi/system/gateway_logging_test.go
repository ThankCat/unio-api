package system

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	admingatewaylogging "github.com/ThankCat/unio-gateway/internal/service/admin/gatewaylogging"
	"github.com/ThankCat/unio-gateway/internal/service/appsettings"
)

type gatewayLoggingServiceStub struct {
	snapshot admingatewaylogging.Snapshot
	err      error

	startDuration int
	startReason   string
	startUserID   int64
	stopUserID    int64
	logQuery      admingatewaylogging.LogQuery
	logs          admingatewaylogging.LogList
}

func (s *gatewayLoggingServiceStub) Get(context.Context) (admingatewaylogging.Snapshot, error) {
	return s.snapshot, s.err
}

func (s *gatewayLoggingServiceStub) Start(_ context.Context, duration int, reason string, userID int64) (admingatewaylogging.Snapshot, error) {
	s.startDuration = duration
	s.startReason = reason
	s.startUserID = userID
	return s.snapshot, s.err
}

func (s *gatewayLoggingServiceStub) Stop(_ context.Context, userID int64) (admingatewaylogging.Snapshot, error) {
	s.stopUserID = userID
	return s.snapshot, s.err
}

func (s *gatewayLoggingServiceStub) ListLogs(_ context.Context, query admingatewaylogging.LogQuery) (admingatewaylogging.LogList, error) {
	s.logQuery = query
	return s.logs, s.err
}

func TestGatewayLoggingHandlerLifecycle(t *testing.T) {
	expiresAt := time.Date(2026, time.August, 1, 9, 15, 0, 0, time.UTC)
	service := &gatewayLoggingServiceStub{snapshot: admingatewaylogging.Snapshot{
		Mode: "debug",
		Control: admingatewaylogging.ControlStatus{
			Active: true, SessionID: "dbg_test", ExpiresAt: &expiresAt, Revision: 4,
		},
	}}
	handler := &gatewayLoggingHandler{service: service}

	getRecorder := httptest.NewRecorder()
	handler.get(getRecorder, httptest.NewRequest(http.MethodGet, "/system/gateway-logging", nil))
	if getRecorder.Code != http.StatusOK || !strings.Contains(getRecorder.Body.String(), `"session_id":"dbg_test"`) {
		t.Fatalf("get status=%d body=%s", getRecorder.Code, getRecorder.Body.String())
	}

	startRecorder := httptest.NewRecorder()
	handler.start(startRecorder, httptest.NewRequest(
		http.MethodPut,
		"/system/gateway-logging/debug-session",
		strings.NewReader(`{"duration_minutes":30,"reason":"investigate TTFT"}`),
	))
	if startRecorder.Code != http.StatusOK {
		t.Fatalf("start status=%d body=%s", startRecorder.Code, startRecorder.Body.String())
	}
	if service.startDuration != 30 || service.startReason != "investigate TTFT" || service.startUserID != staticAdminOperatorID {
		t.Fatalf("start arguments duration=%d reason=%q user=%d", service.startDuration, service.startReason, service.startUserID)
	}

	stopRecorder := httptest.NewRecorder()
	handler.stop(stopRecorder, httptest.NewRequest(http.MethodDelete, "/system/gateway-logging/debug-session", nil))
	if stopRecorder.Code != http.StatusOK || service.stopUserID != staticAdminOperatorID {
		t.Fatalf("stop status=%d user=%d body=%s", stopRecorder.Code, service.stopUserID, stopRecorder.Body.String())
	}
}

func TestGatewayLoggingHandlerListsFilteredLogs(t *testing.T) {
	service := &gatewayLoggingServiceStub{logs: admingatewaylogging.LogList{
		Items: []admingatewaylogging.LogEntry{{ID: "log_1", Message: "request completed"}},
		Limit: 100,
	}}
	handler := &gatewayLoggingHandler{service: service}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodGet,
		"/system/gateway-logs?range=6h&level=warning&type=http&event=request&related_id=req_1&search=timeout&limit=50",
		nil,
	)
	handler.listLogs(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"id":"log_1"`) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if service.logQuery.Window != "6h" || service.logQuery.Level != "warning" ||
		service.logQuery.Type != "http" || service.logQuery.Event != "request" ||
		service.logQuery.RelatedID != "req_1" || service.logQuery.Search != "timeout" ||
		service.logQuery.Limit != 50 {
		t.Fatalf("query = %+v", service.logQuery)
	}
}

func TestGatewayLoggingHandlerRejectsInvalidLogLimit(t *testing.T) {
	handler := &gatewayLoggingHandler{service: &gatewayLoggingServiceStub{}}
	recorder := httptest.NewRecorder()
	handler.listLogs(recorder, httptest.NewRequest(http.MethodGet, "/system/gateway-logs?limit=many", nil))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestGatewayLoggingHandlerRejectsInvalidSessionRequest(t *testing.T) {
	service := &gatewayLoggingServiceStub{err: errors.Join(
		appsettings.ErrGatewayDebugRequestInvalid,
		errors.New("duration must be 5, 15, 30, or 60 minutes"),
	)}
	handler := &gatewayLoggingHandler{service: service}
	recorder := httptest.NewRecorder()
	handler.start(recorder, httptest.NewRequest(
		http.MethodPut,
		"/system/gateway-logging/debug-session",
		strings.NewReader(`{"duration_minutes":10,"reason":"invalid duration"}`),
	))

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error.Code != "admin_invalid_argument" {
		t.Fatalf("error code=%q body=%s", body.Error.Code, recorder.Body.String())
	}
}
