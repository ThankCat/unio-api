package httpmw

import (
	"fmt"
	"net/http"
	"time"

	"go.uber.org/zap"

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
// 访问日志级别：5xx 为 ERROR，其余（含 4xx）为 INFO。
//
// 消息正文按 method → path → status → duration_ms 排序写出（与 console " | " 分隔对齐）；
// JSON 附加字段只保留 correlation / 身份 / 路由等上下文，不再重复这四项。
func Logger(logger *zap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			recorder := &statusRecorder{
				ResponseWriter: w,
				status:         http.StatusOK,
			}

			next.ServeHTTP(recorder, r)

			durationMs := time.Since(start).Milliseconds()
			// method | path | status | duration_ms —— 固定顺序，便于扫读。
			msg := fmt.Sprintf("%s | %s | %d | %dms", r.Method, r.URL.Path, recorder.status, durationMs)

			// 不记录请求体、用户 prompt、API key 或上游 Authorization。
			var fields []zap.Field
			if lf, ok := logfields.FromContext(r.Context()); ok {
				fields = lf.ZapFields()
			} else {
				fields = []zap.Field{zap.String("correlation_id", httpx.RequestID(r.Context()))}
			}

			if recorder.status >= http.StatusInternalServerError {
				logger.Error(msg, fields...)
			} else {
				logger.Info(msg, fields...)
			}
		})
	}
}
