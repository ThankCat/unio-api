package responses

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/core/usage"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

// forceStreamWire 模拟 Codex 这类「只收流式」的 wire：出站前把 stream 置 true。
func forceStreamWire() Wire {
	return Wire{
		ForceStreaming: true,
		NormalizeRequest: func(body []byte, stream bool) ([]byte, error) {
			var payload map[string]any
			if err := json.Unmarshal(body, &payload); err != nil {
				return body, nil
			}
			payload["stream"] = stream
			return json.Marshal(payload)
		},
	}
}

func TestCreateResponseForceStreamingAggregatesTerminalJSON(t *testing.T) {
	var (
		gotAccept string
		gotBody   map[string]any
	)
	responseObj := `{"id":"resp_agg","object":"response","status":"completed","model":"gpt-5.4","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"pong"}]}],"usage":{"input_tokens":8,"output_tokens":2,"total_tokens":10}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAccept = r.Header.Get("Accept")
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &gotBody); err != nil {
			t.Errorf("upstream body not JSON: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.created\n" + `data: {"type":"response.created","response":{"id":"resp_agg","status":"in_progress"}}` + "\n\n"))
		_, _ = w.Write([]byte("event: response.output_text.delta\n" + `data: {"type":"response.output_text.delta","delta":"pong"}` + "\n\n"))
		_, _ = w.Write([]byte("event: response.completed\n" + `data: {"type":"response.completed","response":` + responseObj + `}` + "\n\n"))
	}))
	defer server.Close()

	a := NewAdapterWithWire(server.Client(), forceStreamWire())
	got, err := a.CreateResponse(context.Background(), testChannel(server.URL), Request{
		Body: json.RawMessage(`{"model":"gpt-5.4","stream":false,"input":"hi"}`),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if gotAccept != "text/event-stream" {
		t.Fatalf("accept = %q, want text/event-stream (forced stream outbound)", gotAccept)
	}
	if gotBody["stream"] != true {
		t.Fatalf("outbound stream = %v, want true", gotBody["stream"])
	}
	if string(got.Raw) != responseObj {
		t.Fatalf("aggregated raw mismatch:\n got %s\nwant %s", got.Raw, responseObj)
	}
	if got.ResponseID != "resp_agg" || got.Model != "gpt-5.4" {
		t.Fatalf("id/model = %q/%q, want resp_agg/gpt-5.4", got.ResponseID, got.Model)
	}
	if got.Usage.PromptTokens != 8 || got.Usage.CompletionTokens != 2 || got.Usage.TotalTokens != 10 {
		t.Fatalf("usage = %+v, want 8/2/10", got.Usage)
	}
	if got.Facts.UsageSource != usage.SourceUpstreamStream {
		t.Fatalf("facts usage source = %q, want upstream stream", got.Facts.UsageSource)
	}
	if got.Facts.Finish.Class != adapter.FinishStop {
		t.Fatalf("facts finish = %+v, want stop", got.Facts.Finish)
	}
	if got.Upstream.StatusCode != http.StatusOK {
		t.Fatalf("upstream status = %d, want 200", got.Upstream.StatusCode)
	}
}

// TestCreateResponseForceStreamingBackfillsEmptyTerminalOutput 冻结 Codex 订阅后端的真机形态：
// completed 事件的 response.output 恒为空数组，内容只在 response.output_item.done 里——
// 聚合必须用逐项终态原文回填 output，非流式客户端才拿得到内容。
func TestCreateResponseForceStreamingBackfillsEmptyTerminalOutput(t *testing.T) {
	reasoningItem := `{"id":"rs_1","type":"reasoning","summary":[]}`
	messageItem := `{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"pong"}]}`
	terminalResponse := `{"id":"resp_bf","object":"response","status":"completed","model":"gpt-5.4","output":[],"usage":{"input_tokens":8,"output_tokens":2,"total_tokens":10}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.output_item.added\n" + `data: {"type":"response.output_item.added","output_index":0,"item":{"id":"rs_1","type":"reasoning","summary":[]}}` + "\n\n"))
		_, _ = w.Write([]byte("event: response.output_item.done\n" + `data: {"type":"response.output_item.done","output_index":0,"item":` + reasoningItem + `}` + "\n\n"))
		_, _ = w.Write([]byte("event: response.output_text.delta\n" + `data: {"type":"response.output_text.delta","delta":"pong"}` + "\n\n"))
		_, _ = w.Write([]byte("event: response.output_item.done\n" + `data: {"type":"response.output_item.done","output_index":1,"item":` + messageItem + `}` + "\n\n"))
		_, _ = w.Write([]byte("event: response.completed\n" + `data: {"type":"response.completed","response":` + terminalResponse + `}` + "\n\n"))
	}))
	defer server.Close()

	got, err := NewAdapterWithWire(server.Client(), forceStreamWire()).CreateResponse(
		context.Background(), testChannel(server.URL),
		Request{Body: json.RawMessage(`{"model":"gpt-5.4","stream":false,"input":"hi"}`)},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var decoded struct {
		ID     string            `json:"id"`
		Status string            `json:"status"`
		Output []json.RawMessage `json:"output"`
		Usage  struct {
			TotalTokens int64 `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(got.Raw, &decoded); err != nil {
		t.Fatalf("aggregated raw not JSON: %v", err)
	}
	if decoded.ID != "resp_bf" || decoded.Status != "completed" || decoded.Usage.TotalTokens != 10 {
		t.Fatalf("terminal fields lost: %+v", decoded)
	}
	if len(decoded.Output) != 2 {
		t.Fatalf("output len = %d, want 2 backfilled items", len(decoded.Output))
	}
	if string(decoded.Output[0]) != reasoningItem {
		t.Fatalf("output[0] = %s, want verbatim reasoning item", decoded.Output[0])
	}
	if string(decoded.Output[1]) != messageItem {
		t.Fatalf("output[1] = %s, want verbatim message item", decoded.Output[1])
	}
}

func TestBackfillAggregatedOutputKeepsNonEmptyTerminal(t *testing.T) {
	terminal := json.RawMessage(`{"id":"resp_1","output":[{"type":"message"}]}`)
	items := []json.RawMessage{json.RawMessage(`{"type":"message","id":"other"}`)}
	if got := backfillAggregatedOutput(terminal, items); string(got) != string(terminal) {
		t.Fatalf("non-empty terminal output must stay verbatim, got %s", got)
	}
}

func TestCreateResponseForceStreamingIncompleteStillAggregates(t *testing.T) {
	responseObj := `{"id":"resp_cut","status":"incomplete","model":"gpt-5.4","incomplete_details":{"reason":"max_output_tokens"},"usage":{"input_tokens":3,"output_tokens":9,"total_tokens":12}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.incomplete\n" + `data: {"type":"response.incomplete","response":` + responseObj + `}` + "\n\n"))
	}))
	defer server.Close()

	got, err := NewAdapterWithWire(server.Client(), forceStreamWire()).CreateResponse(
		context.Background(), testChannel(server.URL),
		Request{Body: json.RawMessage(`{"model":"gpt-5.4","stream":false}`)},
	)
	if err != nil {
		t.Fatalf("incomplete terminal must still aggregate: %v", err)
	}
	if string(got.Raw) != responseObj {
		t.Fatalf("raw = %s, want incomplete response object", got.Raw)
	}
	if got.Facts.Finish.Class != adapter.FinishLength {
		t.Fatalf("finish class = %q, want length", got.Facts.Finish.Class)
	}
}

