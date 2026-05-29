package httpmw

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

// fakeMetricsRecorder 记录 HTTP 指标中间件上报的参数。
type fakeMetricsRecorder struct {
	calls []httpMetricsCall
}

type httpMetricsCall struct {
	method   string
	route    string
	status   int
	duration time.Duration
}

func (r *fakeMetricsRecorder) ObserveHTTPRequest(method string, route string, status int, duration time.Duration) {
	r.calls = append(r.calls, httpMetricsCall{method: method, route: route, status: status, duration: duration})
}

// TestMetricsMiddlewareRecordsRoutePattern 验证中间件使用 chi 路由模板而非原始 URL 记录指标。
func TestMetricsMiddlewareRecordsRoutePattern(t *testing.T) {
	recorder := &fakeMetricsRecorder{}

	r := chi.NewRouter()
	r.Use(Metrics(recorder))
	r.Get("/v1/items/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/items/abc-123", nil)
	r.ServeHTTP(httptest.NewRecorder(), req)

	if len(recorder.calls) != 1 {
		t.Fatalf("expected one metrics call, got %d", len(recorder.calls))
	}
	call := recorder.calls[0]
	if call.method != http.MethodGet {
		t.Fatalf("method: got %q, want GET", call.method)
	}
	if call.route != "/v1/items/{id}" {
		t.Fatalf("route: got %q, want %q (must be pattern, not raw URL)", call.route, "/v1/items/{id}")
	}
	if call.status != http.StatusCreated {
		t.Fatalf("status: got %d, want %d", call.status, http.StatusCreated)
	}
}

// TestMetricsMiddlewareRecordsUnmatchedRoute 验证未匹配路由记为 unmatched，避免 404 路径污染 label 基数。
func TestMetricsMiddlewareRecordsUnmatchedRoute(t *testing.T) {
	recorder := &fakeMetricsRecorder{}

	r := chi.NewRouter()
	r.Use(Metrics(recorder))
	r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	// 注册一个真实路由，确保 chi 构建中间件链（与生产 router 一致）。
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/no/such/path/xyz", nil)
	r.ServeHTTP(httptest.NewRecorder(), req)

	if len(recorder.calls) != 1 {
		t.Fatalf("expected one metrics call, got %d", len(recorder.calls))
	}
	if recorder.calls[0].route != "unmatched" {
		t.Fatalf("route: got %q, want %q", recorder.calls[0].route, "unmatched")
	}
}

// TestMetricsMiddlewareNilRecorderIsNoop 验证未配置 recorder 时中间件不改变请求处理。
func TestMetricsMiddlewareNilRecorderIsNoop(t *testing.T) {
	r := chi.NewRouter()
	r.Use(Metrics(nil))
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200 with nil recorder, got %d", rec.Code)
	}
}
