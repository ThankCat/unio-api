package httpmw

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/ThankCat/unio-gateway/internal/platform/observability/logfields"
)

func testJSONLogger(buf *bytes.Buffer) *zap.Logger {
	encoderCfg := zap.NewProductionEncoderConfig()
	encoderCfg.EncodeTime = zapcore.RFC3339TimeEncoder
	encoderCfg.TimeKey = "time"
	encoderCfg.MessageKey = "msg"
	encoderCfg.LevelKey = "level"
	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(encoderCfg),
		zapcore.AddSync(buf),
		zapcore.InfoLevel,
	)
	return zap.New(core)
}

// TestLoggerEmitsUnifiedFields 验证访问日志包含 correlation_id 和下游填充的统一字段。
func TestLoggerEmitsUnifiedFields(t *testing.T) {
	buf := &bytes.Buffer{}
	logger := testJSONLogger(buf)

	r := chi.NewRouter()
	r.Use(RequestID)
	r.Use(Logger(logger))
	r.Get("/v1/chat/completions", func(w http.ResponseWriter, req *http.Request) {
		// 模拟认证和 gateway 下游对同一 *Fields 的填充。
		logfields.SetIdentity(req.Context(), 7, 100)
		logfields.SetRequestID(req.Context(), "req_abc")
		logfields.SetModel(req.Context(), "openai/gpt-4.1")
		logfields.SetRouteID(req.Context(), 2)
		logfields.SetUpstreamAttempt(req.Context(), logfields.UpstreamAttempt{
			ModelID:    99,
			Router:     "default-route",
			ProviderID: 9123,
			Provider:   "openai",
			ChannelID:  123,
			Channel:    "main",
		})
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)
	req.Header.Set("X-Request-ID", "corr-fixed")
	r.ServeHTTP(httptest.NewRecorder(), req)

	var entry map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &entry); err != nil {
		t.Fatalf("parse log entry: %v (raw=%s)", err, buf.String())
	}

	wantMsg := fmt.Sprintf("%s | %s | %d | %dms", http.MethodGet, "/v1/chat/completions", http.StatusOK, 0)
	// duration_ms 非固定；只校验前缀顺序。
	msg, _ := entry["msg"].(string)
	if !strings.HasPrefix(msg, "GET | /v1/chat/completions | 200 | ") || !strings.HasSuffix(msg, "ms") {
		t.Fatalf("msg order: got %q, want prefix like %q", msg, wantMsg)
	}
	for _, key := range []string{"method", "path", "status", "duration_ms"} {
		if _, ok := entry[key]; ok {
			t.Errorf("field %q should be in msg, not JSON fields", key)
		}
	}

	cases := map[string]any{
		"correlation_id": "corr-fixed",
		"request_id":     "req_abc",
		"user_id":        float64(7),
		"api_key_id":     float64(100),
		"model":          "openai/gpt-4.1",
		"model_id":       float64(99),
		"route_id":       float64(2),
		"router":         "default-route",
		"provider_id":    float64(9123),
		"provider":       "openai",
		"channel_id":     float64(123),
		"channel":        "main",
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
	logger := testJSONLogger(buf)

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

// TestLoggerLevelByStatus 验证 5xx 升 ERROR，4xx/2xx 保持 INFO。
func TestLoggerLevelByStatus(t *testing.T) {
	cases := []struct {
		name   string
		status int
		level  string
	}{
		{name: "ok", status: http.StatusOK, level: "info"},
		{name: "too_many_requests", status: http.StatusTooManyRequests, level: "info"},
		{name: "bad_gateway", status: http.StatusBadGateway, level: "error"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf := &bytes.Buffer{}
			logger := testJSONLogger(buf)

			r := chi.NewRouter()
			r.Use(Logger(logger))
			r.Get("/x", func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
			})
			r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))

			var entry map[string]any
			if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &entry); err != nil {
				t.Fatalf("parse log entry: %v (raw=%s)", err, buf.String())
			}
			if got, _ := entry["level"].(string); got != tc.level {
				t.Fatalf("level: got %q, want %q (raw=%s)", got, tc.level, buf.String())
			}
			msg, _ := entry["msg"].(string)
			wantPrefix := fmt.Sprintf("GET | /x | %d | ", tc.status)
			if !strings.HasPrefix(msg, wantPrefix) {
				t.Fatalf("msg: got %q, want prefix %q", msg, wantPrefix)
			}
		})
	}
}
