package codexresponses

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	openairesponses "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/responses"
	"github.com/ThankCat/unio-gateway/internal/core/channel"
	"github.com/ThankCat/unio-gateway/internal/core/clientmeta"
	"github.com/ThankCat/unio-gateway/internal/core/sessionhint"
	"github.com/buger/jsonparser"
)

const (
	clientThread       = "01a06086-5391-7d71-a82e-e5638a243ec4"
	clientInstallation = "4cc3b6c5-f73b-4f20-b6d7-2f2cd9a9ee0a"
	clientTurn         = "01a06086-6747-78d2-ae11-727773ca26fd"
)

// clientMetadataBody 复刻 0.152.1 抓包里的 client_metadata 形态（含内嵌 turn-metadata 字符串）。
func clientMetadataBody() string {
	embedded := `{"installation_id":"` + clientInstallation + `","session_id":"` + clientThread + `","thread_id":"` + clientThread + `","turn_id":"` + clientTurn + `","window_id":"` + clientThread + `:0","request_kind":"turn","sandbox":"seatbelt","workspaces":{"/Users/me/proj":{"has_changes":false}},"turn_started_at_unix_ms":1788325816137}`
	embeddedJSON, _ := json.Marshal(embedded)
	return `{"model":"gpt-5.5","store":false,"stream":true,"prompt_cache_key":"` + clientThread + `","input":[],"temperature": 1.2300,"client_metadata":{"thread_id":"` + clientThread + `","session_id":"` + clientThread + `","turn_id":"` + clientTurn + `","x-codex-installation-id":"` + clientInstallation + `","x-codex-window-id":"` + clientThread + `:0","x-codex-turn-metadata":` + string(embeddedJSON) + `}}`
}

func turnCtx(sessionKey string, apiKeyID int64, turnMetadata, windowID string) context.Context {
	ctx := sessionhint.WithUpstreamAffinity(context.Background(), sessionhint.UpstreamAffinity{SessionKey: sessionKey, APIKeyID: apiKeyID})
	return clientmeta.WithTurn(ctx, clientmeta.ParseCodexHeaders(turnMetadata, windowID))
}

// TestBindCodexSessionNamespacesClientMetadata 冻结 body 身份映射：session/thread/x-client-request-id 等于派生
// 会话身份（四处同值），window 换 thread 分量保留序号，installation/turn 按客户 × 账号 1:1 映射，
// 内嵌 turn-metadata 同规则改写；sandbox/workspaces/request_kind/turn_started_at_unix_ms 原样保留；
// body 其余字节（temperature 原文）不动；客户端原始 id 不再出现在任何位置。
func TestBindCodexSessionNamespacesClientMetadata(t *testing.T) {
	ctx := affinityCtx(clientThread, 7)
	ch := poolRuntime("acct-a")
	id := upstreamSessionID(ctx, ch)

	out, err := bindCodexSession(ctx, ch, []byte(clientMetadataBody()))
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	if !bytes.Contains(out, []byte(`"temperature": 1.2300`)) {
		t.Fatalf("untouched bytes were re-encoded: %s", out)
	}
	for _, leaked := range []string{clientThread, clientInstallation, clientTurn} {
		if bytes.Contains(out, []byte(leaked)) {
			t.Fatalf("client identity %q leaked into outbound body: %s", leaked, out)
		}
	}
	var body struct {
		PromptCacheKey string         `json:"prompt_cache_key"`
		ClientMetadata map[string]any `json:"client_metadata"`
	}
	if err := json.Unmarshal(out, &body); err != nil {
		t.Fatalf("bound body invalid: %v\n%s", err, out)
	}
	cm := body.ClientMetadata
	if body.PromptCacheKey != id || cm["session_id"] != id || cm["thread_id"] != id {
		t.Fatalf("session identities must all equal %q: pck=%v session=%v thread=%v", id, body.PromptCacheKey, cm["session_id"], cm["thread_id"])
	}
	if cm["x-codex-window-id"] != id+":0" {
		t.Fatalf("window id = %v, want %s:0", cm["x-codex-window-id"], id)
	}
	wantInstallation := scopedIdentity(ctx, ch, "installation", clientInstallation)
	if cm["x-codex-installation-id"] != wantInstallation {
		t.Fatalf("installation id = %v, want scoped %q", cm["x-codex-installation-id"], wantInstallation)
	}
	wantTurn := scopedIdentity(ctx, ch, "turn", clientTurn)
	if cm["turn_id"] != wantTurn {
		t.Fatalf("turn id = %v, want scoped %q", cm["turn_id"], wantTurn)
	}

	embeddedRaw, _ := cm["x-codex-turn-metadata"].(string)
	var embedded map[string]any
	if err := json.Unmarshal([]byte(embeddedRaw), &embedded); err != nil {
		t.Fatalf("embedded turn metadata invalid: %v\n%s", err, embeddedRaw)
	}
	if embedded["installation_id"] != wantInstallation || embedded["session_id"] != id || embedded["thread_id"] != id ||
		embedded["turn_id"] != wantTurn || embedded["window_id"] != id+":0" {
		t.Fatalf("embedded identities not rewritten consistently: %v", embedded)
	}
	if embedded["sandbox"] != "seatbelt" || embedded["request_kind"] != "turn" || embedded["turn_started_at_unix_ms"] != float64(1788325816137) {
		t.Fatalf("non-identity fields must be preserved: %v", embedded)
	}
	if _, ok := embedded["workspaces"].(map[string]any); !ok {
		t.Fatalf("workspaces must be preserved verbatim (decision: 不动), got %v", embedded["workspaces"])
	}
}

