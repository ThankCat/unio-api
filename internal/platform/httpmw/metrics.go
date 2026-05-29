package httpmw

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

// MetricsRecorder 定义 HTTP 中间件记录请求指标所需的能力。
// 由 internal/platform/observability/metrics 提供实现，这里只声明消费契约。
type MetricsRecorder interface {
	ObserveHTTPRequest(method string, route string, status int, duration time.Duration)
}

// Metrics 记录每个 HTTP 请求的计数、状态码和耗时。
//
// route label 使用 chi 路由模板（如 /v1/chat/completions）而不是原始 URL，
// 避免把请求路径里的高基数值写进 Prometheus label；未匹配路由统一记为 unmatched。
func Metrics(recorder MetricsRecorder) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if recorder == nil {
			return next
		}

		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			rec := &statusRecorder{
				ResponseWriter: w,
				status:         http.StatusOK,
			}

			next.ServeHTTP(rec, r)

			recorder.ObserveHTTPRequest(r.Method, metricsRoutePattern(r), rec.status, time.Since(start))
		})
	}
}

// metricsRoutePattern 返回 chi 匹配到的路由模板；未匹配路由返回 unmatched 以限制 label 基数。
func metricsRoutePattern(r *http.Request) string {
	if rctx := chi.RouteContext(r.Context()); rctx != nil {
		if pattern := rctx.RoutePattern(); pattern != "" {
			return pattern
		}
	}

	return "unmatched"
}
