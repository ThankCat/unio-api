package httpmw

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
	gatewaylogging "github.com/ThankCat/unio-gateway/internal/platform/logging"
	"github.com/ThankCat/unio-gateway/internal/platform/observability/logfields"
)

type gatewayStatusRecorder struct {
	http.ResponseWriter
	status        int
	wroteHeader   bool
	responseBytes int64
}

func (r *gatewayStatusRecorder) WriteHeader(status int) {
	if r.wroteHeader {
		return
	}
	r.status = status
	r.wroteHeader = true
	r.ResponseWriter.WriteHeader(status)
}

func (r *gatewayStatusRecorder) Write(payload []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	n, err := r.ResponseWriter.Write(payload)
	r.responseBytes += int64(n)
	return n, err
}

func (r *gatewayStatusRecorder) Flush() {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (r *gatewayStatusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// GatewayLogger 记录 Gateway 请求入口 DEBUG 和唯一的请求终态摘要。
func GatewayLogger(logger *zap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			startedAt := time.Now()
			startedFields := requestLogFields(r)
			startedFields = append(startedFields,
				zap.String("method", r.Method),
				zap.String("path", r.URL.Path),
				zap.String("protocol", requestProtocol(r.URL.Path)),
				zap.String("client_ip", httpx.ClientIP(r.Context())),
				zap.String("user_agent", r.UserAgent()),
			)
			gatewaylogging.Debug(logger, "http", "request", "request started", startedFields...)

			recorder := &gatewayStatusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(recorder, r)

			fields := requestLogFields(r)
			fields = append(fields,
				zap.String("method", r.Method),
				zap.String("path", r.URL.Path),
				zap.String("route_pattern", routePattern(r)),
				zap.String("protocol", requestProtocol(r.URL.Path)),
				zap.Int("status_code", recorder.status),
				zap.Int64("duration_ms", time.Since(startedAt).Milliseconds()),
				zap.Int64("response_bytes", recorder.responseBytes),
			)

			if isSuccessfulProbe(r.URL.Path, recorder.status) {
				gatewaylogging.Debug(logger, "http", "request", "request completed", fields...)
				return
			}
			switch gatewayRequestLevel(r, recorder.status, fields) {
			case "error":
				gatewaylogging.Error(logger, "http", "request", "request completed", fields...)
			case "warning":
				gatewaylogging.Warn(logger, "http", "request", "request completed", fields...)
			default:
				gatewaylogging.Info(logger, "http", "request", "request completed", fields...)
			}
		})
	}
}

func GatewayRecoverer(logger *zap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				panicValue := recover()
				if panicValue == nil {
					return
				}
				fields := requestLogFields(r)
				fields = append(fields,
					zap.String("method", r.Method),
					zap.String("path", r.URL.Path),
					zap.String("panic_type", fmt.Sprintf("%T", panicValue)),
					zap.String("panic_message", safePanicMessage(panicValue)),
				)
				if propagated, ok := logfields.FromContext(r.Context()); ok {
					propagated.SetCompletion("error", "internal_error")
				}
				gatewaylogging.Error(logger, "http", "request", "request panic recovered", fields...)
				_ = httpx.WriteError(w, http.StatusInternalServerError, "internal_error", "internal server error")
			}()
			next.ServeHTTP(w, r)
		})
	}
}

func requestLogFields(r *http.Request) []zap.Field {
	if fields, ok := logfields.FromContext(r.Context()); ok {
		return fields.ZapFields()
	}
	return []zap.Field{zap.String("trace_id", httpx.RequestID(r.Context()))}
}

func gatewayRequestLevel(r *http.Request, status int, fields []zap.Field) string {
	if propagated, ok := logfields.FromContext(r.Context()); ok {
		if level := propagated.CompletionLevel(); level != "" {
			return level
		}
	}
	if status < http.StatusInternalServerError {
		if status == http.StatusTooManyRequests {
			return "warning"
		}
		return "info"
	}
	for _, field := range fields {
		if field.Key == "request_id" {
			return "warning"
		}
	}
	return "error"
}

func routePattern(r *http.Request) string {
	if routeContext := chi.RouteContext(r.Context()); routeContext != nil {
		return routeContext.RoutePattern()
	}
	return ""
}

func requestProtocol(path string) string {
	if strings.HasPrefix(path, "/internal/") {
		return "http"
	}
	if strings.HasSuffix(path, "/messages") {
		return "anthropic"
	}
	if path == "/models" || path == "/chat/completions" || path == "/responses" || strings.HasPrefix(path, "/responses/") {
		return "openai"
	}
	if strings.HasPrefix(path, "/v1") || strings.Contains(path, "/v1/") {
		return "openai"
	}
	return "http"
}

func isSuccessfulProbe(path string, status int) bool {
	if status >= http.StatusBadRequest {
		return false
	}
	return path == "/healthz" || path == "/readyz" || path == "/metrics" || strings.HasPrefix(path, "/internal/v1/")
}

func safePanicMessage(value any) string {
	message := fmt.Sprint(value)
	message = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, message)
	if len(message) > 500 {
		message = message[:500]
	}
	return message
}