// TestScopedIdentityIsolation 冻结 1:1 映射的隔离性：同客户同账号恒定；换客户或换账号即不同；不同种类互不相同。
func TestScopedIdentityIsolation(t *testing.T) {
	base := scopedIdentity(affinityCtx("s", 1), poolRuntime("acct-a"), "installation", clientInstallation)
	if base != scopedIdentity(affinityCtx("other-session", 1), poolRuntime("acct-a"), "installation", clientInstallation) {
		t.Fatal("installation mapping must not depend on the session (same device across conversations stays one device)")
	}
	for name, got := range map[string]string{
		"other api key": scopedIdentity(affinityCtx("s", 2), poolRuntime("acct-a"), "installation", clientInstallation),
		"other account": scopedIdentity(affinityCtx("s", 1), poolRuntime("acct-b"), "installation", clientInstallation),
		"other kind":    scopedIdentity(affinityCtx("s", 1), poolRuntime("acct-a"), "turn", clientInstallation),
	} {
		if got == base {
			t.Fatalf("%s must yield a different id", name)
		}
	}
	if scopedIdentity(affinityCtx("s", 1), poolRuntime("acct-a"), "installation", "  ") != "" {
		t.Fatal("blank raw must map to empty")
	}
}

// TestDeviceConvergenceCollapsesInstallation 冻结 device 收敛：账号内不同客户、不同原始 installation_id
// 都映射到同一个账号固定值；会话身份仍按对话各自派生（不合并）。
func TestDeviceConvergenceCollapsesInstallation(t *testing.T) {
	converged := poolRuntime("acct-a")
	converged.Account.FingerprintMode = channel.FingerprintModeDevice
	converged.Account.FingerprintSeed = "3f2c4b7e-1111-4222-8333-444455556666"

	a := installationIdentity(affinityCtx("s1", 1), converged, clientInstallation)
	b := installationIdentity(affinityCtx("s2", 2), converged, "another-device")
	if a == "" || a != b {
		t.Fatalf("device mode must collapse installation ids to one account value: %q vs %q", a, b)
	}
	if a == installationIdentity(affinityCtx("s1", 1), poolRuntime("acct-a"), clientInstallation) {
		t.Fatal("device value must differ from the off-mode 1:1 mapping")
	}
	if upstreamSessionID(affinityCtx("s1", 1), converged) == upstreamSessionID(affinityCtx("s2", 1), converged) {
		t.Fatal("device mode must not merge conversations")
	}

	// 未生成种子的 device 模式按 off 处理，不能派生出空/常量身份。
	noSeed := poolRuntime("acct-a")
	noSeed.Account.FingerprintMode = channel.FingerprintModeDevice
	if installationIdentity(affinityCtx("s1", 1), noSeed, clientInstallation) != installationIdentity(affinityCtx("s1", 1), poolRuntime("acct-a"), clientInstallation) {
		t.Fatal("device mode without a seed must fall back to the off-mode mapping")
	}
}

