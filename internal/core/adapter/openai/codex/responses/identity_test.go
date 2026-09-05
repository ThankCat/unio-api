package codexresponses

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	openairesponses "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/responses"
	"github.com/ThankCat/unio-gateway/internal/core/channel"
	"github.com/ThankCat/unio-gateway/internal/core/codexidentity"
)

// TestCodexOutboundIdentityHeaders 冻结推理面与模型发现面的客户端身份：三个头由 codexidentity
// 同源渲染（originator == UA 首段，version == UA 版本段），版本随注入的来源热变化，
// 模型清单的 client_version 与请求头 version 一致，且不再出现网关自有标记。
func TestCodexOutboundIdentityHeaders(t *testing.T) {
	version := "0.152.1"
	source := func() string { return version }

	var gotHeaders []http.Header
	var gotQuery []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = append(gotHeaders, r.Header.Clone())
		gotQuery = append(gotQuery, r.URL.Query().Get("client_version"))
		switch r.URL.Path {
		case modelsPath:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"models":[{"slug":"gpt-5.5","display_name":"GPT-5.5","visibility":"list"}]}`))
		default:
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("event: response.completed\n" + `data: {"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","model":"gpt-5.5","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}` + "\n\n"))
		}
	}))
	defer server.Close()

	ch := channel.Runtime{Origin: server.URL, APIKey: "tok", Account: channel.AccountIdentity{ID: 1, UpstreamAccountID: "acct"}}
	adapter := NewAdapter(server.Client(), nil, source)
	lister := NewModelLister(server.Client(), nil, source)

	body := json.RawMessage(`{"model":"gpt-5.5","store":false,"stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)
	if _, err := adapter.CreateResponse(context.Background(), ch, openairesponses.Request{Body: body}); err != nil {
		t.Fatalf("CreateResponse: %v", err)
	}
	version = "0.160.0"
	if _, err := lister.ListModels(context.Background(), ch); err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(gotHeaders) != 2 {
		t.Fatalf("expected 2 upstream calls, got %d", len(gotHeaders))
	}

	for i, h := range gotHeaders {
		ua := h.Get("User-Agent")
		originator := h.Get("originator")
		ver := h.Get("version")
		if originator != codexidentity.Originator {
			t.Fatalf("call %d originator = %q, want %q", i, originator, codexidentity.Originator)
		}
		if !strings.HasPrefix(ua, originator+"/"+ver+" ") || !strings.HasSuffix(ua, "("+originator+"; "+ver+")") {
			t.Fatalf("call %d identity drift: originator=%q version=%q ua=%q", i, originator, ver, ua)
		}
		if strings.Contains(strings.ToLower(ua), "unio") {
			t.Fatalf("call %d user agent leaks gateway marker: %q", i, ua)
		}
	}
	if gotHeaders[0].Get("version") != "0.152.1" {
		t.Fatalf("first call version = %q, want 0.152.1", gotHeaders[0].Get("version"))
	}
	if gotHeaders[1].Get("version") != "0.160.0" || gotQuery[1] != "0.160.0" {
		t.Fatalf("model discovery must follow the live version: header=%q query=%q", gotHeaders[1].Get("version"), gotQuery[1])
	}
}

// TestCodexOutboundIdentityFloorsStaleVersion 冻结版本下限：来源给出比基线更旧的版本时按基线出站。
func TestCodexOutboundIdentityFloorsStaleVersion(t *testing.T) {
	var got http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.completed\n" + `data: {"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","model":"gpt-5.5","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}` + "\n\n"))
	}))
	defer server.Close()

	adapter := NewAdapter(server.Client(), nil, func() string { return "0.144.0" })
	body := json.RawMessage(`{"model":"gpt-5.5","store":false,"stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)
	if _, err := adapter.CreateResponse(context.Background(), channel.Runtime{Origin: server.URL, APIKey: "tok"}, openairesponses.Request{Body: body}); err != nil {
		t.Fatalf("CreateResponse: %v", err)
	}
	if got.Get("version") != codexidentity.BaselineVersion {
		t.Fatalf("stale version must floor to baseline, got %q", got.Get("version"))
	}
}
