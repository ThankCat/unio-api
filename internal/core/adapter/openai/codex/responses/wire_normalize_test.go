package codexresponses

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	openairesponses "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/responses"
	"github.com/ThankCat/unio-gateway/internal/core/channel"
)

// TestNewAdapterNormalizesAndAggregates 冻结 Codex wire 装配：非流式 + 字符串 input +
// 不支持字段入站，出站必须是流式结构化形态，客户端拿到终态 response JSON。
func TestNewAdapterNormalizesAndAggregates(t *testing.T) {
	var gotBody map[string]any
	responseObj := `{"id":"resp_codex","object":"response","status":"completed","model":"gpt-5.4","usage":{"input_tokens":4,"output_tokens":1,"total_tokens":5}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != responsesPath {
			t.Errorf("path = %q, want %s", r.URL.Path, responsesPath)
		}
		if r.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("accept = %q, want text/event-stream", r.Header.Get("Accept"))
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &gotBody); err != nil {
			t.Errorf("outbound body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.completed\n" + `data: {"type":"response.completed","response":` + responseObj + `}` + "\n\n"))
	}))
	defer server.Close()

	a := NewAdapter(server.Client(), nil, nil)
	got, err := a.CreateResponse(context.Background(), channel.Runtime{
		Origin: server.URL,
		APIKey: "tok",
	}, openairesponses.Request{Body: json.RawMessage(`{
		"model":"gpt-5.4",
		"stream":false,
		"store":true,
		"temperature":0.2,
		"max_output_tokens":32,
		"input":"hi"
	}`)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotBody["store"] != false || gotBody["stream"] != true {
		t.Fatalf("store/stream = %v/%v, want false/true", gotBody["store"], gotBody["stream"])
	}
	if _, ok := gotBody["temperature"]; ok {
		t.Fatal("temperature must be stripped")
	}
	if _, ok := gotBody["max_output_tokens"]; ok {
		t.Fatal("max_output_tokens must be stripped")
	}
	items, ok := gotBody["input"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("input = %#v, want structured array", gotBody["input"])
	}
	if string(got.Raw) != responseObj {
		t.Fatalf("aggregated raw = %s, want terminal response object", got.Raw)
	}
}