// TestCodexOutboundForwardsRewrittenTurnHeaders 冻结头转发：客户端的 x-codex-turn-metadata / x-codex-window-id
// 经同一命名空间改写后出站，与 body 内 client_metadata 完全一致；客户端未发时不发；原始 id 不外泄。
func TestCodexOutboundForwardsRewrittenTurnHeaders(t *testing.T) {
	embedded := `{"installation_id":"` + clientInstallation + `","session_id":"` + clientThread + `","thread_id":"` + clientThread + `","turn_id":"` + clientTurn + `","window_id":"` + clientThread + `:2","request_kind":"turn","sandbox":"seatbelt"}`
	ctx := turnCtx(clientThread, 7, embedded, clientThread+":2")

	var gotHeader http.Header
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Clone()
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.completed\n" + `data: {"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","model":"gpt-5.5","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}` + "\n\n"))
	}))
	defer server.Close()

	ch := channel.Runtime{Origin: server.URL, APIKey: "tok", Account: channel.AccountIdentity{ID: 1, UpstreamAccountID: "acct-a"}}
	id := upstreamSessionID(ctx, ch)
	if _, err := NewAdapter(server.Client(), nil, nil).CreateResponse(ctx, ch, openairesponses.Request{Body: json.RawMessage(clientMetadataBody())}); err != nil {
		t.Fatalf("CreateResponse: %v", err)
	}

	if got := gotHeader.Get("x-codex-window-id"); got != id+":2" {
		t.Fatalf("x-codex-window-id = %q, want %s:2", got, id)
	}
	var headerMeta map[string]any
	if err := json.Unmarshal([]byte(gotHeader.Get("x-codex-turn-metadata")), &headerMeta); err != nil {
		t.Fatalf("forwarded turn metadata invalid: %v (%q)", err, gotHeader.Get("x-codex-turn-metadata"))
	}
	var body struct {
		ClientMetadata map[string]any `json:"client_metadata"`
	}
	if err := json.Unmarshal(gotBody, &body); err != nil {
		t.Fatalf("body: %v", err)
	}
	for _, field := range []string{"session_id", "thread_id", "turn_id"} {
		if headerMeta[field] != body.ClientMetadata[field] {
			t.Fatalf("header/body %s drift: %v vs %v", field, headerMeta[field], body.ClientMetadata[field])
		}
	}
	if headerMeta["installation_id"] != body.ClientMetadata["x-codex-installation-id"] {
		t.Fatalf("installation drift: header %v vs body %v", headerMeta["installation_id"], body.ClientMetadata["x-codex-installation-id"])
	}
	if headerMeta["window_id"] != id+":2" || headerMeta["sandbox"] != "seatbelt" {
		t.Fatalf("window/sandbox not preserved as expected: %v", headerMeta)
	}
	for name, values := range gotHeader {
		for _, v := range values {
			for _, leaked := range []string{clientThread, clientInstallation, clientTurn} {
				if strings.Contains(v, leaked) {
					t.Fatalf("header %s leaks client identity %q", name, leaked)
				}
			}
		}
	}
}

// TestCodexOutboundOmitsTurnHeadersWhenClientDidNotSend 冻结「客户端没发就不发」与「无亲和不改写」。
func TestCodexOutboundOmitsTurnHeadersWhenClientDidNotSend(t *testing.T) {
	var gotHeader http.Header
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Clone()
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.completed\n" + `data: {"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","model":"gpt-5.5","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}` + "\n\n"))
	}))
	defer server.Close()
	ch := channel.Runtime{Origin: server.URL, APIKey: "tok", Account: channel.AccountIdentity{ID: 1, UpstreamAccountID: "acct-a"}}

	// 有亲和、无客户端头：只发会话亲和头，不伪造 turn 头。
	if _, err := NewAdapter(server.Client(), nil, nil).CreateResponse(affinityCtx(clientThread, 7), ch, openairesponses.Request{Body: json.RawMessage(clientMetadataBody())}); err != nil {
		t.Fatalf("CreateResponse: %v", err)
	}
	if gotHeader.Get("x-codex-turn-metadata") != "" || gotHeader.Get("x-codex-window-id") != "" {
		t.Fatalf("turn headers must not be synthesized when the client did not send them: %v", gotHeader)
	}

	// 无亲和（探测）：身份字段零改动（wire 归一化只动 stream 等协议字段，不碰身份），也不发会话头。
	body := clientMetadataBody()
	if _, err := NewAdapter(server.Client(), nil, nil).CreateResponse(context.Background(), ch, openairesponses.Request{Body: json.RawMessage(body)}); err != nil {
		t.Fatalf("CreateResponse: %v", err)
	}
	// wire 归一化会重编码 body（键序可能变化），因此按语义比较。
	wantMetadata, _, _, _ := jsonparser.Get([]byte(body), "client_metadata")
	gotMetadata, _, _, _ := jsonparser.Get(gotBody, "client_metadata")
	var wantObj, gotObj map[string]any
	if err := json.Unmarshal(wantMetadata, &wantObj); err != nil {
		t.Fatalf("want metadata: %v", err)
	}
	if err := json.Unmarshal(gotMetadata, &gotObj); err != nil {
		t.Fatalf("got metadata: %v", err)
	}
	if !reflect.DeepEqual(wantObj, gotObj) {
		t.Fatalf("without affinity client_metadata must pass through unchanged:\n got %s\nwant %s", gotMetadata, wantMetadata)
	}
	if pck, _ := jsonparser.GetString(gotBody, "prompt_cache_key"); pck != clientThread {
		t.Fatalf("without affinity prompt_cache_key must be untouched, got %q", pck)
	}
	if gotHeader.Get("session-id") != "" || gotHeader.Get("x-codex-turn-metadata") != "" {
		t.Fatalf("without affinity no session headers may be sent: %v", gotHeader)
	}
}
