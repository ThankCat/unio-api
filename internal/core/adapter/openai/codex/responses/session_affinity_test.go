package codexresponses

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	openairesponses "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/responses"
	"github.com/ThankCat/unio-gateway/internal/core/channel"
	"github.com/ThankCat/unio-gateway/internal/core/sessionhint"
)

var uuidV4Shape = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func affinityCtx(sessionKey string, apiKeyID int64) context.Context {
	return sessionhint.WithUpstreamAffinity(context.Background(), sessionhint.UpstreamAffinity{
		SessionKey: sessionKey, APIKeyID: apiKeyID,
	})
}

func poolRuntime(upstreamAccountID string) channel.Runtime {
	return channel.Runtime{Account: channel.AccountIdentity{ID: 1, UpstreamAccountID: upstreamAccountID}}
}

// TestUpstreamSessionIDDerivation 冻结派生契约：同客户同会话同账号恒定；客户、账号、会话任一
// 不同即不同；形状为 UUIDv4（36 字符，低于上游 64 字符上限）；无会话键不表达身份。
func TestUpstreamSessionIDDerivation(t *testing.T) {
	base := upstreamSessionID(affinityCtx("01a06086-5391-7d71-a82e-e5638a243ec4", 1), poolRuntime("acct-a"))
	if !uuidV4Shape.MatchString(base) {
		t.Fatalf("derived id %q is not UUIDv4-shaped", base)
	}
	if again := upstreamSessionID(affinityCtx("01a06086-5391-7d71-a82e-e5638a243ec4", 1), poolRuntime("acct-a")); again != base {
		t.Fatalf("derivation must be deterministic: %q vs %q", base, again)
	}
	if base == "01a06086-5391-7d71-a82e-e5638a243ec4" {
		t.Fatal("client session key must never be sent upstream verbatim")
	}
	for name, got := range map[string]string{
		"different api key":    upstreamSessionID(affinityCtx("01a06086-5391-7d71-a82e-e5638a243ec4", 2), poolRuntime("acct-a")),
		"different account":    upstreamSessionID(affinityCtx("01a06086-5391-7d71-a82e-e5638a243ec4", 1), poolRuntime("acct-b")),
		"different session":    upstreamSessionID(affinityCtx("01a06086-5391-7d71-a82e-ffffffffffff", 1), poolRuntime("acct-a")),
		"content derived hint": upstreamSessionID(affinityCtx("content:abcdef", 1), poolRuntime("acct-a")),
	} {
		if got == base || !uuidV4Shape.MatchString(got) {
			t.Fatalf("%s: got %q, want a distinct UUIDv4-shaped id (base %q)", name, got, base)
		}
	}
	if got := upstreamSessionID(context.Background(), poolRuntime("acct-a")); got != "" {
		t.Fatalf("no affinity in ctx must yield empty id, got %q", got)
	}
	if got := upstreamSessionID(affinityCtx("   ", 1), poolRuntime("acct-a")); got != "" {
		t.Fatalf("blank session key must yield empty id, got %q", got)
	}
}

