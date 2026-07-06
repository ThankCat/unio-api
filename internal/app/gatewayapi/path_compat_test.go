package gatewayapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCanonicalizeV1Path(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"correct single prefix unchanged", "/v1/messages", "/v1/messages"},
		{"doubled prefix collapsed", "/v1/v1/messages", "/v1/messages"},
		{"tripled prefix collapsed", "/v1/v1/v1/messages", "/v1/messages"},
		{"missing prefix added", "/messages", "/v1/messages"},
		{"missing prefix nested", "/chat/completions", "/v1/chat/completions"},
		{"doubled prefix nested", "/v1/v1/responses/compact", "/v1/responses/compact"},
		{"responses exact missing prefix", "/responses", "/v1/responses"},
		{"responses subpath missing prefix", "/responses/resp_1/cancel", "/v1/responses/resp_1/cancel"},
		{"models correct", "/v1/models", "/v1/models"},
		{"models missing prefix", "/models", "/v1/models"},
		{"bare version root", "/v1", "/v1"},
		{"bare version root trailing", "/v1/", "/v1/"},
		{"doubled bare version", "/v1/v1", "/v1"},
		{"unknown root path kept for clean 404", "/not-found", "/not-found"},
		{"unknown v1 path collapsed but not invented", "/v1/v1/not-found", "/v1/not-found"},
		{"healthz exempt", "/healthz", "/healthz"},
		{"metrics exempt", "/metrics", "/metrics"},
		{"root exempt", "/", "/"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := canonicalizeV1Path(tc.in); got != tc.want {
				t.Fatalf("canonicalizeV1Path(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestV1PathCompatRoutesCanonicalKeepsOriginal 验证中间件把下游路由到规范化路径，
// 同时不改动原始 *http.Request（外层日志/指标据此仍能记录客户端真实路径）。
func TestV1PathCompatRoutesCanonicalKeepsOriginal(t *testing.T) {
	const original = "/v1/v1/messages"

	var seenByDownstream string
	handler := v1PathCompat(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seenByDownstream = r.URL.Path
	}))

	req := httptest.NewRequest(http.MethodPost, original, nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if seenByDownstream != "/v1/messages" {
		t.Fatalf("downstream path = %q, want %q", seenByDownstream, "/v1/messages")
	}
	if req.URL.Path != original {
		t.Fatalf("original request path mutated to %q, want %q (logs must see client path)", req.URL.Path, original)
	}
}

// TestV1PathCompatPassthroughUnchanged 验证已规范的/豁免路径不触发请求副本，原样透传。
func TestV1PathCompatPassthroughUnchanged(t *testing.T) {
	for _, p := range []string{"/v1/messages", "/healthz", "/metrics"} {
		var seen string
		handler := v1PathCompat(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			seen = r.URL.Path
		}))
		req := httptest.NewRequest(http.MethodGet, p, nil)
		handler.ServeHTTP(httptest.NewRecorder(), req)
		if seen != p {
			t.Fatalf("path %q routed to %q, want unchanged", p, seen)
		}
	}
}
