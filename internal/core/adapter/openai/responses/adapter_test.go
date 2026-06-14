package responses

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ThankCat/unio-api/internal/core/adapter"
	"github.com/ThankCat/unio-api/internal/core/channel"
	"github.com/ThankCat/unio-api/internal/core/usage"
	"github.com/ThankCat/unio-api/internal/platform/failure"
)

func testChannel(baseURL string) channel.Runtime {
	return channel.Runtime{
		ID:      123,
		BaseURL: baseURL + "/v1",
		APIKey:  "test-secret",
		Timeout: 30 * time.Second,
	}
}

// TestCreateResponseForwardsBodyAndParsesFacts 验证非流式直传：请求体逐字转发到 <base>/responses，
// 带 Authorization/Content-Type；响应体原文回传（Raw == 上游 body），usage/id/facts 在同次解析抽取。
func TestCreateResponseForwardsBodyAndParsesFacts(t *testing.T) {
	var (
		gotMethod      string
		gotPath        string
		gotAuth        string
		gotContentType string
		gotBody        []byte
	)

	respBody := `{"id":"resp_123","object":"response","status":"completed","model":"gpt-5.5","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}],"usage":{"input_tokens":11,"output_tokens":5,"total_tokens":16,"input_tokens_details":{"cached_tokens":4},"output_tokens_details":{"reasoning_tokens":2}}}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		gotBody = body

		w.Header().Set("X-Request-Id", "req-up-1")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(respBody))
	}))
	defer server.Close()

	a := NewAdapter(server.Client())
	reqBody := json.RawMessage(`{"model":"gpt-5.5","stream":false,"input":"hello"}`)

	got, err := a.CreateResponse(context.Background(), testChannel(server.URL), Request{Body: reqBody})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Fatalf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/v1/responses" {
		t.Fatalf("path = %q, want /v1/responses", gotPath)
	}
	if gotAuth != "Bearer test-secret" {
		t.Fatalf("authorization = %q, want Bearer test-secret", gotAuth)
	}
	if !strings.HasPrefix(gotContentType, "application/json") {
		t.Fatalf("content-type = %q, want application/json", gotContentType)
	}
	if string(gotBody) != string(reqBody) {
		t.Fatalf("upstream body = %s, want verbatim %s", gotBody, reqBody)
	}

	// 响应原文回传。
	if string(got.Raw) != respBody {
		t.Fatalf("raw passthrough mismatch:\n got %s\nwant %s", got.Raw, respBody)
	}
	if got.ResponseID != "resp_123" || got.Model != "gpt-5.5" {
		t.Fatalf("id/model = %q/%q, want resp_123/gpt-5.5", got.ResponseID, got.Model)
	}

	// usage 归一。
	if got.Usage.PromptTokens != 11 || got.Usage.CompletionTokens != 5 || got.Usage.TotalTokens != 16 {
		t.Fatalf("usage = %+v, want 11/5/16", got.Usage)
	}
	if got.Usage.CachedTokens != 4 || got.Usage.ReasoningTokens != 2 {
		t.Fatalf("usage details = cached %d / reasoning %d, want 4/2", got.Usage.CachedTokens, got.Usage.ReasoningTokens)
	}

	// facts。
	if got.Facts.UpstreamProtocol != "openai" {
		t.Fatalf("facts protocol = %q, want openai", got.Facts.UpstreamProtocol)
	}
	if got.Facts.Finish.Class != adapter.FinishStop || got.Facts.Finish.RawReason != "completed" {
		t.Fatalf("facts finish = %+v, want stop/completed", got.Facts.Finish)
	}
	if got.Facts.UsageSource != usage.SourceUpstreamResponse {
		t.Fatalf("facts usage source = %q, want upstream response", got.Facts.UsageSource)
	}
	if got.Facts.UsageMappingVersion != usageMappingVersionResponses {
		t.Fatalf("facts mapping version = %q, want %q", got.Facts.UsageMappingVersion, usageMappingVersionResponses)
	}

	// upstream metadata。
	if got.Upstream.StatusCode != http.StatusOK || got.Upstream.RequestID != "req-up-1" {
		t.Fatalf("upstream meta = %+v, want 200/req-up-1", got.Upstream)
	}
}

func TestCreateResponseMissingUsageReturnsInvalidResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","status":"completed","model":"gpt-5.5"}`))
	}))
	defer server.Close()

	_, err := NewAdapter(server.Client()).CreateResponse(context.Background(), testChannel(server.URL), Request{Body: json.RawMessage(`{"model":"gpt-5.5"}`)})
	if err == nil {
		t.Fatal("expected error for missing usage")
	}
	if failure.CodeOf(err) != failure.CodeAdapterInvalidResponse {
		t.Fatalf("code = %q, want %q", failure.CodeOf(err), failure.CodeAdapterInvalidResponse)
	}
}