func TestCreateResponseForceStreamingMissedTerminal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.created\n" + `data: {"type":"response.created","response":{"id":"resp_x"}}` + "\n\n"))
	}))
	defer server.Close()

	_, err := NewAdapterWithWire(server.Client(), forceStreamWire()).CreateResponse(
		context.Background(), testChannel(server.URL),
		Request{Body: json.RawMessage(`{"model":"gpt-5.4"}`)},
	)
	if err == nil {
		t.Fatal("expected error when stream misses terminal")
	}
	if failure.CodeOf(err) != failure.CodeAdapterReadStreamFailed {
		t.Fatalf("code = %q, want %q", failure.CodeOf(err), failure.CodeAdapterReadStreamFailed)
	}
}

func TestCreateResponseForceStreamingPropagatesUpstreamStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	defer server.Close()

	_, err := NewAdapterWithWire(server.Client(), forceStreamWire()).CreateResponse(
		context.Background(), testChannel(server.URL),
		Request{Body: json.RawMessage(`{"model":"gpt-5.4"}`)},
	)
	if err == nil {
		t.Fatal("expected upstream 429 to surface")
	}
	cat, ok := adapter.UpstreamCategoryOf(err)
	if !ok || cat != adapter.UpstreamErrorRateLimit {
		t.Fatalf("category = %q ok=%v, want rate_limit", cat, ok)
	}
}

func TestCreateResponseOfficialWireStillPassthroughJSON(t *testing.T) {
	var gotAccept string
	respBody := `{"id":"resp_off","status":"completed","model":"gpt-5.4","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAccept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(respBody))
	}))
	defer server.Close()

	got, err := NewAdapter(server.Client()).CreateResponse(
		context.Background(), testChannel(server.URL),
		Request{Body: json.RawMessage(`{"model":"gpt-5.4","stream":false}`)},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotAccept == "text/event-stream" {
		t.Fatal("official wire must not force streaming")
	}
	if string(got.Raw) != respBody {
		t.Fatalf("official wire raw mismatch: %s", got.Raw)
	}
	if got.Facts.UsageSource != usage.SourceUpstreamResponse {
		t.Fatalf("official facts source = %q, want upstream response", got.Facts.UsageSource)
	}
}
