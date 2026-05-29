package httpmw

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// installSpanRecorder 安装一个进程级内存 span recorder，并在测试结束后还原 provider。
func installSpanRecorder(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()

	recorder := tracetest.NewSpanRecorder()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
	t.Cleanup(func() {
		otel.SetTracerProvider(sdktrace.NewTracerProvider())
	})

	return recorder
}

func attrValue(attrs []attribute.KeyValue, key string) (attribute.Value, bool) {
	for _, kv := range attrs {
		if string(kv.Key) == key {
			return kv.Value, true
		}
	}

	return attribute.Value{}, false
}

// TestTracingMiddlewareCreatesServerSpan 验证中间件用路由模板命名 server span 并记录状态码。
func TestTracingMiddlewareCreatesServerSpan(t *testing.T) {
	recorder := installSpanRecorder(t)

	r := chi.NewRouter()
	r.Use(Tracing)
	r.Get("/v1/items/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})

	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/items/abc", nil))

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("expected one span, got %d", len(spans))
	}

	span := spans[0]
	if span.Name() != "GET /v1/items/{id}" {
		t.Fatalf("span name: got %q, want %q", span.Name(), "GET /v1/items/{id}")
	}

	route, ok := attrValue(span.Attributes(), "http.route")
	if !ok || route.AsString() != "/v1/items/{id}" {
		t.Fatalf("http.route attr: got %v ok=%v", route, ok)
	}
	status, ok := attrValue(span.Attributes(), "http.response.status_code")
	if !ok || status.AsInt64() != int64(http.StatusCreated) {
		t.Fatalf("http.response.status_code attr: got %v ok=%v", status, ok)
	}
}
