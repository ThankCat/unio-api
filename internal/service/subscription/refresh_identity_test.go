package subscription

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/ThankCat/unio-gateway/internal/core/codexidentity"
)

type captureTransport struct {
	req *http.Request
}

func (c *captureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	c.req = req
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"access_token":"a","refresh_token":"r","id_token":"i","expires_in":3600}`)),
		Request:    req,
	}, nil
}

// TestTokenClientSendsCanonicalCredentialIdentity 冻结凭据面身份：与推理面同源的 originator + User-Agent
// 成对出站、不发 version（真实客户端在 auth.openai.com 面的形态），版本随注入来源变化，
// 不再有网关自有的 UA 字面量。
func TestTokenClientSendsCanonicalCredentialIdentity(t *testing.T) {
	transport := &captureTransport{}
	client := &http.Client{Transport: transport}
	tc := NewTokenClient(func(string) *http.Client { return client }, func() string { return "0.158.0" })

	if _, err := tc.Refresh(context.Background(), Credentials{RefreshToken: "rt"}, ""); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if transport.req == nil {
		t.Fatal("token endpoint was not called")
	}
	h := transport.req.Header
	want := codexidentity.WithVersion("0.158.0")
	if h.Get("originator") != codexidentity.Originator {
		t.Fatalf("originator = %q, want %q", h.Get("originator"), codexidentity.Originator)
	}
	if h.Get("User-Agent") != want.UserAgent() {
		t.Fatalf("user agent = %q, want %q", h.Get("User-Agent"), want.UserAgent())
	}
	if h.Get("version") != "" {
		t.Fatalf("credential face must not send version, got %q", h.Get("version"))
	}
	if strings.Contains(strings.ToLower(h.Get("User-Agent")), "unio") {
		t.Fatal("user agent leaks gateway marker")
	}
}
