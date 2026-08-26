package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 预检放行的方法必须覆盖路由实际注册的写方法。少一个，浏览器侧对应功能就整体失效，
// 而 curl 不发预检，测不出来——所以这里按方法逐个断言。
func TestCORSPreflightAllowsEveryRoutedMethod(t *testing.T) {
	handler := CORS([]string{"http://localhost:5174"})(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		},
	))

	request := httptest.NewRequest(http.MethodOptions, "/v1/api-keys/1", nil)
	request.Header.Set("Origin", "http://localhost:5174")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want %d", recorder.Code, http.StatusNoContent)
	}

	allowed := recorder.Header().Get("Access-Control-Allow-Methods")
	for _, method := range []string{
		http.MethodGet,
		http.MethodPost,
		http.MethodPatch,
		http.MethodDelete,
		http.MethodOptions,
	} {
		if !strings.Contains(allowed, method) {
			t.Errorf("Access-Control-Allow-Methods = %q, missing %s", allowed, method)
		}
	}
}

func TestCORSRejectsUnlistedOrigin(t *testing.T) {
	handler := CORS([]string{"http://localhost:5174"})(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			t.Error("handler must not run for a disallowed origin")
		},
	))

	request := httptest.NewRequest(http.MethodPatch, "/v1/api-keys/1", nil)
	request.Header.Set("Origin", "http://evil.example.com")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
	if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want empty", got)
	}
}
