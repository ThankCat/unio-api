//go:build blackbox

package openaisdk_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/blackbox/sdkfixture"
)

// responses_direct_test.go 覆盖「上游 Responses 直传」黑盒：channel 绑定 adapter_key=openai
// （官方 1P key 同时含 chat + responses 两组能力），上游原生支持 POST /responses，gateway 直连
// 上游零结构转换（仅改写 model 回显），账务/审计仍走统一 AttemptRunner + settlement。与
// responses_test.go（chat 桥接）对照，验证分流两条路径对外契约一致、账务一致。全部用例依赖
// 真实 PostgreSQL + Redis（sdkfixture.Setup 在缺 DATABASE_URL 时 t.Skip）。

// mockResponsesUpstream 是 fixture 用的原生 OpenAI Responses API 上游（POST /v1/responses）。
type mockResponsesUpstream struct {
	*httptest.Server
	calls int
}

// newMockResponsesUpstream 启一个原生 Responses 上游 mock，所有合法 POST /v1/responses 按 respond 处理。
func newMockResponsesUpstream(t *testing.T, respond func(t *testing.T, w http.ResponseWriter, r *http.Request, body []byte)) *mockResponsesUpstream {
	t.Helper()

	mock := &mockResponsesUpstream{}
	mock.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
			http.Error(w, fmt.Sprintf("unexpected upstream: %s %s", r.Method, r.URL.Path), http.StatusNotFound)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mock.calls++
		respond(t, w, r, body)
	}))
	return mock
}

