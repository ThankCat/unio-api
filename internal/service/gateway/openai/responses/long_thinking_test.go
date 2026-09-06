package responses

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	responsesadapter "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/responses"
	"github.com/ThankCat/unio-gateway/internal/core/channel"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/core/usage"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/openai/responses/dto"
)

// thinkingStreamAdapter 模拟 Codex 后端的 reasoning 长思考流：协议前导之后只有 reasoning item 事件
// （带 encrypted_content、无可见内容），可选地在最后才出正文并以 completed 收口，或在思考中途断流。
type thinkingStreamAdapter struct {
	called         int
	reasoningItems int
	step           time.Duration
	failAfter      bool
	deltaEmittedAt time.Time
	facts          *adapter.ResponseFacts
}

func (a *thinkingStreamAdapter) StreamResponse(ctx context.Context, _ channel.Runtime, _ responsesadapter.Request, emit func(responsesadapter.StreamChunk) error) (adapter.StreamOutcome, error) {
	a.called++
	adapter.MarkTransportStarted(ctx)
	adapter.MarkRequestWritten(ctx, nil)
	adapter.MarkResponseHeadersReceived(ctx, adapter.UpstreamMetadata{StatusCode: http.StatusOK})
	send := func(eventType, data string) error {
		return emit(responsesadapter.StreamChunk{EventType: eventType, Data: json.RawMessage(data)})
	}
	if err := send("response.created", `{"type":"response.created","response":{"id":"resp_think","model":"gpt-5.6-up","status":"in_progress"}}`); err != nil {
		return adapter.StreamOutcome{}, err
	}
	if err := send("response.in_progress", `{"type":"response.in_progress","response":{"id":"resp_think","status":"in_progress"}}`); err != nil {
		return adapter.StreamOutcome{}, err
	}
	for i := 0; i < a.reasoningItems; i++ {
		time.Sleep(a.step)
		if err := send("response.output_item.added", `{"type":"response.output_item.added","item":{"type":"reasoning","id":"rs_1","encrypted_content":"gAAAA","summary":[]}}`); err != nil {
			return adapter.StreamOutcome{}, err
		}
		adapter.MarkFirstTokenEligible(ctx) // adapter 层：semantic 口径下进展即记上游首字
		if err := send("response.output_item.done", `{"type":"response.output_item.done","item":{"type":"reasoning","id":"rs_1","encrypted_content":"gAAAB","summary":[]}}`); err != nil {
			return adapter.StreamOutcome{}, err
		}
	}
	if a.failAfter {
		return adapter.StreamOutcome{}, adapter.NewUpstreamError(
			adapter.UpstreamErrorServer,
			adapter.UpstreamMetadata{StatusCode: http.StatusOK},
			failure.New(failure.CodeAdapterReadStreamFailed, failure.WithMessage("upstream connection reset during reasoning")),
		)
	}
	time.Sleep(a.step)
	a.deltaEmittedAt = time.Now()
	if err := send("response.output_text.delta", `{"type":"response.output_text.delta","delta":"answer"}`); err != nil {
		return adapter.StreamOutcome{}, err
	}
	u := adapter.ChatUsage{PromptTokens: 100000, CompletionTokens: 20, TotalTokens: 100020}
	if err := emit(responsesadapter.StreamChunk{
		EventType:    "response.completed",
		Data:         json.RawMessage(`{"type":"response.completed","response":{"id":"resp_think","model":"gpt-5.6-up","status":"completed","usage":{"input_tokens":100000,"output_tokens":20,"total_tokens":100020}}}`),
		ResponseID:   "resp_think",
		FinishReason: "completed",
		Usage:        &u,
	}); err != nil {
		return adapter.StreamOutcome{}, err
	}
	facts := &adapter.ResponseFacts{
		UpstreamProtocol:    "openai",
		UpstreamResponseID:  "resp_think",
		UpstreamModel:       "gpt-5.6-up",
		Finish:              adapter.FinishFacts{Class: adapter.FinishStop, RawReason: "completed"},
		Usage:               u.ToUsageFacts(),
		UsageSource:         usage.SourceUpstreamStream,
		UsageMappingVersion: "responses.v1",
	}
	a.facts = facts
	return adapter.StreamOutcome{Facts: facts}, nil
}

