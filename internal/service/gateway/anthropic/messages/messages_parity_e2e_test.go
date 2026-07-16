package messages

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	messagesadapter "github.com/ThankCat/unio-gateway/internal/core/adapter/anthropic/messages"
	"github.com/ThankCat/unio-gateway/internal/core/requestlog"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	coreusage "github.com/ThankCat/unio-gateway/internal/core/usage"

	gatewayapi "github.com/ThankCat/unio-gateway/internal/app/gatewayapi/anthropic/messages"
)

// mockMessagesUpstream 是一个最小的 Anthropic Messages 上游替身。
//
// 与 service_test.go 的 fakeMessagesAdapter 不同，这里挂载真实的 messagesadapter.Adapter，
// 因此本组用例端到端覆盖「gateway 请求映射 → adapter wire 编码 → HTTP → 响应/SSE 解析 →
// ResponseFacts → gateway 公开 DTO」整条链路，等价于 OpenAI 侧的 openai_parity_e2e_test.go。
type mockMessagesUpstream struct {
	server          *httptest.Server
	lastRequestBody []byte
}

func newMockMessagesUpstream(t *testing.T, respond func(w http.ResponseWriter)) *mockMessagesUpstream {
	t.Helper()

	mock := &mockMessagesUpstream{}
	mock.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// adapter 固定 POST <base>/v1/messages，这里校验出站契约。
		if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" {
			t.Errorf("unexpected upstream request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read upstream body: %v", err)
			return
		}
		mock.lastRequestBody = body

		respond(w)
	}))

	return mock
}

// newRealMessagesRegistry 用真实 anthropic base adapter 充当注册表，复用 service_test 的 fake 形状。
func newRealMessagesRegistry(client *http.Client) *fakeMessagesRegistry {
	real := messagesadapter.NewAdapter(client)
	return &fakeMessagesRegistry{
		messages:       map[string]messagesadapter.MessagesAdapter{"deepseek": real},
		streamMessages: map[string]messagesadapter.StreamMessagesAdapter{"deepseek": real},
	}
}

// mockMessagesCandidate 把 routing 候选指向 mock 上游，并标注 deepseek provider slug。
func mockMessagesCandidate(server *httptest.Server) routing.ChatRouteCandidate {
	candidate := routeCandidate("deepseek", 123, "deepseek-v4-flash")
	candidate.Channel.BaseURL = server.URL
	candidate.Channel.ProviderSlug = "deepseek"
	return candidate
}

// TestAnthropicSDKShapeNonStreamMessage 端到端验证非流式 /v1/messages：
// 公开响应保持原生 Anthropic Message 形状、catalog model 还原、usage 贯通、结算消费 anthropic facts，
// 且出站 wire 使用 upstream model 而非客户 catalog model。
func TestAnthropicSDKShapeNonStreamMessage(t *testing.T) {
	upstream := newMockMessagesUpstream(t, func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		body := `{
			"id": "msg_ds_1",
			"type": "message",
			"role": "assistant",
			"model": "deepseek-v4-flash",
			"content": [{"type":"text","text":"hi there"}],
			"stop_reason": "end_turn",
			"stop_sequence": null,
			"usage": {"input_tokens": 10, "output_tokens": 11, "cache_read_input_tokens": 2}
		}`
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("write upstream body: %v", err)
		}
	})
	defer upstream.server.Close()

	settlement := &fakeMessagesSettlement{}
	service := newMessagesServiceForTest(
		&fakeMessagesRouter{plan: routePlan(mockMessagesCandidate(upstream.server))},
		newRealMessagesRegistry(upstream.server.Client()),
		settlement,
		&fakeMessagesAuthorizer{},
	)

	got, err := service.CreateMessage(contextWithPrincipal(42), messageRequest())
	if err != nil {
		t.Fatalf("CreateMessage returned err: %v", err)
	}

	if got.Type != "message" || got.Role != "assistant" {
		t.Fatalf("unexpected response envelope: type=%q role=%q", got.Type, got.Role)
	}
	// 客户拿到的是自己的 catalog model，而不是 upstream 真实 model。
	if got.Model != "anthropic/claude-sonnet-4" {
		t.Fatalf("expected catalog model echoed, got %q", got.Model)
	}
	if len(got.Content) != 1 || !strings.Contains(string(got.Content[0]), "hi there") {
		t.Fatalf("unexpected content: %v", got.Content)
	}
	if got.StopReason == nil || *got.StopReason != "end_turn" {
		t.Fatalf("stop_reason = %#v, want end_turn", got.StopReason)
	}
	if got.Usage.InputTokens != 10 || got.Usage.OutputTokens != 11 {
		t.Fatalf("usage = %+v, want input=10 output=11", got.Usage)
	}
	if got.Usage.CacheReadInputTokens == nil || *got.Usage.CacheReadInputTokens != 2 {
		t.Fatalf("cache_read_input_tokens = %#v, want 2", got.Usage.CacheReadInputTokens)
	}

	// 结算只消费不可变 facts，并标记 anthropic / upstream_response。
	if len(settlement.params) != 1 {
		t.Fatalf("expected one settlement attempt, got %d", len(settlement.params))
	}
	settled := settlement.params[0]
	if settled.ResponseProtocol != requestlog.ProtocolAnthropic {
		t.Fatalf("settlement protocol = %q, want anthropic", settled.ResponseProtocol)
	}
	if settled.Facts.UsageSource != coreusage.SourceUpstreamResponse {
		t.Fatalf("settlement usage source = %q, want upstream_response", settled.Facts.UsageSource)
	}
	if out, ok := settled.Facts.Usage.OutputTokensTotal.BillableValue(); !ok || out != 11 {
		t.Fatalf("settlement output tokens = (%d, %v), want 11", out, ok)
	}

	// 出站 wire：必须送 upstream model，不得泄漏客户 catalog model。
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(upstream.lastRequestBody, &wire); err != nil {
		t.Fatalf("unmarshal upstream body: %v", err)
	}
	var upstreamModel string
	if err := json.Unmarshal(wire["model"], &upstreamModel); err != nil {
		t.Fatalf("decode upstream model: %v", err)
	}
	if upstreamModel != "deepseek-v4-flash" {
		t.Fatalf("upstream model = %q, want deepseek-v4-flash", upstreamModel)
	}
	if _, ok := wire["max_tokens"]; !ok {
		t.Fatal("expected max_tokens forwarded to upstream")
	}

	// 客户端 JSON 序列化必须真实带上原生 Anthropic 字段（faithful 形状）。
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	for _, key := range []string{`"type":"message"`, `"role":"assistant"`, `"model":"anthropic/claude-sonnet-4"`, `"stop_reason":"end_turn"`, `"input_tokens":10`, `"output_tokens":11`} {
		if !strings.Contains(string(encoded), key) {
			t.Fatalf("serialized response missing %q: %s", key, encoded)
		}
	}
}