// writeMockResponsesResponse 写一个最小可解析的原生 Responses 非流式 JSON 响应。
func writeMockResponsesResponse(w http.ResponseWriter, id, model, text string, inputTokens, outputTokens int) {
	resp := map[string]any{
		"id":     id,
		"object": "response",
		"status": "completed",
		"model":  model,
		"output": []map[string]any{{
			"type":   "message",
			"role":   "assistant",
			"status": "completed",
			"content": []map[string]any{{
				"type": "output_text",
				"text": text,
			}},
		}},
		"usage": map[string]any{
			"input_tokens":  inputTokens,
			"output_tokens": outputTokens,
			"total_tokens":  inputTokens + outputTokens,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Request-Id", "req-resp-direct-1")
	_ = json.NewEncoder(w).Encode(resp)
}

// responsesUpstreamEvent 是原生 Responses 上游 SSE 事件（event 名 + data JSON）。
type responsesUpstreamEvent struct {
	Type string
	Data map[string]any
}

// writeMockResponsesStream 按原生 Responses 命名事件协议写出 SSE（event: <type> + data: <json>）。
func writeMockResponsesStream(w http.ResponseWriter, events []responsesUpstreamEvent) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, _ := w.(http.Flusher)
	for _, ev := range events {
		data, _ := json.Marshal(ev.Data)
		_, _ = io.WriteString(w, "event: "+ev.Type+"\n")
		_, _ = io.WriteString(w, "data: "+string(data)+"\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}
}

func directResponsesOptions(mockURL string) sdkfixture.SetupOptions {
	return sdkfixture.SetupOptions{
		Mode:            sdkfixture.UpstreamMock,
		UpstreamBaseURL: mockURL,
		// 官方 1P key=openai 现含 responses 直传槽（HasResponses=true → 直传分流）。
		AdapterKey: "openai",
		// 用 test-only model id，避免与本地 dev DB 残留的真实模型行（如 gpt-5.5）撞 models_model_id_key。
		ModelID:       "blackbox-openai-responses-direct",
		UpstreamModel: "gpt-5.5-upstream",
	}
}

// directResponsesBody 是直传非流式响应的客户视角解析结构（含 model，用于回显改写断言）。
type directResponsesBody struct {
	ID     string `json:"id"`
	Object string `json:"object"`
	Status string `json:"status"`
	Model  string `json:"model"`
	Output []struct {
		Type    string `json:"type"`
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"output"`
	Usage *struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
}

// OAI-RESP-Direct-01：非流式 /v1/responses 直传成功——上游响应原文透传，仅 model 回显改写为客户请求名。
func TestResponsesDirectMockNonStreamSucceeds(t *testing.T) {
	var capturedBody []byte
	mock := newMockResponsesUpstream(t, func(_ *testing.T, w http.ResponseWriter, _ *http.Request, body []byte) {
		capturedBody = body
		writeMockResponsesResponse(w, "resp_up_1", "gpt-5.5-upstream", "hi from responses", 7, 3)
	})
	t.Cleanup(mock.Close)

	f := sdkfixture.Setup(t, directResponsesOptions(mock.URL))

	resp := doResponses(t, http.MethodPost, f.BaseURL+"/responses", f.APIKey,
		`{"model":"`+f.ModelID+`","input":"hello","stream":false}`)
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", resp.StatusCode, string(raw))
	}

	var rb directResponsesBody
	if err := json.Unmarshal(raw, &rb); err != nil {
		t.Fatalf("decode direct responses body: %v (raw: %s)", err, string(raw))
	}
	// 原文透传：上游 id 保留。
	if rb.ID != "resp_up_1" || rb.Object != "response" || rb.Status != "completed" {
		t.Fatalf("unexpected passthrough envelope: %+v", rb)
	}
	// model 回显改写为客户请求名（不是上游 model）。
	if rb.Model != f.ModelID {
		t.Errorf("response model = %q, want %q (rewritten)", rb.Model, f.ModelID)
	}
	if len(rb.Output) == 0 || rb.Output[0].Type != "message" || len(rb.Output[0].Content) == 0 || rb.Output[0].Content[0].Text != "hi from responses" {
		t.Fatalf("unexpected output: %+v", rb.Output)
	}
	if rb.Usage == nil || rb.Usage.InputTokens != 7 || rb.Usage.OutputTokens != 3 || rb.Usage.TotalTokens != 10 {
		t.Fatalf("unexpected usage: %+v", rb.Usage)
	}

	// 上送上游请求体：model→upstream model、stream=false。
	var upstream map[string]any
	if err := json.Unmarshal(capturedBody, &upstream); err != nil {
		t.Fatalf("upstream body not json: %v (raw: %s)", err, string(capturedBody))
	}
	if model, _ := upstream["model"].(string); model != f.UpstreamModel {
		t.Errorf("upstream model = %q, want %q", model, f.UpstreamModel)
	}
	if stream, ok := upstream["stream"].(bool); ok && stream {
		t.Errorf("expected upstream stream=false")
	}
}

// OAI-RESP-Direct-02：直传非流式账务事实与桥接等价，operation 记为 responses，usage 由上游 responses usage 落账。
func TestResponsesDirectMockSettlementWritesAuditTrail(t *testing.T) {
	mock := newMockResponsesUpstream(t, func(_ *testing.T, w http.ResponseWriter, _ *http.Request, _ []byte) {
		writeMockResponsesResponse(w, "resp_up_settle", "gpt-5.5-upstream", "settled", 120, 40)
	})
	t.Cleanup(mock.Close)

	f := sdkfixture.Setup(t, directResponsesOptions(mock.URL))

	resp := doResponses(t, http.MethodPost, f.BaseURL+"/responses", f.APIKey,
		`{"model":"`+f.ModelID+`","input":"hi","stream":false}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	time.Sleep(200 * time.Millisecond)

	dbCtx, dbCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dbCancel()

	var (
		rrID        int64
		rrStatus    string
		rrIngress   string
		rrOperation string
		rrModelID   string
	)
	if err := f.Pool.QueryRow(dbCtx, `
		SELECT id, status, ingress_protocol, operation, requested_model_id
		FROM request_records
		WHERE user_id = $1
		ORDER BY id DESC
		LIMIT 1
	`, f.UserID).Scan(&rrID, &rrStatus, &rrIngress, &rrOperation, &rrModelID); err != nil {
		t.Fatalf("query request_records: %v", err)
	}
	if rrStatus != "succeeded" {
		t.Errorf("request_records.status = %q, want succeeded", rrStatus)
	}
	if rrIngress != "openai" {
		t.Errorf("request_records.ingress_protocol = %q, want openai", rrIngress)
	}
	if rrOperation != "responses" {
		t.Errorf("request_records.operation = %q, want responses", rrOperation)
	}
	if rrModelID != f.ModelID {
		t.Errorf("request_records.requested_model_id = %q, want %q", rrModelID, f.ModelID)
	}

	// usage_records：直传上游 input_tokens=120→uncached_input，output_tokens=40→output。
	var usageUncached, usageOutput int64
	if err := f.Pool.QueryRow(dbCtx, `
		SELECT uncached_input_tokens, output_tokens_total
		FROM usage_records WHERE request_record_id = $1
	`, rrID).Scan(&usageUncached, &usageOutput); err != nil {
		t.Fatalf("query usage_records: %v", err)
	}
	if usageUncached != 120 || usageOutput != 40 {
		t.Errorf("usage = (uncached=%d,output=%d), want (120,40)", usageUncached, usageOutput)
	}

	var debits int
	if err := f.Pool.QueryRow(dbCtx, `
		SELECT COUNT(*) FROM ledger_entries WHERE request_record_id = $1 AND entry_type = 'debit'
	`, rrID).Scan(&debits); err != nil {
		t.Fatalf("query ledger_entries: %v", err)
	}
	if debits < 1 {
		t.Errorf("expected >=1 debit ledger_entry, got %d", debits)
	}
}

// OAI-RESP-Direct-03：流式 /v1/responses 直传——上游命名事件原文透传，仅 response.model 回显改写，
// response.completed 由上游下发（不二次补发），usage 落在 completed。
func TestResponsesDirectMockStreamSucceeds(t *testing.T) {
	var capturedBody []byte
	mock := newMockResponsesUpstream(t, func(_ *testing.T, w http.ResponseWriter, _ *http.Request, body []byte) {
		capturedBody = body
		writeMockResponsesStream(w, []responsesUpstreamEvent{
			{Type: "response.created", Data: map[string]any{"type": "response.created", "sequence_number": 0, "response": map[string]any{"id": "resp_up_stream", "model": "gpt-5.5-upstream", "status": "in_progress"}}},
			{Type: "response.output_item.added", Data: map[string]any{"type": "response.output_item.added", "sequence_number": 1, "item": map[string]any{"type": "message"}}},
			{Type: "response.output_text.delta", Data: map[string]any{"type": "response.output_text.delta", "sequence_number": 2, "delta": "hello "}},
			{Type: "response.output_text.delta", Data: map[string]any{"type": "response.output_text.delta", "sequence_number": 3, "delta": "world"}},
			{Type: "response.output_item.done", Data: map[string]any{"type": "response.output_item.done", "sequence_number": 4, "item": map[string]any{"type": "message"}}},
			{Type: "response.completed", Data: map[string]any{"type": "response.completed", "sequence_number": 5, "response": map[string]any{"id": "resp_up_stream", "model": "gpt-5.5-upstream", "status": "completed", "usage": map[string]any{"input_tokens": 5, "output_tokens": 3, "total_tokens": 8}}}},
		})
	})
	t.Cleanup(mock.Close)

	f := sdkfixture.Setup(t, directResponsesOptions(mock.URL))

	resp := doResponses(t, http.MethodPost, f.BaseURL+"/responses", f.APIKey,
		`{"model":"`+f.ModelID+`","input":"say hello world","stream":true}`)
	defer resp.Body.Close()

	rawBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", resp.StatusCode, string(rawBody))
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}

	events := readResponsesSSE(t, strings.NewReader(string(rawBody)))
	assertResponsesSSEInvariants(t, events)

	var text strings.Builder
	for _, ev := range events {
		if ev.Type == "response.output_text.delta" {
			text.WriteString(ev.Delta)
		}
	}
	if text.String() != "hello world" {
		t.Errorf("accumulated text = %q, want %q", text.String(), "hello world")
	}

	completed := events[len(events)-1]
	if completed.Response == nil || completed.Response.Usage == nil {
		t.Fatalf("response.completed missing usage: %+v", completed)
	}
	if u := completed.Response.Usage; u.InputTokens != 5 || u.OutputTokens != 3 || u.TotalTokens != 8 {
		t.Errorf("completed usage = %+v, want 5/3/8", u)
	}

	// model 回显改写为客户请求名，上游 model 不泄漏给客户。
	if !strings.Contains(string(rawBody), `"model":"`+f.ModelID+`"`) {
		t.Errorf("client SSE missing rewritten model %q\nbody: %s", f.ModelID, string(rawBody))
	}
	if strings.Contains(string(rawBody), f.UpstreamModel) {
		t.Errorf("client SSE leaked upstream model %q\nbody: %s", f.UpstreamModel, string(rawBody))
	}

	// 上送上游 stream=true。
	var upstream map[string]any
	if err := json.Unmarshal(capturedBody, &upstream); err != nil {
		t.Fatalf("upstream body not json: %v (raw: %s)", err, string(capturedBody))
	}
	if stream, _ := upstream["stream"].(bool); !stream {
		t.Errorf("expected upstream stream=true (raw: %s)", string(capturedBody))
	}
}

// OAI-RESP-Direct-04：上游用 200 包裹的协议失败（status=failed）映射成客户可见上游错误（502 upstream_error），
// 绝不误判为可计费成功响应。
func TestResponsesDirectMockUpstreamFailedMapsError(t *testing.T) {
	mock := newMockResponsesUpstream(t, func(_ *testing.T, w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp_up_fail","object":"response","status":"failed","model":"gpt-5.5-upstream","error":{"type":"server_error","code":"server_error","message":"boom"}}`)
	})
	t.Cleanup(mock.Close)

	f := sdkfixture.Setup(t, directResponsesOptions(mock.URL))

	resp := doResponses(t, http.MethodPost, f.BaseURL+"/responses", f.APIKey,
		`{"model":"`+f.ModelID+`","input":"hi","stream":false}`)
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected non-200 for upstream failed, got 200 body=%s", string(raw))
	}
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502 (upstream server error)", resp.StatusCode)
	}
	eb := decodeResponsesError(t, resp)
	if eb.Error.Code != "upstream_error" {
		t.Errorf("error.code = %q, want upstream_error", eb.Error.Code)
	}
}