// TestCreateResponseStatusFailedReturnsUpstreamError 验证上游用 200 包裹的协议失败（status=failed）
// 被映射成结构化上游错误，而非误判为可计费成功响应。
func TestCreateResponseStatusFailedReturnsUpstreamError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","status":"failed","model":"gpt-5.5"}`))
	}))
	defer server.Close()

	_, err := NewAdapter(server.Client()).CreateResponse(context.Background(), testChannel(server.URL), Request{Body: json.RawMessage(`{"model":"gpt-5.5"}`)})
	if err == nil {
		t.Fatal("expected error for status=failed")
	}
	if failure.CodeOf(err) != failure.CodeAdapterUpstreamStatus {
		t.Fatalf("code = %q, want %q", failure.CodeOf(err), failure.CodeAdapterUpstreamStatus)
	}
	if cat, ok := adapter.UpstreamCategoryOf(err); !ok || cat != adapter.UpstreamErrorServer {
		t.Fatalf("category = %q ok=%v, want server", cat, ok)
	}
}

func TestCreateResponseErrorObjectReturnsUpstreamError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","status":"completed","model":"gpt-5.5","error":{"type":"server_error","code":"oops","message":"boom"}}`))
	}))
	defer server.Close()

	_, err := NewAdapter(server.Client()).CreateResponse(context.Background(), testChannel(server.URL), Request{Body: json.RawMessage(`{"model":"gpt-5.5"}`)})
	if err == nil {
		t.Fatal("expected error for inline error object")
	}
	if failure.CodeOf(err) != failure.CodeAdapterUpstreamStatus {
		t.Fatalf("code = %q, want %q", failure.CodeOf(err), failure.CodeAdapterUpstreamStatus)
	}
}

func TestCreateResponseUpstreamNon2xxReturnsStatusError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-Id", "req-429")
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	defer server.Close()

	_, err := NewAdapter(server.Client()).CreateResponse(context.Background(), testChannel(server.URL), Request{Body: json.RawMessage(`{"model":"gpt-5.5"}`)})
	if err == nil {
		t.Fatal("expected error for non-2xx")
	}
	if failure.CodeOf(err) != failure.CodeAdapterUpstreamStatus {
		t.Fatalf("code = %q, want %q", failure.CodeOf(err), failure.CodeAdapterUpstreamStatus)
	}
	cat, ok := adapter.UpstreamCategoryOf(err)
	if !ok || cat != adapter.UpstreamErrorRateLimit {
		t.Fatalf("category = %q ok=%v, want rate_limit", cat, ok)
	}
	meta, ok := adapter.UpstreamMetadataOf(err)
	if !ok || meta.StatusCode != http.StatusTooManyRequests || meta.RequestID != "req-429" {
		t.Fatalf("meta = %+v ok=%v, want 429/req-429", meta, ok)
	}
}

func TestCreateResponseEmptyBodyReturnsEncodeError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("upstream must not be called with empty body")
	}))
	defer server.Close()

	_, err := NewAdapter(server.Client()).CreateResponse(context.Background(), testChannel(server.URL), Request{})
	if err == nil {
		t.Fatal("expected error for empty body")
	}
	if failure.CodeOf(err) != failure.CodeAdapterEncodeRequestFailed {
		t.Fatalf("code = %q, want %q", failure.CodeOf(err), failure.CodeAdapterEncodeRequestFailed)
	}
}

