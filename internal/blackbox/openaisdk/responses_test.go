//go:build blackbox

package openaisdk_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/blackbox/sdkfixture"
)

// responses_test.go 覆盖 OpenAI Responses API（Codex 兼容）的 mock 黑盒（TASK-11.15）：
// 客户直接用原始 JSON 打 unio gateway 的 /v1/responses*（不经 openai-go SDK，贴近 Codex
// 实际行为），mock DeepSeek 的 chat completions 上游，验证 responses→chat 桥接的对外契约、
// thinking opt-in 闸门（DEC-016）、无状态边界与账务事实。
//
// 全部用例依赖真实 PostgreSQL + Redis（sdkfixture.Setup 在缺 DATABASE_URL 时 t.Skip）。

// doResponses 用原始 JSON body 打 gateway 的 responses origin，返回原始 http 响应。
func doResponses(t *testing.T, method, url, apiKey, body string) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	return resp
}

// responsesBody 是 Responses 非流式响应的最小客户视角解析结构。
type responsesBody struct {
	ID     string `json:"id"`
	Object string `json:"object"`
	Status string `json:"status"`
	Output []struct {
		Type    string `json:"type"`
		Role    string `json:"role"`
		Status  string `json:"status"`
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

// responsesErrorBody 是 Responses 原生 error 响应的解析结构。
type responsesErrorBody struct {
	Error struct {
		Type    string  `json:"type"`
		Code    string  `json:"code"`
		Message string  `json:"message"`
		Param   *string `json:"param"`
	} `json:"error"`
}

func decodeResponsesError(t *testing.T, resp *http.Response) responsesErrorBody {
	t.Helper()
	var eb responsesErrorBody
	if err := json.NewDecoder(resp.Body).Decode(&eb); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	return eb
}

// OAI-RESP-Mock-01：非流式 /v1/responses 成功，并端到端验证 thinking opt-in 闸门。
//
// 验证：
//   - 客户拿到 Responses 形状响应（object=response，output[0] 是 assistant message，文本=上游内容）；
//   - usage 由 chat usage 映射（input/output/total）；
//   - 上游收到的是合法 OpenAI Chat Completions（含 messages、stream=false）；
//   - **DEC-016**：请求未带 reasoning → 桥接置 ReasoningDisabled → DeepSeek adapter 出站注入
//     thinking:{type:"disabled"}，避免非 reasoning run 触发上游思考。
func TestResponsesMockNonStreamSucceeds(t *testing.T) {
	var capturedBody []byte
	mock := newMockUpstream(t, func(_ *testing.T, w http.ResponseWriter, _ *http.Request, body []byte) {
		capturedBody = body
		writeMockChatCompletion(w, "deepseek-resp-mock", "ok", 6, 1)
	})
	t.Cleanup(mock.Close)

	f := sdkfixture.Setup(t, sdkfixture.SetupOptions{
		Mode:            sdkfixture.UpstreamMock,
		UpstreamBaseURL: mock.URL,
	})

	resp := doResponses(t, http.MethodPost, f.BaseURL+"/responses", f.APIKey,
		`{"model":"`+f.ModelID+`","input":"hello","stream":false}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d body=%s", resp.StatusCode, string(raw))
	}

	var rb responsesBody
	if err := json.NewDecoder(resp.Body).Decode(&rb); err != nil {
		t.Fatalf("decode responses body: %v", err)
	}
	if rb.Object != "response" {
		t.Errorf("object = %q, want response", rb.Object)
	}
	if len(rb.Output) == 0 || rb.Output[0].Type != "message" || rb.Output[0].Role != "assistant" {
		t.Fatalf("unexpected output: %+v", rb.Output)
	}
	if len(rb.Output[0].Content) == 0 || rb.Output[0].Content[0].Type != "output_text" || rb.Output[0].Content[0].Text != "ok" {
		t.Fatalf("unexpected output content: %+v", rb.Output[0].Content)
	}
	if rb.Usage == nil || rb.Usage.InputTokens != 6 || rb.Usage.OutputTokens != 1 || rb.Usage.TotalTokens != 7 {
		t.Fatalf("unexpected usage: %+v", rb.Usage)
	}

	var upstream map[string]any
	if err := json.Unmarshal(capturedBody, &upstream); err != nil {
		t.Fatalf("upstream body not valid json: %v (raw: %s)", err, string(capturedBody))
	}
	if model, _ := upstream["model"].(string); model != f.UpstreamModel {
		t.Errorf("upstream model = %q, want %q", model, f.UpstreamModel)
	}
	if stream, ok := upstream["stream"].(bool); ok && stream {
		t.Errorf("expected upstream stream=false")
	}
	if msgs, ok := upstream["messages"].([]any); !ok || len(msgs) == 0 {
		t.Errorf("expected upstream messages array, got %v", upstream["messages"])
	}
	// DEC-016：reasoning 缺省 → thinking:{type:"disabled"} 必须出现在上游 body。
	thinking, ok := upstream["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("expected upstream thinking:{type:disabled}, got %v (raw: %s)", upstream["thinking"], string(capturedBody))
	}
	if thinking["type"] != "disabled" {
		t.Errorf("upstream thinking.type = %v, want disabled", thinking["type"])
	}
}

// OAI-RESP-Mock-02：非流式 /v1/responses 的账务事实与 chat 等价，endpoint 记为 responses。
func TestResponsesMockSettlementWritesAuditTrail(t *testing.T) {
	mock := newMockUpstream(t, func(_ *testing.T, w http.ResponseWriter, _ *http.Request, _ []byte) {
		writeMockChatCompletion(w, "deepseek-resp-settle", "settle ok", 100, 50)
	})
	t.Cleanup(mock.Close)

	f := sdkfixture.Setup(t, sdkfixture.SetupOptions{
		Mode:            sdkfixture.UpstreamMock,
		UpstreamBaseURL: mock.URL,
	})

	resp := doResponses(t, http.MethodPost, f.BaseURL+"/responses", f.APIKey,
		`{"model":"`+f.ModelID+`","input":"hi","stream":false}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// 同步结算路径，留 200ms buffer。
	time.Sleep(200 * time.Millisecond)

	dbCtx, dbCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dbCancel()

	var (
		rrID        int64
		rrStatus    string
		rrIngress   string
		rrEndpoint string
		rrModelID   string
	)
	if err := f.Pool.QueryRow(dbCtx, `
		SELECT id, status, ingress_protocol, endpoint, requested_model_id
		FROM request_records
		WHERE user_id = $1
		ORDER BY id DESC
		LIMIT 1
	`, f.UserID).Scan(&rrID, &rrStatus, &rrIngress, &rrEndpoint, &rrModelID); err != nil {
		t.Fatalf("query request_records: %v", err)
	}
	if rrStatus != "succeeded" {
		t.Errorf("request_records.status = %q, want succeeded", rrStatus)
	}
	if rrIngress != "openai" {
		t.Errorf("request_records.ingress_protocol = %q, want openai", rrIngress)
	}
	// 关键差异：responses ingress 的 endpoint 必须是 responses（migration 000009 放开的枚举）。
	if rrEndpoint != "responses" {
		t.Errorf("request_records.endpoint = %q, want responses", rrEndpoint)
	}
	if rrModelID != f.ModelID {
		t.Errorf("request_records.requested_model_id = %q, want %q", rrModelID, f.ModelID)
	}

	// usage_records：prompt=100→uncached_input，completion=50→output。
	var usageUncached, usageOutput int64
	if err := f.Pool.QueryRow(dbCtx, `
		SELECT uncached_input_tokens, output_tokens_total
		FROM usage_records WHERE request_record_id = $1
	`, rrID).Scan(&usageUncached, &usageOutput); err != nil {
		t.Fatalf("query usage_records: %v", err)
	}
	if usageUncached != 100 || usageOutput != 50 {
		t.Errorf("usage = (uncached=%d,output=%d), want (100,50)", usageUncached, usageOutput)
	}

	// ledger debit + 价格/成本快照各一条。
	var debits int
	if err := f.Pool.QueryRow(dbCtx, `
		SELECT COUNT(*) FROM ledger_entries WHERE request_record_id = $1 AND entry_type = 'debit'
	`, rrID).Scan(&debits); err != nil {
		t.Fatalf("query ledger_entries: %v", err)
	}
	if debits < 1 {
		t.Errorf("expected >=1 debit ledger_entry, got %d", debits)
	}
	var priceSnaps, costSnaps int
	if err := f.Pool.QueryRow(dbCtx, `SELECT COUNT(*) FROM price_snapshots WHERE request_record_id = $1`, rrID).Scan(&priceSnaps); err != nil {
		t.Fatalf("query price_snapshots: %v", err)
	}
	if err := f.Pool.QueryRow(dbCtx, `SELECT COUNT(*) FROM cost_snapshots WHERE request_record_id = $1`, rrID).Scan(&costSnaps); err != nil {
		t.Fatalf("query cost_snapshots: %v", err)
	}
	if priceSnaps != 1 || costSnaps != 1 {
		t.Errorf("snapshots = (price=%d,cost=%d), want (1,1)", priceSnaps, costSnaps)
	}
}

// OAI-RESP-Mock-03：background:true 明确 400 拒绝（无状态商业承诺，不静默转同步）。
func TestResponsesBackgroundRejected(t *testing.T) {
	mock := newMockUpstream(t, func(_ *testing.T, w http.ResponseWriter, _ *http.Request, _ []byte) {
		writeMockChatCompletion(w, "should-not-be-called", "x", 1, 1)
	})
	t.Cleanup(mock.Close)

	f := sdkfixture.Setup(t, sdkfixture.SetupOptions{
		Mode:            sdkfixture.UpstreamMock,
		UpstreamBaseURL: mock.URL,
	})

	resp := doResponses(t, http.MethodPost, f.BaseURL+"/responses", f.APIKey,
		`{"model":"`+f.ModelID+`","input":"hi","background":true}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	eb := decodeResponsesError(t, resp)
	if eb.Error.Code != "unsupported_background" {
		t.Errorf("error.code = %q, want unsupported_background", eb.Error.Code)
	}
	if mock.calls != 0 {
		t.Errorf("expected no upstream call for background reject, got %d", mock.calls)
	}
}

// OAI-RESP-Mock-04：有状态 origin 统一 501 unsupported_origin_stateless（无服务端存储）。
func TestResponsesStatelessUnsupported(t *testing.T) {
	mock := newMockUpstream(t, func(_ *testing.T, w http.ResponseWriter, _ *http.Request, _ []byte) {
		writeMockChatCompletion(w, "should-not-be-called", "x", 1, 1)
	})
	t.Cleanup(mock.Close)

	f := sdkfixture.Setup(t, sdkfixture.SetupOptions{
		Mode:            sdkfixture.UpstreamMock,
		UpstreamBaseURL: mock.URL,
	})

	cases := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/responses/resp_123"},
		{http.MethodDelete, "/responses/resp_123"},
		{http.MethodGet, "/responses/resp_123/input_items"},
		{http.MethodPost, "/responses/resp_123/cancel"},
	}
	for _, tc := range cases {
		resp := doResponses(t, tc.method, f.BaseURL+tc.path, f.APIKey, "")
		if resp.StatusCode != http.StatusNotImplemented {
			resp.Body.Close()
			t.Fatalf("%s %s: expected 501, got %d", tc.method, tc.path, resp.StatusCode)
		}
		eb := decodeResponsesError(t, resp)
		resp.Body.Close()
		if eb.Error.Code != "unsupported_origin_stateless" {
			t.Errorf("%s %s: code = %q, want unsupported_origin_stateless", tc.method, tc.path, eb.Error.Code)
		}
	}
	if mock.calls != 0 {
		t.Errorf("expected no upstream call for stateless origins, got %d", mock.calls)
	}
}

// OAI-RESP-Mock-05：/v1/responses/compact 无状态降级摘要，返回 {"output":[message]}。
func TestResponsesCompact(t *testing.T) {
	mock := newMockUpstream(t, func(_ *testing.T, w http.ResponseWriter, _ *http.Request, _ []byte) {
		writeMockChatCompletion(w, "deepseek-compact", "SUMMARY", 200, 10)
	})
	t.Cleanup(mock.Close)

	f := sdkfixture.Setup(t, sdkfixture.SetupOptions{
		Mode:            sdkfixture.UpstreamMock,
		UpstreamBaseURL: mock.URL,
	})

	resp := doResponses(t, http.MethodPost, f.BaseURL+"/responses/compact", f.APIKey,
		`{"model":"`+f.ModelID+`","input":"a very long earlier conversation to compact","instructions":"compact the conversation"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d body=%s", resp.StatusCode, string(raw))
	}

	var cb struct {
		Output []struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&cb); err != nil {
		t.Fatalf("decode compact body: %v", err)
	}
	if len(cb.Output) != 1 || cb.Output[0].Type != "message" || cb.Output[0].Role != "assistant" {
		t.Fatalf("unexpected compact output: %+v", cb.Output)
	}
	if len(cb.Output[0].Content) == 0 || cb.Output[0].Content[0].Text != "SUMMARY" {
		t.Fatalf("unexpected compact content: %+v", cb.Output[0].Content)
	}
}

// OAI-RESP-Mock-06：/v1/responses/input_tokens 本地估算，不调上游、不计费。
func TestResponsesInputTokens(t *testing.T) {
	mock := newMockUpstream(t, func(_ *testing.T, w http.ResponseWriter, _ *http.Request, _ []byte) {
		writeMockChatCompletion(w, "should-not-be-called", "x", 1, 1)
	})
	t.Cleanup(mock.Close)

	f := sdkfixture.Setup(t, sdkfixture.SetupOptions{
		Mode:            sdkfixture.UpstreamMock,
		UpstreamBaseURL: mock.URL,
	})

	resp := doResponses(t, http.MethodPost, f.BaseURL+"/responses/input_tokens", f.APIKey,
		`{"model":"`+f.ModelID+`","input":"count the tokens in this prompt please"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d body=%s", resp.StatusCode, string(raw))
	}

	var ib struct {
		InputTokens int    `json:"input_tokens"`
		Object      string `json:"object"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ib); err != nil {
		t.Fatalf("decode input_tokens body: %v", err)
	}
	if ib.Object != "response.input_tokens" {
		t.Errorf("object = %q, want response.input_tokens", ib.Object)
	}
	if ib.InputTokens <= 0 {
		t.Errorf("input_tokens = %d, want > 0 (local estimate)", ib.InputTokens)
	}
	// 本地估算不触达上游。
	if mock.calls != 0 {
		t.Errorf("expected no upstream call for input_tokens, got %d", mock.calls)
	}
}
