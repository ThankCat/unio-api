package modelcatalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// placeholderSVG 模拟 models.dev 对无图标出品方返回的统一占位星星图。
const placeholderSVG = `<svg viewBox="0 0 24 24"><path d="M9.8132 15.9038L9 18.75C8.1868 14.4089"/></svg>`

const realSVG = `<svg viewBox="0 0 40 40"><path d="M1 1h38v38H1z"/></svg>`

// newLogoServer 起一个假 models.dev：labs 与 provider 两个 logo 路径按传入映射响应，缺失 404。
func newLogoServer(t *testing.T, labs, providers map[string]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	serve := func(table map[string]string, prefix string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			slug := r.URL.Path[len(prefix) : len(r.URL.Path)-len(".svg")]
			svg, ok := table[slug]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "image/svg+xml")
			_, _ = w.Write([]byte(svg))
		}
	}
	mux.HandleFunc("/logos/labs/", serve(labs, "/logos/labs/"))
	mux.HandleFunc("/logos/", serve(providers, "/logos/"))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func TestFetchLabLogoPrefersLabsPath(t *testing.T) {
	t.Parallel()
	// provider 路径故意放占位图：labs 命中后不该再看 provider。
	server := newLogoServer(t,
		map[string]string{"tencent": realSVG},
		map[string]string{"tencent": placeholderSVG},
	)
	fetcher := NewHTTPFetcher(server.URL, 0, 0)

	svg, err := fetcher.FetchLabLogo(context.Background(), "tencent")
	if err != nil {
		t.Fatalf("FetchLabLogo: %v", err)
	}
	if svg != realSVG {
		t.Fatalf("want labs-path svg, got %q", svg)
	}
}

func TestFetchLabLogoFallsBackToProviderPath(t *testing.T) {
	t.Parallel()
	server := newLogoServer(t,
		map[string]string{},
		map[string]string{"openai": realSVG},
	)
	fetcher := NewHTTPFetcher(server.URL, 0, 0)

	svg, err := fetcher.FetchLabLogo(context.Background(), "openai")
	if err != nil {
		t.Fatalf("FetchLabLogo: %v", err)
	}
	if svg != realSVG {
		t.Fatalf("want provider-path fallback svg, got %q", svg)
	}
}

func TestFetchLabLogoDiscardsPlaceholder(t *testing.T) {
	t.Parallel()
	server := newLogoServer(t,
		map[string]string{"ibm": placeholderSVG},
		map[string]string{},
	)
	fetcher := NewHTTPFetcher(server.URL, 0, 0)

	svg, err := fetcher.FetchLabLogo(context.Background(), "ibm")
	if err != nil {
		t.Fatalf("FetchLabLogo: %v", err)
	}
	if svg != "" {
		t.Fatalf("placeholder must be treated as no logo, got %q", svg)
	}
}

func TestFetchLabLogoBothPathsMissing(t *testing.T) {
	t.Parallel()
	server := newLogoServer(t, map[string]string{}, map[string]string{})
	fetcher := NewHTTPFetcher(server.URL, 0, 0)

	svg, err := fetcher.FetchLabLogo(context.Background(), "nobody")
	if err != nil {
		t.Fatalf("FetchLabLogo: %v", err)
	}
	if svg != "" {
		t.Fatalf("missing logo must yield empty string, got %q", svg)
	}
}