// TestStreamResponseForwardsRawEventsAndExtractsFacts 验证流式直传：每个上游 SSE 事件原文经 emit 透传，
// 终态 response.completed 抽取 usage/id/finish，StreamOutcome.Facts 在流尾产出。
func TestStreamResponseForwardsRawEventsAndExtractsFacts(t *testing.T) {
	var gotAccept string

	events := []string{
		"event: response.created\n" + `data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.5","status":"in_progress"}}` + "\n\n",
		"event: response.output_text.delta\n" + `data: {"type":"response.output_text.delta","delta":"Hello"}` + "\n\n",
		"event: response.completed\n" + `data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.5","status":"completed","usage":{"input_tokens":11,"output_tokens":5,"total_tokens":16}}}` + "\n\n",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAccept = r.Header.Get("Accept")
		w.Header().Set("X-Request-Id", "req-stream-1")
		w.Header().Set("Content-Type", "text/event-stream")
		for _, ev := range events {
			_, _ = w.Write([]byte(ev))
		}
	}))
	defer server.Close()

	got := make([]StreamChunk, 0)
	outcome, err := NewAdapter(server.Client()).StreamResponse(context.Background(), testChannel(server.URL), Request{Body: json.RawMessage(`{"model":"gpt-5.5","stream":true}`)}, func(c StreamChunk) error {
		got = append(got, c)
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if gotAccept != "text/event-stream" {
		t.Fatalf("accept = %q, want text/event-stream", gotAccept)
	}

	if len(got) != 3 {
		t.Fatalf("got %d chunks, want 3", len(got))
	}
	if got[0].EventType != "response.created" {
		t.Fatalf("chunk[0] event = %q, want response.created", got[0].EventType)
	}
	if got[1].EventType != "response.output_text.delta" || !strings.Contains(string(got[1].Data), `"delta":"Hello"`) {
		t.Fatalf("chunk[1] = %+v, want verbatim delta", got[1])
	}

	// 终态 chunk 携带解析出的 usage / id / finish。
	terminal := got[2]
	if terminal.EventType != "response.completed" {
		t.Fatalf("chunk[2] event = %q, want response.completed", terminal.EventType)
	}
	if terminal.ResponseID != "resp_1" {
		t.Fatalf("terminal id = %q, want resp_1", terminal.ResponseID)
	}
	if terminal.FinishReason != "completed" {
		t.Fatalf("terminal finish = %q, want completed", terminal.FinishReason)
	}
	if terminal.Usage == nil || terminal.Usage.TotalTokens != 16 {
		t.Fatalf("terminal usage = %+v, want total 16", terminal.Usage)
	}

	if outcome.Facts == nil {
		t.Fatal("expected stream outcome facts")
	}
	if outcome.Facts.UpstreamResponseID != "resp_1" || outcome.Facts.Finish.Class != adapter.FinishStop {
		t.Fatalf("facts = %+v, want resp_1/stop", outcome.Facts)
	}
	if outcome.Facts.UsageSource != usage.SourceUpstreamStream {
		t.Fatalf("facts usage source = %q, want upstream stream", outcome.Facts.UsageSource)
	}
}

// TestStreamResponseIncompleteMapsToLength 验证 response.incomplete + max_output_tokens 终态映射为 length。
func TestStreamResponseIncompleteMapsToLength(t *testing.T) {
	events := []string{
		"event: response.incomplete\n" + `data: {"type":"response.incomplete","response":{"id":"resp_2","model":"gpt-5.5","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"usage":{"input_tokens":3,"output_tokens":9,"total_tokens":12}}}` + "\n\n",
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, ev := range events {
			_, _ = w.Write([]byte(ev))
		}
	}))
	defer server.Close()

	got := make([]StreamChunk, 0)
	outcome, err := NewAdapter(server.Client()).StreamResponse(context.Background(), testChannel(server.URL), Request{Body: json.RawMessage(`{"model":"gpt-5.5","stream":true}`)}, func(c StreamChunk) error {
		got = append(got, c)
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].FinishReason != "max_output_tokens" {
		t.Fatalf("got %+v, want finish max_output_tokens", got)
	}
	if outcome.Facts == nil || outcome.Facts.Finish.Class != adapter.FinishLength {
		t.Fatalf("facts = %+v, want length", outcome.Facts)
	}
}

