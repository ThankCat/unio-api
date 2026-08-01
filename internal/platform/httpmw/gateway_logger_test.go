package httpmw

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/ThankCat/unio-gateway/internal/platform/observability/logfields"
)

func TestGatewayRequestProtocol(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{path: "/internal/v1/logging/status", want: "http"},
		{path: "/healthz", want: "http"},
		{path: "/v1/responses", want: "openai"},
		{path: "/v1/chat/completions", want: "openai"},
		{path: "/v1/messages", want: "anthropic"},
	}
	for _, test := range tests {
		if got := requestProtocol(test.path); got != test.want {
			t.Errorf("requestProtocol(%q) = %q, want %q", test.path, got, test.want)
		}
	}
}

func TestGatewayRecovererMarksRequestSummaryAsError(t *testing.T) {
	core, observed := observer.New(zapcore.DebugLevel)
	logger := zap.New(core)
	handler := RequestID(GatewayLogger(logger)(GatewayRecoverer(logger)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		logfields.SetRequestID(r.Context(), "req_panic")
		panic("boom")
	}))))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", recorder.Code)
	}

	completed := observed.FilterMessage("request completed").All()
	if len(completed) != 1 || completed[0].Level != zapcore.ErrorLevel {
		t.Fatalf("completed logs = %+v", completed)
	}
}
