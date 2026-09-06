package proxyclient

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestClientForReturnsDirectClientForEmptyProxy(t *testing.T) {
	direct := &http.Client{Timeout: 3 * time.Second}
	resolver := NewResolver(direct)
	if got := resolver.ClientFor(""); got != direct {
		t.Fatal("empty proxy url must reuse the direct client")
	}
}

func TestClientForCachesPerProxyURLAndInheritsTimeout(t *testing.T) {
	direct := &http.Client{Timeout: 7 * time.Second}
	resolver := NewResolver(direct)
	first := resolver.ClientFor("http://proxy.internal:8080")
	second := resolver.ClientFor("http://proxy.internal:8080")
	if first == nil || first == direct {
		t.Fatal("proxy url must produce a dedicated client")
	}
	if first != second {
		t.Fatal("same proxy url must reuse the cached client")
	}
	if first.Timeout != direct.Timeout {
		t.Fatalf("proxy client timeout = %v, want %v", first.Timeout, direct.Timeout)
	}
	if other := resolver.ClientFor("socks5://other.internal:1080"); other == first {
		t.Fatal("different proxy urls must not share a client")
	}
}

// 非法代理 URL 不得静默直连：请求必须在出站路径上明确失败（同号同出口是风控约束）。
func TestClientForFailsClosedOnInvalidProxyURL(t *testing.T) {
	direct := &http.Client{}
	resolver := NewResolver(direct)
	for _, raw := range []string{"::not-a-url", "proxy.internal:8080", "http://"} {
		client := resolver.ClientFor(raw)
		if client == direct {
			t.Fatalf("invalid proxy url %q fell back to the direct client", raw)
		}
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://upstream.example/v1/models", nil)
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.Do(req)
		if !errors.Is(err, ErrInvalidProxyURL) {
			t.Fatalf("invalid proxy url %q: err = %v, want ErrInvalidProxyURL", raw, err)
		}
		if resolver.ClientFor(raw) != client {
			t.Fatalf("invalid proxy url %q must cache its failing client", raw)
		}
	}
}
