package httpmw

import (
	"net/http"
	"strconv"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/ThankCat/unio-api/internal/platform/observability/tracing"
)

// Tracing 为每个 HTTP 请求创建一个 server span，并把上游 trace context 串联进来。
//
// 当 tracing 未启用时，otel 返回 no-op tracer，本中间件的开销可忽略。
// span 只记录方法、路由模板和状态码等非敏感属性，不记录请求体或鉴权头。
func Tracing(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 从请求头提取上游 W3C trace context（traceparent），实现跨服务链路串联。
		ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))

		// 路由模板要等 chi 完成匹配后才可用，因此 span 先以方法命名，
		// 在 ServeHTTP 之后再用 SetName 补上 "METHOD /route"。
		ctx, span := tracing.Tracer().Start(
			ctx,
			r.Method,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(attribute.String("http.request.method", r.Method)),
		)
		defer span.End()

		rec := &statusRecorder{
			ResponseWriter: w,
			status:         http.StatusOK,
		}

		next.ServeHTTP(rec, r.WithContext(ctx))

		route := metricsRoutePattern(r)
		span.SetName(r.Method + " " + route)
		span.SetAttributes(
			attribute.String("http.route", route),
			attribute.Int("http.response.status_code", rec.status),
		)
		if rec.status >= http.StatusInternalServerError {
			span.SetStatus(codes.Error, "server error: "+strconv.Itoa(rec.status))
		}
	})
}
