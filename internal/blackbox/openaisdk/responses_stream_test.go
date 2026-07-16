//go:build blackbox

package openaisdk_test

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/ThankCat/unio-gateway/internal/blackbox/sdkfixture"
)

// responses_stream_test.go 覆盖 Responses 流式 SSE 黑盒（TASK-11.15）：mock DeepSeek 的 chat
// 流，验证 gateway 把 chat SSE 翻译成 Responses 命名事件序列（codex-rs 消费子集）、单调
// sequence_number、usage 落在 response.completed，并用真实 Codex v0.130 抓包端到端回归。

// responsesSSEEvent 是 Responses 流式事件的最小客户视角解析结构。
type responsesSSEEvent struct {
	Type           string `json:"type"`
	SequenceNumber int64  `json:"sequence_number"`
	Delta          string `json:"delta"`
	Item           *struct {
		Type string `json:"type"`
	} `json:"item"`
	Response *struct {
		Usage *struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
			TotalTokens  int `json:"total_tokens"`
		} `json:"usage"`
	} `json:"response"`
}

// readResponsesSSE 解析 SSE 流，按空行分帧，取每帧 data: 的 JSON（type 在 data 内）。
func readResponsesSSE(t *testing.T, r io.Reader) []responsesSSEEvent {
	t.Helper()

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	var (
		events    []responsesSSEEvent
		dataLines []string
	)
	flush := func() {
		if len(dataLines) == 0 {
			return
		}
		raw := strings.Join(dataLines, "\n")
		dataLines = nil
		if raw == "[DONE]" {
			return
		}
		var ev responsesSSEEvent
		if err := json.Unmarshal([]byte(raw), &ev); err != nil {
			t.Fatalf("decode sse data %q: %v", raw, err)
		}
		events = append(events, ev)
	}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
		// event:/id:/retry:/comment 行忽略：事件类型已在 data JSON 的 type 字段内。
	}
	flush()
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan sse: %v", err)
	}
	return events
}

// assertResponsesSSEInvariants 校验所有 Responses 流共有的不变量：
// 首事件 created、尾事件 completed、sequence_number 从 0 起每事件 +1。
func assertResponsesSSEInvariants(t *testing.T, events []responsesSSEEvent) {
	t.Helper()
	if len(events) == 0 {
		t.Fatal("expected at least one SSE event")
	}
	if events[0].Type != "response.created" {
		t.Errorf("first event = %q, want response.created", events[0].Type)
	}
	last := events[len(events)-1]
	if last.Type != "response.completed" && last.Type != "response.incomplete" {
		t.Errorf("last event = %q, want response.completed/incomplete", last.Type)
	}
	for i, ev := range events {
		if ev.SequenceNumber != int64(i) {
			t.Errorf("event[%d].sequence_number = %d, want %d (monotonic from 0)", i, ev.SequenceNumber, i)
		}
	}
}

// hasEventType 判断事件序列中是否出现某类型。
func hasEventType(events []responsesSSEEvent, typ string) bool {
	for _, ev := range events {
		if ev.Type == typ {
			return true
		}
	}
	return false
}