// TestStreamResponse_LongThinkingCommitsOnProgressAndTTFTFollowsMode 冻结 WP6 的核心行为：
// reasoning 思考阶段（只有 reasoning item 事件）即为进展——前导与 reasoning 事件按序写给客户、attempt 提交；
// 正文出现后正常结算；TTFT 在 semantic 口径记在首个 reasoning 事件，在 visible 口径记在正文首 delta。
func TestStreamResponse_LongThinkingCommitsOnProgressAndTTFTFollowsMode(t *testing.T) {
	t.Cleanup(func() { adapter.SetTTFTMode(adapter.DefaultTTFTMode) })
	for _, mode := range []adapter.TTFTMode{adapter.TTFTModeSemantic, adapter.TTFTModeVisible} {
		t.Run(string(mode), func(t *testing.T) {
			adapter.SetTTFTMode(mode)
			upstream := &thinkingStreamAdapter{reasoningItems: 3, step: 15 * time.Millisecond}
			registry := &fakeRegistry{streamResponsesAdapters: map[string]responsesadapter.StreamResponsesAdapter{"codex": upstream}}
			router := &fakeRouter{plan: routing.ChatRoutePlan{Candidates: []routing.ChatRouteCandidate{candidate("codex", 1, "gpt-5.6-up")}}}
			settlement := &fakeSettlement{}
			requestLog := newFakeRequestLog()
			svc := newServiceForTest(router, registry, settlement, &fakeAuthorizer{}, requestLog)

			var events []string
			err := svc.StreamResponse(ctxWithPrincipal(), directRequest(), func(ev dto.ResponsesStreamEvent) error {
				events = append(events, ev.Type)
				return nil
			})
			if err != nil {
				t.Fatalf("long thinking stream must succeed: %v", err)
			}
			want := []string{
				"response.created", "response.in_progress",
				"response.output_item.added", "response.output_item.done",
				"response.output_item.added", "response.output_item.done",
				"response.output_item.added", "response.output_item.done",
				"response.output_text.delta", "response.completed",
			}
			if len(events) != len(want) {
				t.Fatalf("events = %v, want %v", events, want)
			}
			for i := range want {
				if events[i] != want[i] {
					t.Fatalf("event[%d] = %q, want %q (full: %v)", i, events[i], want[i], events)
				}
			}
			if len(settlement.params) != 1 {
				t.Fatalf("expected one full settlement, got %d", len(settlement.params))
			}
			firstToken := settlement.params[0].GatewayFirstTokenAt
			if firstToken == nil {
				t.Fatal("gateway first token must be recorded")
			}
			switch mode {
			case adapter.TTFTModeSemantic:
				if !firstToken.Before(upstream.deltaEmittedAt) {
					t.Fatalf("semantic TTFT must land on the first reasoning event (before the text delta at %v), got %v", upstream.deltaEmittedAt, *firstToken)
				}
			case adapter.TTFTModeVisible:
				if firstToken.Before(upstream.deltaEmittedAt) {
					t.Fatalf("visible TTFT must land on the text delta (%v), got %v", upstream.deltaEmittedAt, *firstToken)
				}
			}
			// 首字落库经 launchStreamAudit 异步写出（不阻塞客户流），只能断言「最终」写入而不是即时可见。
			deadline := time.Now().Add(2 * time.Second)
			for requestLog.gatewayFirstTokens.Load() == 0 {
				if time.Now().After(deadline) {
					t.Fatal("gateway first token must be persisted")
				}
				time.Sleep(5 * time.Millisecond)
			}
		})
	}
}

// TestStreamResponse_UpstreamDiesDuringThinkingReleasesWithoutFallback 冻结进展之后的失败语义：
// 客户已收到前导与 reasoning 事件（attempt 已提交）→ 不再换号；但没有任何可见内容交付 → 不进 partial
// settlement、全额释放预扣、交付标记为中断。这是「口径切换不改变金钱规则」的直接体现。
func TestStreamResponse_UpstreamDiesDuringThinkingReleasesWithoutFallback(t *testing.T) {
	t.Cleanup(func() { adapter.SetTTFTMode(adapter.DefaultTTFTMode) })
	adapter.SetTTFTMode(adapter.TTFTModeSemantic)

	first := &thinkingStreamAdapter{reasoningItems: 2, step: 5 * time.Millisecond, failAfter: true}
	second := &thinkingStreamAdapter{reasoningItems: 0, step: time.Millisecond}
	registry := &fakeRegistry{streamResponsesAdapters: map[string]responsesadapter.StreamResponsesAdapter{
		"codex-a": first, "codex-b": second,
	}}
	router := &fakeRouter{plan: routing.ChatRoutePlan{Candidates: []routing.ChatRouteCandidate{
		candidate("codex-a", 1, "gpt-5.6-up"),
		candidate("codex-b", 2, "gpt-5.6-up"),
	}}}
	settlement := &fakeSettlement{}
	authorizer := &fakeAuthorizer{}
	requestLog := newFakeRequestLog()
	svc := NewResponsesService(router, registry, passthroughPreparer{}, alwaysRetryClassifier{}, requestLog, settlement, authorizer, nil, nil)

	var events []string
	err := svc.StreamResponse(ctxWithPrincipal(), directRequest(), func(ev dto.ResponsesStreamEvent) error {
		events = append(events, ev.Type)
		return nil
	})
	if err == nil {
		t.Fatal("upstream failure during reasoning must surface as an error")
	}
	var upstreamErr *adapter.UpstreamError
	if !errors.As(err, &upstreamErr) {
		t.Fatalf("expected the upstream error to propagate, got %v", err)
	}
	if first.called != 1 || second.called != 0 {
		t.Fatalf("progress must lock fallback: calls first=%d second=%d", first.called, second.called)
	}
	if len(events) == 0 || events[0] != "response.created" {
		t.Fatalf("prelude and reasoning events must have been delivered before the failure, got %v", events)
	}
	if len(settlement.params) != 0 {
		t.Fatalf("no visible content was delivered, partial settlement must not run: %d", len(settlement.params))
	}
	if authorizer.releaseCount != 1 {
		t.Fatalf("authorization must be released exactly once, got %d", authorizer.releaseCount)
	}
	if len(requestLog.deliveryInterrupted) != 1 {
		t.Fatalf("delivery must be marked interrupted once, got %v", requestLog.deliveryInterrupted)
	}
}
