package meta

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

type envelope struct {
	Data struct {
		Configured bool `json:"configured"`
		Protocols  []struct {
			Key       string   `json:"key"`
			BaseURL   string   `json:"base_url"`
			AuthStyle string   `json:"auth_style"`
			Endpoints []string `json:"endpoints"`
		} `json:"protocols"`
	} `json:"data"`
}

func get(t *testing.T, deps Deps) envelope {
	t.Helper()
	out, _ := getWithBody(t, deps)
	return out
}

func getWithBody(t *testing.T, deps Deps) (envelope, string) {
	t.Helper()
	r := chi.NewRouter()
	Register(r, deps)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/meta/endpoints", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	var out envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v; body = %s", err, rec.Body.String())
	}
	return out, rec.Body.String()
}

// 没配公开地址时不能猜一个出来给用户——前端据 configured 隐藏整块接入区。
func TestEndpointsReportsUnconfigured(t *testing.T) {
	out := get(t, Deps{})

	if out.Data.Configured {
		t.Fatal("configured must be false when no public base url is set")
	}
	if len(out.Data.Protocols) != 0 {
		t.Fatalf("protocols must be empty, got %d", len(out.Data.Protocols))
	}
}

func TestEndpointsListsBothProtocols(t *testing.T) {
	out := get(t, Deps{
		GatewayPublicBaseURL: "https://api.example.com/v1",
	})

	if !out.Data.Configured {
		t.Fatal("configured must be true once the base url is set")
	}
	if len(out.Data.Protocols) != 2 {
		t.Fatalf("protocols = %d, want 2", len(out.Data.Protocols))
	}

	byKey := map[string]int{}
	for i, p := range out.Data.Protocols {
		byKey[p.Key] = i
		if p.BaseURL != "https://api.example.com/v1" {
			t.Fatalf("%s base_url = %q", p.Key, p.BaseURL)
		}
		if len(p.Endpoints) == 0 {
			t.Fatalf("%s must expose at least one endpoint", p.Key)
		}
	}

	// 两种协议的认证头不同，前端要据此给出正确的示例代码。
	openai, ok := byKey["openai"]
	if !ok {
		t.Fatal("openai protocol is missing")
	}
	if got := out.Data.Protocols[openai].AuthStyle; got != "bearer" {
		t.Fatalf("openai auth_style = %q, want bearer", got)
	}
	anthropic, ok := byKey["anthropic"]
	if !ok {
		t.Fatal("anthropic protocol is missing")
	}
	if got := out.Data.Protocols[anthropic].AuthStyle; got != "x-api-key" {
		t.Fatalf("anthropic auth_style = %q, want x-api-key", got)
	}
}

// 文档地址不由网关下发：文档站部署在哪属于前端形态，由 console 的 VITE_DOCS_URL 决定。
// 网关同时配一份意味着同一个地址要在两处保持一致，而漏配的表现是按钮静默消失。
func TestEndpointsDoesNotCarryDocURL(t *testing.T) {
	_, body := getWithBody(t, Deps{GatewayPublicBaseURL: "https://api.example.com/v1"})

	if strings.Contains(body, "doc_url") {
		t.Fatalf("response must not carry doc_url; body = %s", body)
	}
}
