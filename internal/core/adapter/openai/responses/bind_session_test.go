package responses

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ThankCat/unio-gateway/internal/core/channel"
	"github.com/ThankCat/unio-gateway/internal/core/sessionhint"
)

const bindSessionRespBody = `{"id":"resp_1","object":"response","status":"completed","model":"gpt-5.5","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`

func bindSessionServer(t *testing.T, captured map[string][]byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		captured[r.URL.Path] = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(bindSessionRespBody))
	}))
}

// TestOfficialWireIgnoresUpstreamAffinity 冻结官方 wire 纪律：ctx 里即便带会话亲和事实，
// 缺省 wire 也零转换直传（/responses 与 /responses/compact），不改 body、不加会话头。
func TestOfficialWireIgnoresUpstreamAffinity(t *testing.T) {
	captured := map[string][]byte{}
	server := bindSessionServer(t, captured)
	defer server.Close()

	ctx := sessionhint.WithUpstreamAffinity(context.Background(), sessionhint.UpstreamAffinity{SessionKey: "thread-1", APIKeyID: 7})
	a := NewAdapter(server.Client())
	body := json.RawMessage(`{"model":"gpt-5.5","stream":false,"prompt_cache_key":"thread-1","input":"hello"}`)
	if _, err := a.CreateResponse(ctx, testChannel(server.URL), Request{Body: body}); err != nil {
		t.Fatalf("CreateResponse: %v", err)
	}
	if _, err := a.CompactResponse(ctx, testChannel(server.URL), Request{Body: body}); err != nil {
		t.Fatalf("CompactResponse: %v", err)
	}
	for _, path := range []string{"/v1/responses", "/v1/responses/compact"} {
		if string(captured[path]) != string(body) {
			t.Fatalf("%s body = %s, want verbatim %s", path, captured[path], body)
		}
	}
}

// TestWireBindSessionAppliedToBothOutboundPaths 验证 BindSession 钩子在 /responses 与
// /responses/compact 出站前都被调用，且钩子拿到的是出站 ctx 与渠道运行时。
func TestWireBindSessionAppliedToBothOutboundPaths(t *testing.T) {
	captured := map[string][]byte{}
	server := bindSessionServer(t, captured)
	defer server.Close()

	var seenAccounts []string
	a := NewAdapterWithWire(server.Client(), Wire{
		BindSession: func(ctx context.Context, ch channel.Runtime, body []byte) ([]byte, error) {
			seenAccounts = append(seenAccounts, ch.Account.UpstreamAccountID)
			affinity := sessionhint.UpstreamAffinityFromContext(ctx)
			return []byte(`{"bound":"` + affinity.SessionKey + `"}`), nil
		},
	})
	ctx := sessionhint.WithUpstreamAffinity(context.Background(), sessionhint.UpstreamAffinity{SessionKey: "thread-1", APIKeyID: 7})
	ch := testChannel(server.URL)
	ch.Account = channel.AccountIdentity{ID: 1, UpstreamAccountID: "acct-a"}
	body := json.RawMessage(`{"model":"gpt-5.5","stream":false,"input":"hello"}`)

	if _, err := a.CreateResponse(ctx, ch, Request{Body: body}); err != nil {
		t.Fatalf("CreateResponse: %v", err)
	}
	if _, err := a.CompactResponse(ctx, ch, Request{Body: body}); err != nil {
		t.Fatalf("CompactResponse: %v", err)
	}
	for _, path := range []string{"/v1/responses", "/v1/responses/compact"} {
		if string(captured[path]) != `{"bound":"thread-1"}` {
			t.Fatalf("%s body = %s, want bound body", path, captured[path])
		}
	}
	if len(seenAccounts) != 2 || seenAccounts[0] != "acct-a" || seenAccounts[1] != "acct-a" {
		t.Fatalf("BindSession runtimes = %v, want account acct-a on both paths", seenAccounts)
	}
}
