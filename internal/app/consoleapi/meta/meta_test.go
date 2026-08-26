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
			DocURL    string   `json:"doc_url"`
		} `json:"protocols"`
	} `json:"data"`
}

func get(t *testing.T, deps Deps) envelope {
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
	return out
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
		DocsBaseURL:          "https://docs.example.com",
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
		if !strings.HasPrefix(p.DocURL, "https://docs.example.com") {
			t.Fatalf("%s doc_url = %q", p.Key, p.DocURL)
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

// 文档站没配时不能拼出 "/quickstart/openai" 这种断头链接。
func TestEndpointsOmitsDocURLWhenDocsUnset(t *testing.T) {
	out := get(t, Deps{GatewayPublicBaseURL: "https://api.example.com/v1"})

	for _, p := range out.Data.Protocols {
		if p.DocURL != "" {
			t.Fatalf("%s doc_url = %q, want empty", p.Key, p.DocURL)
		}
	}
}