// TestBindCodexSessionRewritesOnlyPromptCacheKey 冻结 body 绑定：顶层 prompt_cache_key 覆盖或注入为
// 派生值，其余字节（字段顺序、数字原文、嵌套同名键）原样保留；输入切片不被污染。
func TestBindCodexSessionRewritesOnlyPromptCacheKey(t *testing.T) {
	ctx := affinityCtx("thread-1", 1)
	ch := poolRuntime("acct-a")
	want := upstreamSessionID(ctx, ch)

	cases := []struct {
		name string
		body string
	}{
		{name: "overwrite existing", body: `{"model":"gpt-5.5","prompt_cache_key":"thread-1","temperature": 1.2300,"input":[{"prompt_cache_key":"nested"}],"stream":true}`},
		{name: "inject missing", body: `{"model":"gpt-5.5","temperature": 1.2300,"input":[{"prompt_cache_key":"nested"}],"stream":true}`},
		{name: "overwrite null", body: `{"model":"gpt-5.5","prompt_cache_key":null,"stream":true}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := []byte(tc.body)
			out, err := bindCodexSession(ctx, ch, input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(input) != tc.body {
				t.Fatal("input body must not be mutated in place")
			}
			var got map[string]json.RawMessage
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatalf("bound body is not valid JSON: %v\n%s", err, out)
			}
			if string(got["prompt_cache_key"]) != `"`+want+`"` {
				t.Fatalf("prompt_cache_key = %s, want %q", got["prompt_cache_key"], want)
			}
			if strings.Contains(tc.body, "temperature") && !bytes.Contains(out, []byte(`"temperature": 1.2300`)) {
				t.Fatalf("untouched bytes were re-encoded: %s", out)
			}
			if strings.Contains(tc.body, "nested") && !bytes.Contains(out, []byte(`{"prompt_cache_key":"nested"}`)) {
				t.Fatalf("nested same-name key must stay verbatim: %s", out)
			}
			if bytes.Contains(out, []byte(`"thread-1"`)) {
				t.Fatalf("client session key leaked into outbound body: %s", out)
			}
		})
	}
}

func TestBindCodexSessionNoAffinityIsNoop(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","prompt_cache_key":"thread-1","stream":true}`)
	out, err := bindCodexSession(context.Background(), poolRuntime("acct-a"), body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(out) != string(body) {
		t.Fatalf("without affinity the body must pass through untouched, got %s", out)
	}
}

func TestBindCodexSessionIdempotentAndTolerant(t *testing.T) {
	ctx := affinityCtx("thread-1", 1)
	ch := poolRuntime("acct-a")
	id := upstreamSessionID(ctx, ch)

	already := []byte(`{"model":"gpt-5.5","prompt_cache_key":"` + id + `","stream":true}`)
	out, err := bindCodexSession(ctx, ch, already)
	if err != nil || string(out) != string(already) {
		t.Fatalf("already-bound body must be returned unchanged, got %s err=%v", out, err)
	}

	for _, malformed := range []string{`not-json`, `[1,2,3]`, ``} {
		out, err := bindCodexSession(ctx, ch, []byte(malformed))
		if err != nil || string(out) != malformed {
			t.Fatalf("non-object body %q must pass through for upstream to reject, got %q err=%v", malformed, out, err)
		}
	}
}

// TestCodexOutboundCarriesSessionAffinity 是 wire 装配回归：经真实 base adapter 出站，上游收到的
// 亲和头（连字符 + 下划线拼写 + x-client-request-id）与 body prompt_cache_key 四处同值且等于派生 id，
// 客户端原始会话键不出现在任何出站头或 body 中；/responses 与 /responses/compact 同样生效。
func TestCodexOutboundCarriesSessionAffinity(t *testing.T) {
	const clientThread = "01a06086-5391-7d71-a82e-e5638a243ec4"
	ctx := affinityCtx(clientThread, 42)

	type capture struct {
		header http.Header
		body   []byte
	}
	captured := map[string]capture{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		captured[r.URL.Path] = capture{header: r.Header.Clone(), body: raw}
		switch r.URL.Path {
		case responsesPath:
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("event: response.completed\n" + `data: {"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","model":"gpt-5.5","usage":{"input_tokens":4,"output_tokens":1,"total_tokens":5}}}` + "\n\n"))
		case compactPath:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"resp_c","object":"response","status":"completed","model":"gpt-5.5","output":[],"usage":{"input_tokens":4,"output_tokens":1,"total_tokens":5}}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	ch := channel.Runtime{
		Origin:  server.URL,
		APIKey:  "tok",
		Account: channel.AccountIdentity{ID: 1, UpstreamAccountID: "acct-a"},
	}
	want := upstreamSessionID(ctx, ch)
	a := NewAdapter(server.Client(), nil, nil)

	body := json.RawMessage(`{"model":"gpt-5.5","store":false,"stream":true,"prompt_cache_key":"` + clientThread + `","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)
	if _, err := a.CreateResponse(ctx, ch, openairesponses.Request{Body: body}); err != nil {
		t.Fatalf("CreateResponse: %v", err)
	}
	if _, err := a.CompactResponse(ctx, ch, openairesponses.Request{Body: body}); err != nil {
		t.Fatalf("CompactResponse: %v", err)
	}

	for _, path := range []string{responsesPath, compactPath} {
		got, ok := captured[path]
		if !ok {
			t.Fatalf("upstream never received %s", path)
		}
		for _, name := range sessionAffinityHeaders {
			if v := got.header.Get(name); v != want {
				t.Fatalf("%s header %s = %q, want %q", path, name, v, want)
			}
		}
		if v := got.header.Get("chatgpt-account-id"); v != "acct-a" {
			t.Fatalf("%s chatgpt-account-id = %q, want acct-a", path, v)
		}
		var decoded struct {
			PromptCacheKey string `json:"prompt_cache_key"`
		}
		if err := json.Unmarshal(got.body, &decoded); err != nil {
			t.Fatalf("%s body: %v", path, err)
		}
		if decoded.PromptCacheKey != want {
			t.Fatalf("%s body prompt_cache_key = %q, want %q", path, decoded.PromptCacheKey, want)
		}
		if bytes.Contains(got.body, []byte(clientThread)) {
			t.Fatalf("%s leaked client session key in body: %s", path, got.body)
		}
		for name, values := range got.header {
			for _, v := range values {
				if strings.Contains(v, clientThread) {
					t.Fatalf("%s leaked client session key in header %s", path, name)
				}
			}
		}
	}
}

// TestCodexOutboundWithoutAffinityOmitsSessionHeaders 冻结无会话键路径（渠道/账号探测、单测直构）：
// 不发任何亲和头，body 原样。
func TestCodexOutboundWithoutAffinityOmitsSessionHeaders(t *testing.T) {
	var gotHeader http.Header
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Clone()
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.completed\n" + `data: {"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","model":"gpt-5.5","usage":{"input_tokens":4,"output_tokens":1,"total_tokens":5}}}` + "\n\n"))
	}))
	defer server.Close()

	a := NewAdapter(server.Client(), nil, nil)
	body := `{"model":"gpt-5.5","store":false,"stream":true,"prompt_cache_key":"probe","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`
	if _, err := a.CreateResponse(context.Background(), channel.Runtime{Origin: server.URL, APIKey: "tok"}, openairesponses.Request{Body: json.RawMessage(body)}); err != nil {
		t.Fatalf("CreateResponse: %v", err)
	}
	for _, name := range sessionAffinityHeaders {
		if v := gotHeader.Get(name); v != "" {
			t.Fatalf("header %s = %q, want absent without affinity", name, v)
		}
	}
	if string(gotBody) != body {
		t.Fatalf("body must pass through verbatim without affinity:\n got %s\nwant %s", gotBody, body)
	}
}