// TestStreamResponseDoneSentinelTerminates 验证流尾追加的 chat 风格 [DONE] 哨兵被截留为内部成功终态，
// 不透传给客户。
func TestStreamResponseDoneSentinelTerminates(t *testing.T) {
	events := []string{
		"event: response.output_text.delta\n" + `data: {"type":"response.output_text.delta","delta":"hi"}` + "\n\n",
		"data: [DONE]\n\n",
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, ev := range events {
			_, _ = w.Write([]byte(ev))
		}
	}))
	defer server.Close()

	got := make([]StreamChunk, 0)
	_, err := NewAdapter(server.Client()).StreamResponse(context.Background(), testChannel(server.URL), Request{Body: json.RawMessage(`{"model":"gpt-5.5","stream":true}`)}, func(c StreamChunk) error {
		got = append(got, c)
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d chunks, want 1 ([DONE] must not be forwarded)", len(got))
	}
}

// TestStreamResponseFailedEventReturnsError 验证 response.failed 事件先原文透传，再以结构化上游错误中断。
func TestStreamResponseFailedEventReturnsError(t *testing.T) {
	events := []string{
		"event: response.failed\n" + `data: {"type":"response.failed","response":{"id":"resp_3","status":"failed","error":{"code":"server_error","message":"upstream blew up"}}}` + "\n\n",
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, ev := range events {
			_, _ = w.Write([]byte(ev))
		}
	}))
	defer server.Close()

	got := make([]StreamChunk, 0)
	_, err := NewAdapter(server.Client()).StreamResponse(context.Background(), testChannel(server.URL), Request{Body: json.RawMessage(`{"model":"gpt-5.5","stream":true}`)}, func(c StreamChunk) error {
		got = append(got, c)
		return nil
	})
	if err == nil {
		t.Fatal("expected error for response.failed")
	}
	if failure.CodeOf(err) != failure.CodeAdapterUpstreamStatus {
		t.Fatalf("code = %q, want %q", failure.CodeOf(err), failure.CodeAdapterUpstreamStatus)
	}
	// 错误事件原文先透传给客户（Codex 据此映射 ApiError）。
	if len(got) != 1 || got[0].EventType != "response.failed" {
		t.Fatalf("got %+v, want failed event forwarded once", got)
	}
}

func TestStreamResponseEndsBeforeTerminalReturnsError(t *testing.T) {
	events := []string{
		"event: response.output_text.delta\n" + `data: {"type":"response.output_text.delta","delta":"hi"}` + "\n\n",
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, ev := range events {
			_, _ = w.Write([]byte(ev))
		}
	}))
	defer server.Close()

	_, err := NewAdapter(server.Client()).StreamResponse(context.Background(), testChannel(server.URL), Request{Body: json.RawMessage(`{"model":"gpt-5.5","stream":true}`)}, func(StreamChunk) error { return nil })
	if err == nil {
		t.Fatal("expected error for stream ending before terminal event")
	}
	if failure.CodeOf(err) != failure.CodeAdapterReadStreamFailed {
		t.Fatalf("code = %q, want %q", failure.CodeOf(err), failure.CodeAdapterReadStreamFailed)
	}
}

