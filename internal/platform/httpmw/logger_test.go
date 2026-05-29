package httpmw

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/ThankCat/unio-api/internal/platform/observability/logfields"
)

// TestLoggerEmitsUnifiedFields 验证访问日志包含 correlation_id 和下游填充的统一字段。
func TestLoggerEmitsUnifiedFields(t *testing.T) {
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buf, nil))

	r := chi.NewRouter()
	r.Use(RequestID)
	r.Use(Logger(logger))
	r.Get("/v1/chat/completions", func(w http.ResponseWriter, req *http.Request) {
		// 模拟认证和 gateway 下游对同一 *Fields 的填充。
		logfields.SetIdentity(req.Context(), 7, 42, 100)
		logfields.SetRequestID(req.Context(), "req_abc")
		logfields.SetRoute(req.Context(), "openai/gpt-4.1", "9123", "123")
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)
	req.Header.Set("X-Request-ID", "corr-fixed")
	r.ServeHTTP(httptest.NewRecorder(), req)

	var entry map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &entry); err != nil {
		t.Fatalf("parse log entry: %v (raw=%s)", err, buf.String())
	}

	cases := map[string]any{
		"correlation_id": "corr-fixed",
		"request_id":     "req_abc",
		"user_id":        float64(7),
		"project_id":     float64(42),
		"api_key_id":     float64(100),
		"model":          "openai/gpt-4.1",
		"provider":       "9123",
		"channel":        "123",
		"status":         float64(http.StatusOK),
		"method":         http.MethodGet,
	}
	for key, want := range cases {
		if entry[key] != want {
			t.Errorf("log field %q: got %v, want %v", key, entry[key], want)
		}
	}
}

// TestLoggerDoesNotLogRequestBodyOrAuth 验证访问日志不包含请求体或鉴权头等敏感内容。
func TestLoggerDoesNotLogRequestBodyOrAuth(t *testing.T) {
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buf, nil))

	r := chi.NewRouter()
	r.Use(RequestID)
	r.Use(Logger(logger))
	r.Post("/v1/chat/completions", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"secret_prompt":"do not log"}`))
	req.Header.Set("Authorization", "Bearer unio_sk_super_secret")
	r.ServeHTTP(httptest.NewRecorder(), req)

	logged := buf.String()
	for _, forbidden := range []string{"secret_prompt", "do not log", "unio_sk_super_secret", "Bearer"} {
		if bytes.Contains([]byte(logged), []byte(forbidden)) {
			t.Errorf("access log must not contain %q, got: %s", forbidden, logged)
		}
	}
}
