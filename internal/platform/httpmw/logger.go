package httpmw

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
	"github.com/ThankCat/unio-gateway/internal/platform/observability/logfields"
)

// statusRecorder 记录 handler 写出的 HTTP 状态码，供请求日志使用。
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

// WriteHeader 记录第一次写出的 HTTP 状态码，并保持 net/http 的首次写入语义。
func (r *statusRecorder) WriteHeader(status int) {
	if r.wroteHeader {
		return
	}

	r.status = status
	r.wroteHeader = true
	r.ResponseWriter.WriteHeader(status)
}

// Flush 将流式响应的 flush 能力转发给底层 ResponseWriter。
func (r *statusRecorder) Flush() {
	f, ok := r.ResponseWriter.(http.Flusher)
	if !ok {
		return
	}

	f.Flush()
}

// Logger 记录每个 HTTP 请求的基础信息，包括方法、路径、状态码、耗时和请求 ID。
func Logger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			recorder := &statusRecorder{
				ResponseWriter: w,
				status:         http.StatusOK,
			}

			next.ServeHTTP(recorder, r)

			// 基础访问字段只包含方法、路径、状态码和耗时；
			// 不记录请求体、用户 prompt、API key 或上游 Authorization。
			args := []any{
				"method", r.Method,
				"path", r.URL.Path,
				"status", recorder.status,
				"duration_ms", time.Since(start).Milliseconds(),
			}

			// 统一结构化字段（correlation_id、request_id、user/project/api_key、model/provider/channel）
			// 由下游中间件和 gateway 填充到同一个 *logfields.Fields。
			if fields, ok := logfields.FromContext(r.Context()); ok {
				args = append(args, fields.Attrs()...)
			} else {
				args = append(args, "correlation_id", httpx.RequestID(r.Context()))
			}

			logger.InfoContext(r.Context(), "http request", args...)
		})
	}
}