// TestStreamResponseChannelTimeoutDoesNotCutLongStream 复现 Codex 长任务(图像生成等):上游先回
// 200 + 响应头,静默远超渠道 timeout 后才吐终态事件。渠道 timeout 只该约束「拿到响应头」的等待,
// 不该把流本体当成绝对截止时间掐断,故此流必须成功完成(而非 adapter_read_stream_failed)。
func TestStreamResponseChannelTimeoutDoesNotCutLongStream(t *testing.T) {
	const channelTimeout = 60 * time.Millisecond
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("test server response writer must support flushing")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher.Flush() // 立刻发响应头:客户端 Do 返回 → headersReceived 停表
		time.Sleep(4 * channelTimeout) // 远超渠道 timeout 才吐事件(旧逻辑会在此被掐断)
		_, _ = w.Write([]byte("event: response.completed\n" + `data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.5","status":"completed","usage":{"input_tokens":3,"output_tokens":5,"total_tokens":8}}}` + "\n\n"))
		flusher.Flush()
	}))
	defer server.Close()

	ch := testChannel(server.URL)
	ch.Timeout = channelTimeout

	got := make([]StreamChunk, 0)
	outcome, err := NewAdapter(server.Client()).StreamResponse(context.Background(), ch, Request{Body: json.RawMessage(`{"model":"gpt-5.5","stream":true}`)}, func(c StreamChunk) error {
		got = append(got, c)
		return nil
	})
	if err != nil {
		t.Fatalf("expected stream to survive channel timeout after headers, got err: %v", err)
	}
	if len(got) != 1 || got[0].EventType != "response.completed" {
		t.Fatalf("got %+v, want one response.completed event", got)
	}
	if outcome.Facts == nil || outcome.Facts.UpstreamResponseID != "resp_1" {
		t.Fatalf("facts = %+v, want resp_1 facts from terminal event", outcome.Facts)
	}
}

// TestStreamResponseRecoversMalformedMultiDataFrame 复现某些中转的畸形多行 data 帧:真正的终态事件
// 前多塞一行残片 `data: {"type"`。SSE 合并后整体非法 JSON,旧逻辑会 adapter_read/emit 失败、丢计费。
// 修复后必须 sanitize 出真事件,流正常收口且 usage/facts 完整。
func TestStreamResponseRecoversMalformedMultiDataFrame(t *testing.T) {
	// 注意:同一事件内的两行 data(中间无空行),SSE reader 会用 \n 合并。
	body := "event: response.completed\n" +
		"data: {\"type\"\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_9\",\"model\":\"gpt-5.5\",\"status\":\"completed\",\"usage\":{\"input_tokens\":7,\"output_tokens\":4,\"total_tokens\":11}}}\n" +
		"\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	got := make([]StreamChunk, 0)
	outcome, err := NewAdapter(server.Client()).StreamResponse(context.Background(), testChannel(server.URL), Request{Body: json.RawMessage(`{"model":"gpt-5.5","stream":true}`)}, func(c StreamChunk) error {
		got = append(got, c)
		return nil
	})
	if err != nil {
		t.Fatalf("expected malformed multi-data frame to be recovered, got err: %v", err)
	}
	if len(got) != 1 || got[0].EventType != "response.completed" {
		t.Fatalf("got %+v, want one recovered response.completed event", got)
	}
	if !json.Valid(got[0].Data) {
		t.Fatalf("forwarded chunk data must be valid JSON, got %q", got[0].Data)
	}
	if outcome.Facts == nil || outcome.Facts.UpstreamResponseID != "resp_9" {
		t.Fatalf("facts = %+v, want resp_9 facts recovered from malformed terminal event", outcome.Facts)
	}
}

func TestSanitizeEventData(t *testing.T) {
	full := `{"type":"response.completed","response":{"id":"r"}}`
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"valid passthrough", full, full},
		{"empty", "", ""},
		{"done sentinel untouched", "[DONE]", "[DONE]"},
		{"stray prefix fragment", "{\"type\"\n" + full, full},
		{"stray suffix fragment", full + "\n{\"type\"", full},
		{"pretty multiline valid untouched", "{\n  \"a\": 1\n}", "{\n  \"a\": 1\n}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := string(sanitizeEventData([]byte(tc.in)))
			if got != tc.want {
				t.Fatalf("sanitizeEventData(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestStreamResponseUpstreamStatusReturnsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad gateway", http.StatusBadGateway)
	}))
	defer server.Close()

	_, err := NewAdapter(server.Client()).StreamResponse(context.Background(), testChannel(server.URL), Request{Body: json.RawMessage(`{"model":"gpt-5.5","stream":true}`)}, func(StreamChunk) error {
		t.Fatal("emit must not be called on non-2xx")
		return nil
	})
	if err == nil {
		t.Fatal("expected error for non-2xx stream")
	}
	if failure.CodeOf(err) != failure.CodeAdapterUpstreamStatus {
		t.Fatalf("code = %q, want %q", failure.CodeOf(err), failure.CodeAdapterUpstreamStatus)
	}
}

