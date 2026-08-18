package ingresslog_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/ThankCat/unio-gateway/internal/app/gatewayapi/ingresslog"
	"github.com/ThankCat/unio-gateway/internal/platform/httpmw"
	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
)

func TestRecordInvalidJSONEnrichesSingleWarningCompletionLog(t *testing.T) {
	core, observed := observer.New(zapcore.DebugLevel)
	logger := zap.New(core)
	type requestBody struct {
		Value string `json:"value"`
	}

	handler := httpmw.RequestID(httpmw.GatewayLogger(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body requestBody
		err := httpx.DecodeJSON(w, r, &body)
		if err == nil {
			t.Fatal("expected invalid JSON")
		}
		ingresslog.RecordInvalidJSON(r, err)
		_ = httpx.WriteOpenAIError(w, http.StatusBadRequest, "invalid_request", "invalid json body", "invalid_request_error", nil)
	})))

	requestBodyJSON := `{"value":123,"prompt":"must not be logged"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(requestBodyJSON))
	req.Header.Set("Content-Encoding", "identity")
	req.Header.Set("User-Agent", "Codex Desktop/0.148 "+strings.Repeat("x", 300))
	req.TransferEncoding = []string{"chunked"}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", recorder.Code)
	}
	completed := observed.FilterMessage("request completed").All()
	if len(completed) != 1 {
		t.Fatalf("completed logs=%d, want 1", len(completed))
	}
	if completed[0].Level != zapcore.WarnLevel {
		t.Fatalf("completion level=%s, want warning", completed[0].Level)
	}
	fields := completed[0].ContextMap()
	want := map[string]any{
		"error_code":              "http_invalid_json_body",
		"rejection_reason":        "invalid_json",
		"request_body_error_kind": "type_mismatch",
		"decode_error_kind":       "type_mismatch",
		"json_field":              "value",
		"content_length":          int64(len(requestBodyJSON)),
		"body_bytes_read":         int64(len(requestBodyJSON)),
		"body_completion_status":  "complete",
		"content_encoding":        "identity",
		"transfer_encoding":       "chunked",
		"http_version":            "HTTP/1.1",
	}
	for key, value := range want {
		if fields[key] != value {
			t.Errorf("field %s=%v, want %v", key, fields[key], value)
		}
	}
	userAgent, _ := fields["user_agent"].(string)
	if utf8.RuneCountInString(userAgent) != 256 {
		t.Fatalf("user_agent rune count=%d, want 256", utf8.RuneCountInString(userAgent))
	}
	for _, entry := range observed.All() {
		for key, value := range entry.ContextMap() {
			if strings.Contains(key, "must not be logged") || strings.Contains(valueAsString(value), "must not be logged") {
				t.Fatalf("log leaked request body in field %q", key)
			}
		}
	}
}

func TestRecordRequestBodyFailureLogsTimeoutWithoutBodyContent(t *testing.T) {
	core, observed := observer.New(zapcore.DebugLevel)
	logger := zap.New(core)

	handler := httpmw.RequestID(httpmw.GatewayLogger(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Use a real diagnostic-producing error from DecodeJSON rather than logging a fabricated body.
		reader := &timeoutReader{}
		decodeRequest := httptest.NewRequest(http.MethodPost, "/v1/responses", reader).WithContext(r.Context())
		decodeRequest.ContentLength = 128
		var body map[string]any
		err := httpx.DecodeJSONWithLimit(w, decodeRequest, &body, 1024)
		ingresslog.RecordRequestBodyFailure(decodeRequest, err)
		_ = httpx.WriteOpenAIError(w, http.StatusRequestTimeout, "request_body_timeout", "request body read timed out", "invalid_request_error", nil)
	})))

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader("must not be logged"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	completed := observed.FilterMessage("request completed").All()
	if len(completed) != 1 {
		t.Fatalf("completed logs=%d, want 1", len(completed))
	}
	fields := completed[0].ContextMap()
	if fields["error_code"] != "http_request_body_timeout" || fields["rejection_reason"] != "request_body_timeout" {
		t.Fatalf("fields=%#v", fields)
	}
	if fields["body_completion_status"] != "incomplete" || fields["request_body_error_kind"] != "read_timeout" {
		t.Fatalf("fields=%#v", fields)
	}
	for _, entry := range observed.All() {
		for key, value := range entry.ContextMap() {
			if strings.Contains(key, "must not be logged") || strings.Contains(valueAsString(value), "must not be logged") {
				t.Fatalf("log leaked request body in field %q", key)
			}
		}
	}
}

type timeoutReader struct{}

func (*timeoutReader) Read([]byte) (int, error) { return 0, timeoutLogError{} }

type timeoutLogError struct{}

func (timeoutLogError) Error() string   { return "timeout" }
func (timeoutLogError) Timeout() bool   { return true }
func (timeoutLogError) Temporary() bool { return true }

func valueAsString(value any) string {
	text, _ := value.(string)
	return text
}