// TestAnthropicSDKShapeStreamMessage 端到端验证流式 /v1/messages：
// 原生事件序透传、message_start 的 model 被还原为 catalog model、message_stop 由 gateway 结算后补发，
// 且结算使用 upstream_stream facts。
func TestAnthropicSDKShapeStreamMessage(t *testing.T) {
	upstream := newMockMessagesUpstream(t, func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		events := []string{
			"event: message_start\n" +
				`data: {"type":"message_start","message":{"id":"msg_ds_stream","model":"deepseek-v4-flash","usage":{"input_tokens":10,"output_tokens":1}}}` + "\n\n",
			"event: content_block_delta\n" +
				`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi there"}}` + "\n\n",
			"event: message_delta\n" +
				`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":11}}` + "\n\n",
			"event: message_stop\n" +
				`data: {"type":"message_stop"}` + "\n\n",
		}
		for _, ev := range events {
			if _, err := w.Write([]byte(ev)); err != nil {
				t.Errorf("write stream event: %v", err)
				return
			}
		}
	})
	defer upstream.server.Close()

	settlement := &fakeMessagesSettlement{}
	service := newMessagesServiceForTest(
		&fakeMessagesRouter{plan: routePlan(mockMessagesCandidate(upstream.server))},
		newRealMessagesRegistry(upstream.server.Client()),
		settlement,
		&fakeMessagesAuthorizer{},
	)

	stream := true
	req := messageRequest()
	req.Stream = &stream

	var frames []gatewayapi.StreamFrame
	if err := service.StreamMessage(contextWithPrincipal(42), req, func(frame gatewayapi.StreamFrame) error {
		frames = append(frames, frame)
		return nil
	}); err != nil {
		t.Fatalf("StreamMessage returned err: %v", err)
	}

	wantOrder := []string{"message_start", "content_block_delta", "message_delta", "message_stop"}
	if len(frames) != len(wantOrder) {
		t.Fatalf("expected %d frames, got %d (%#v)", len(wantOrder), len(frames), frames)
	}
	for i, want := range wantOrder {
		if frames[i].EventType != want {
			t.Fatalf("frame %d = %q, want %q", i, frames[i].EventType, want)
		}
	}

	// message_start 的 model 必须被还原为客户 catalog model。
	var startPayload struct {
		Message struct {
			Model string `json:"model"`
		} `json:"message"`
	}
	if err := json.Unmarshal(frames[0].Data, &startPayload); err != nil {
		t.Fatalf("decode message_start: %v", err)
	}
	if startPayload.Message.Model != "anthropic/claude-sonnet-4" {
		t.Fatalf("expected catalog model in message_start, got %q", startPayload.Message.Model)
	}

	// content delta 仍透出原生文本。
	if !strings.Contains(string(frames[1].Data), "hi there") {
		t.Fatalf("expected content_block_delta text, got %s", frames[1].Data)
	}

	// message_stop 必须由 gateway 在结算收口后补发，且结算使用 stream facts。
	if len(settlement.params) != 1 {
		t.Fatalf("expected one settlement attempt, got %d", len(settlement.params))
	}
	if settlement.params[0].Facts.UsageSource != coreusage.SourceUpstreamStream {
		t.Fatalf("settlement usage source = %q, want upstream_stream", settlement.params[0].Facts.UsageSource)
	}
	if out, ok := settlement.params[0].Facts.Usage.OutputTokensTotal.BillableValue(); !ok || out != 11 {
		t.Fatalf("settlement output tokens = (%d, %v), want 11", out, ok)
	}

	// 出站 wire 必须声明 stream:true。
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(upstream.lastRequestBody, &wire); err != nil {
		t.Fatalf("unmarshal upstream body: %v", err)
	}
	if string(wire["stream"]) != "true" {
		t.Fatalf("expected stream=true in upstream body, got %s", wire["stream"])
	}
}