func TestStreamResponseNilEmitReturnsError(t *testing.T) {
	_, err := NewAdapter(http.DefaultClient).StreamResponse(context.Background(), testChannel("https://example.test"), Request{Body: json.RawMessage(`{}`)}, nil)
	if err == nil {
		t.Fatal("expected error for nil emit")
	}
	if failure.CodeOf(err) != failure.CodeAdapterEmitFailed {
		t.Fatalf("code = %q, want %q", failure.CodeOf(err), failure.CodeAdapterEmitFailed)
	}
}

func TestCountResponsesInputTokens(t *testing.T) {
	a := NewAdapter(http.DefaultClient)

	body := json.RawMessage(`{"model":"gpt-5.5","input":"hello world"}`)
	tokens, err := a.CountResponsesInputTokens(Request{Body: body})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := int64(len(body))/charsPerToken + wireOverheadTokens
	if tokens != want {
		t.Fatalf("tokens = %d, want %d", tokens, want)
	}

	if _, err := a.CountResponsesInputTokens(Request{}); err == nil {
		t.Fatal("expected error for empty body")
	} else if failure.CodeOf(err) != failure.CodeAdapterTokenizeFailed {
		t.Fatalf("code = %q, want %q", failure.CodeOf(err), failure.CodeAdapterTokenizeFailed)
	}
}

func TestChatUsageFromWire(t *testing.T) {
	if _, ok := chatUsageFromWire(nil); ok {
		t.Fatal("nil usage must map to ok=false")
	}

	u, ok := chatUsageFromWire(&wireUsage{
		InputTokens:         10,
		OutputTokens:        4,
		TotalTokens:         14,
		InputTokensDetails:  &wireInputTokenDetail{CachedTokens: 3},
		OutputTokensDetails: &wireOutputTokenDetail{ReasoningTokens: 2},
	})
	if !ok {
		t.Fatal("expected ok=true")
	}
	if u.PromptTokens != 10 || u.CompletionTokens != 4 || u.TotalTokens != 14 {
		t.Fatalf("usage = %+v, want 10/4/14", u)
	}
	if u.CachedTokens != 3 || u.ReasoningTokens != 2 {
		t.Fatalf("usage details = cached %d / reasoning %d, want 3/2", u.CachedTokens, u.ReasoningTokens)
	}
}

func TestResponsesFinishClassAndRawFinish(t *testing.T) {
	cases := []struct {
		status     string
		incomplete string
		wantClass  adapter.FinishClass
		wantRaw    string
	}{
		{"completed", "", adapter.FinishStop, "completed"},
		{"incomplete", "max_output_tokens", adapter.FinishLength, "max_output_tokens"},
		{"incomplete", "content_filter", adapter.FinishContentFilter, "content_filter"},
		{"incomplete", "weird", adapter.FinishOther, "weird"},
		{"unknown", "", adapter.FinishOther, "unknown"},
	}
	for _, tc := range cases {
		if got := responsesFinishClass(tc.status, tc.incomplete); got != tc.wantClass {
			t.Fatalf("finishClass(%q,%q) = %q, want %q", tc.status, tc.incomplete, got, tc.wantClass)
		}
		if got := responsesRawFinish(tc.status, tc.incomplete); got != tc.wantRaw {
			t.Fatalf("rawFinish(%q,%q) = %q, want %q", tc.status, tc.incomplete, got, tc.wantRaw)
		}
	}
}