// OAI-RESP-Mock-07：流式 /v1/responses 成功，事件序列与 usage 正确。
func TestResponsesMockStreamSucceeds(t *testing.T) {
	var capturedBody []byte
	mock := newMockUpstream(t, func(_ *testing.T, w http.ResponseWriter, _ *http.Request, body []byte) {
		capturedBody = body
		writeMockStreamChunks(w, "chatcmpl-resp-stream",
			[]map[string]any{
				{"role": "assistant", "content": "hel"},
				{"content": "lo"},
				{"content": " world"},
			},
			map[string]any{"prompt_tokens": 5, "completion_tokens": 3, "total_tokens": 8},
		)
	})
	t.Cleanup(mock.Close)

	f := sdkfixture.Setup(t, sdkfixture.SetupOptions{
		Mode:            sdkfixture.UpstreamMock,
		UpstreamBaseURL: mock.URL + "/v1",
	})

	resp := doResponses(t, http.MethodPost, f.BaseURL+"/responses", f.APIKey,
		`{"model":"`+f.ModelID+`","input":"say hello world","stream":true}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d body=%s", resp.StatusCode, string(raw))
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}

	events := readResponsesSSE(t, resp.Body)
	assertResponsesSSEInvariants(t, events)

	if !hasEventType(events, "response.output_item.added") {
		t.Errorf("missing response.output_item.added")
	}
	if !hasEventType(events, "response.output_item.done") {
		t.Errorf("missing response.output_item.done (权威载体)")
	}

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
	u := completed.Response.Usage
	if u.InputTokens != 5 || u.OutputTokens != 3 || u.TotalTokens != 8 {
		t.Errorf("completed usage = %+v, want input=5 output=3 total=8", u)
	}

	var upstream map[string]any
	if err := json.Unmarshal(capturedBody, &upstream); err != nil {
		t.Fatalf("unmarshal upstream body: %v", err)
	}
	if stream, _ := upstream["stream"].(bool); !stream {
		t.Errorf("expected upstream stream=true (raw: %s)", string(capturedBody))
	}
}

// OAI-RESP-Mock-08：reasoning 流式——DeepSeek reasoning_content → response.reasoning_text.delta。
func TestResponsesMockStreamReasoning(t *testing.T) {
	mock := newMockUpstream(t, func(_ *testing.T, w http.ResponseWriter, _ *http.Request, _ []byte) {
		writeMockStreamChunks(w, "chatcmpl-resp-reason",
			[]map[string]any{
				{"role": "assistant", "reasoning_content": "think"},
				{"reasoning_content": "ing..."},
				{"content": "answer"},
			},
			map[string]any{
				"prompt_tokens":             10,
				"completion_tokens":         4,
				"total_tokens":              14,
				"completion_tokens_details": map[string]any{"reasoning_tokens": 2},
			},
		)
	})
	t.Cleanup(mock.Close)

	f := sdkfixture.Setup(t, sdkfixture.SetupOptions{
		Mode:            sdkfixture.UpstreamMock,
		UpstreamBaseURL: mock.URL + "/v1",
		ModelID:         "deepseek-v4-pro",
		UpstreamModel:   "deepseek-v4-pro",
	})

	// 显式带 reasoning（非 disabled），与真实 reasoning run 一致。
	resp := doResponses(t, http.MethodPost, f.BaseURL+"/responses", f.APIKey,
		`{"model":"`+f.ModelID+`","input":"why is the sky blue?","stream":true,"reasoning":{"effort":"high"}}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d body=%s", resp.StatusCode, string(raw))
	}

	events := readResponsesSSE(t, resp.Body)
	assertResponsesSSEInvariants(t, events)

	var (
		reasoning strings.Builder
		content   strings.Builder
	)
	for _, ev := range events {
		switch ev.Type {
		case "response.reasoning_text.delta":
			reasoning.WriteString(ev.Delta)
		case "response.output_text.delta":
			content.WriteString(ev.Delta)
		}
	}
	if reasoning.String() != "thinking..." {
		t.Errorf("reasoning = %q, want %q", reasoning.String(), "thinking...")
	}
	if content.String() != "answer" {
		t.Errorf("content = %q, want %q", content.String(), "answer")
	}
}

// OAI-RESP-Mock-09：真实 Codex v0.130 /responses 抓包端到端回归。
//
// 把真实 fixture（含多 input item、developer/user message、function + 内置工具、reasoning:null、
// tool_choice 等）的 model 改成 fixture 路由可命中的模型后整体回放，验证桥接翻译不报错、
// 上游收到合法 chat completions、客户拿到完整 Responses 事件流。
func TestResponsesRealCodexFixtureRoundTrips(t *testing.T) {
	raw, err := os.ReadFile("../fixtures/codex/20260605_151709.845_POST_v1_responses.json")
	if err != nil {
		t.Fatalf("read codex fixture: %v", err)
	}

	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal codex fixture: %v", err)
	}

	var capturedBody []byte
	mock := newMockUpstream(t, func(_ *testing.T, w http.ResponseWriter, _ *http.Request, body []byte) {
		capturedBody = body
		writeMockStreamChunks(w, "chatcmpl-codex-fixture",
			[]map[string]any{
				{"role": "assistant", "content": "done"},
			},
			map[string]any{"prompt_tokens": 1200, "completion_tokens": 5, "total_tokens": 1205},
		)
	})
	t.Cleanup(mock.Close)

	f := sdkfixture.Setup(t, sdkfixture.SetupOptions{
		Mode:            sdkfixture.UpstreamMock,
		UpstreamBaseURL: mock.URL + "/v1",
	})

	// 真实 Codex 模型名（gpt-5-codex）不在 fixture 模型目录里；改成可路由模型即可端到端回放。
	doc["model"] = f.ModelID
	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal patched fixture: %v", err)
	}

	resp := doResponses(t, http.MethodPost, f.BaseURL+"/responses", f.APIKey, string(body))
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respRaw, _ := io.ReadAll(resp.Body)
		t.Fatalf("real Codex fixture rejected: status=%d body=%s", resp.StatusCode, string(respRaw))
	}

	events := readResponsesSSE(t, resp.Body)
	assertResponsesSSEInvariants(t, events)
	if hasEventType(events, "error") {
		t.Errorf("unexpected error event in fixture round-trip: %+v", events)
	}

	// 桥接成功的最小证明：上游收到合法 chat completions（messages 数组 + stream=true）。
	var upstream map[string]any
	if err := json.Unmarshal(capturedBody, &upstream); err != nil {
		t.Fatalf("upstream body not json: %v (raw: %s)", err, string(capturedBody))
	}
	if msgs, ok := upstream["messages"].([]any); !ok || len(msgs) == 0 {
		t.Errorf("expected translated chat messages, got %v", upstream["messages"])
	}
	if stream, _ := upstream["stream"].(bool); !stream {
		t.Errorf("expected upstream stream=true")
	}
}
