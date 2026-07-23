package responses

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	gatewayapi "github.com/ThankCat/unio-gateway/internal/app/gatewayapi/openai/responses"
	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	chatcompletionsadapter "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/chatcompletions"
	responsesadapter "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/responses"
	"github.com/ThankCat/unio-gateway/internal/core/channel"
	"github.com/ThankCat/unio-gateway/internal/core/requestlog"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	observabilitymetrics "github.com/ThankCat/unio-gateway/internal/platform/observability/metrics"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/lifecycle"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/requestadmission"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/runtimefacts"
)

// fakeCompactAdapter 是原生 /responses/compact 直传 adapter 的测试替身：记录上送请求体并返回预置原文或错误。
type fakeCompactAdapter struct {
	called  int
	gotBody json.RawMessage
	resp    *responsesadapter.Response
	err     error
}

func (a *fakeCompactAdapter) CompactResponse(ctx context.Context, _ channel.Runtime, req responsesadapter.Request) (*responsesadapter.Response, error) {
	a.called++
	a.gotBody = req.Body
	adapter.MarkTransportStarted(ctx)
	if a.err != nil {
		return nil, a.err
	}
	return a.resp, nil
}

type compactPermitStep struct {
	admission breakerstore.AttemptAdmission
	err       error
}

type compactPermitStore struct {
	mu sync.Mutex

	steps          []compactPermitStep
	acquireInputs  []breakerstore.AcquireAttemptInput
	finishPermits  []breakerstore.AttemptPermit
	finishOutcomes []breakerstore.FinishOutcome
	abortPermits   []breakerstore.AttemptPermit
}

type compactRequestMetric struct {
	stream  bool
	outcome observabilitymetrics.ChatOutcome
}

type compactUpstreamMetric struct {
	provider      string
	channel       string
	success       bool
	errorCategory string
}

type compactMetricsRecorder struct {
	requests    []compactRequestMetric
	upstreams   []compactUpstreamMetric
	routing     int
	settlements []observabilitymetrics.SettlementOutcome
}

func (r *compactMetricsRecorder) IncChatRequest(stream bool, outcome observabilitymetrics.ChatOutcome) {
	r.requests = append(r.requests, compactRequestMetric{stream: stream, outcome: outcome})
}

func (r *compactMetricsRecorder) IncRoutingSelected(string, string, string) { r.routing++ }

func (r *compactMetricsRecorder) ObserveUpstream(provider, channel string, success bool, errorCategory string, _ time.Duration) {
	r.upstreams = append(r.upstreams, compactUpstreamMetric{
		provider: provider, channel: channel, success: success, errorCategory: errorCategory,
	})
}

func (r *compactMetricsRecorder) IncSettlement(outcome observabilitymetrics.SettlementOutcome) {
	r.settlements = append(r.settlements, outcome)
}

func (*compactMetricsRecorder) IncStreamEvent(observabilitymetrics.StreamEvent) {}
func (*compactMetricsRecorder) IncPartialSettlement(string)                     {}
func (*compactMetricsRecorder) IncRetryableFallback(string)                     {}
func (*compactMetricsRecorder) IncZeroPriceServed(string, string, string)       {}
func (*compactMetricsRecorder) IncRoutingSkip(string)                           {}
func (*compactMetricsRecorder) ObserveRoutingHeadWait(time.Duration)            {}

func (s *compactPermitStore) AcquireAttempt(_ context.Context, in breakerstore.AcquireAttemptInput) (breakerstore.AttemptAdmission, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	call := len(s.acquireInputs)
	s.acquireInputs = append(s.acquireInputs, in)
	if call < len(s.steps) {
		step := s.steps[call]
		if step.err != nil {
			return breakerstore.AttemptAdmission{}, step.err
		}
		if step.admission.Mode == breakerstore.AdmissionDenied {
			return step.admission, nil
		}
	}
	permit := breakerstore.AttemptPermit{
		PermitID: in.PermitID, RequestAdmissionID: in.RequestAdmissionID,
		IntegrityEpoch: in.IntegrityEpoch, IntegrityRevision: in.IntegrityRevision,
		EndpointID: in.EndpointID, ChannelID: in.ChannelID,
		EndpointBaseURLRevision: in.EndpointBaseURLRevision,
		EndpointStatusRevision:  in.EndpointStatusRevision,
		ChannelConfigRevision:   in.ChannelConfigRevision,
		ModelID:                 in.ModelID, UpstreamOperation: in.UpstreamOperation, RequestMode: in.RequestMode,
		PermitTTLMs: 30_000, RenewMs: 10_000, TerminalTTLMs: 300_000,
	}
	return breakerstore.AttemptAdmission{Mode: breakerstore.AdmissionPermit, Permit: &permit}, nil
}

func (s *compactPermitStore) Renew(context.Context, breakerstore.AttemptPermit) error { return nil }

func (s *compactPermitStore) Finish(_ context.Context, permit breakerstore.AttemptPermit, outcome breakerstore.FinishOutcome) (breakerstore.FinishResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finishPermits = append(s.finishPermits, permit)
	s.finishOutcomes = append(s.finishOutcomes, outcome)
	return breakerstore.FinishResult{
		EndpointDisposition: breakerstore.DispositionApplied,
		ChannelDisposition:  breakerstore.DispositionApplied,
	}, nil
}

func (s *compactPermitStore) Abort(_ context.Context, permit breakerstore.AttemptPermit) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.abortPermits = append(s.abortPermits, permit)
	return nil
}

type compactRuntimeFacts struct{}

func (compactRuntimeFacts) Integrity(context.Context) (runtimefacts.Integrity, error) {
	return runtimefacts.Integrity{Epoch: "epoch-compact", Revision: 7}, nil
}

func (compactRuntimeFacts) Admission(context.Context) (runtimefacts.AdmissionRevisions, error) {
	integrity := runtimefacts.Integrity{Epoch: "epoch-compact", Revision: 7}
	return runtimefacts.AdmissionRevisions{
		Integrity: integrity, RouteRateLimits: 8, ChannelRateLimits: 12, Concurrency: 9,
	}, nil
}

func (compactRuntimeFacts) Routing(context.Context) (runtimefacts.RoutingRevisions, error) {
	integrity := runtimefacts.Integrity{Epoch: "epoch-compact", Revision: 7}
	return runtimefacts.RoutingRevisions{Integrity: integrity, CircuitBreaker: 10, RoutingBalance: 11}, nil
}

type compactUsageSession struct {
	requestID string
	reserved  int64
}

func (s *compactUsageSession) Reserve(_ context.Context, tokens int64) error {
	s.reserved = tokens
	return nil
}

func (*compactUsageSession) PublishAuthoritativeUsage(int64) bool { return true }

func (s *compactUsageSession) BindAttempt(in *breakerstore.AcquireAttemptInput) error {
	if in.EstimatedInputTokens != s.reserved {
		return errors.New("attempt estimate differs from request reservation")
	}
	in.RequestAdmissionID = s.requestID
	return nil
}

func compactPermitContext() (context.Context, *compactUsageSession) {
	session := &compactUsageSession{requestID: "request-admission-compact"}
	return requestadmission.ContextWithUsageSession(ctxWithPrincipal(), session), session
}

func setCompactPermitManager(svc *ResponsesService, store *compactPermitStore) {
	svc.SetAttemptPermitManager(lifecycle.NewAttemptPermitManager(
		store,
		compactRuntimeFacts{},
		lifecycle.AttemptPermitManagerOptions{Logger: zap.NewNop(), OperationTimeout: 100 * time.Millisecond},
	))
}

func compactUnsupportedError(status int) error {
	return adapter.NewUpstreamError(
		adapter.UpstreamErrorBadRequest,
		adapter.UpstreamMetadata{StatusCode: status, RequestID: "req-compact-unsupported"},
		failure.Wrap(
			failure.CodeAdapterRequestUnsupported,
			responsesadapter.ErrCompactUnsupported,
			failure.WithMessage("simulated native compact unsupported"),
		),
	)
}

// nativeCompactResponse 构造一个含 compaction item + encrypted_content + usage 的上游压缩响应原文。
func nativeCompactResponse() *responsesadapter.Response {
	raw := json.RawMessage(`{"id":"resp_compact","object":"response","model":"gpt-5.5-upstream","output":[{"type":"compaction","encrypted_content":"enc-blob"}],"usage":{"input_tokens":40,"output_tokens":6,"total_tokens":46}}`)
	meta := adapter.UpstreamMetadata{StatusCode: 200, RequestID: "req-compact"}
	return &responsesadapter.Response{
		Raw:        raw,
		ResponseID: "resp_compact",
		Model:      "gpt-5.5-upstream",
		Usage:      adapter.ChatUsage{PromptTokens: 40, CompletionTokens: 6, TotalTokens: 46},
		Upstream:   meta,
		Facts: adapter.ResponseFacts{
			UpstreamProtocol:    "openai",
			UpstreamResponseID:  "resp_compact",
			UpstreamModel:       "gpt-5.5-upstream",
			Finish:              adapter.FinishFacts{Class: adapter.FinishStop, RawReason: "stop"},
			UsageMappingVersion: "chatcompletionsadapter.responses.v1",
			Metadata:            meta,
		},
	}
}

func compactNativeRequest() gatewayapi.ResponsesRequest {
	instructions := "compact please"
	text := "long history to compact"
	return gatewayapi.ResponsesRequest{
		Model:        "gpt-5.5",
		Instructions: &instructions,
		Input:        gatewayapi.ResponsesInput{Text: &text},
	}
}

// TestCompactHistory_NativePassthrough 验证：候选 adapter 注册了原生 compact 能力时走 NativeCompact——
// 透传上游 /responses/compact，响应原文返回（仅顶层 model 回显改写为客户请求名），chat 摘要零触达，
// settlement 落原生 facts。
func TestCompactHistory_NativePassthrough(t *testing.T) {
	compactAdapter := &fakeCompactAdapter{resp: nativeCompactResponse()}
	// chat 适配器同时存在但不应被触达（原生 compact 命中，不落 synthetic）。
	chatAdapter := &fakeChatAdapter{resp: okChatResponse()}
	registry := &fakeRegistry{
		adapters:                 map[string]chatcompletionsadapter.ChatAdapter{"openai": chatAdapter},
		tokenizers:               map[string]chatcompletionsadapter.ChatInputTokenizer{"openai": fakeTokenizer{}},
		responsesCompactAdapters: map[string]responsesadapter.ResponsesCompactAdapter{"openai": compactAdapter},
	}
	router := &fakeRouter{plan: routing.ChatRoutePlan{Candidates: []routing.ChatRouteCandidate{candidate("openai", 1, "gpt-5.5-upstream")}}}
	settlement := &fakeSettlement{}

	svc := newServiceForTest(router, registry, settlement, &fakeAuthorizer{}, newFakeRequestLog())

	result, err := svc.CompactHistory(ctxWithPrincipal(), compactNativeRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	resp := result.Response
	// 原生 compact 命中一次，synthetic chat 完全未触达。
	if compactAdapter.called != 1 {
		t.Fatalf("expected native compact called once, got %d", compactAdapter.called)
	}
	if chatAdapter.req.Model != "" {
		t.Fatalf("synthetic chat must not be invoked for native compact, got model %q", chatAdapter.req.Model)
	}

	// 上送上游请求体 model 改写为 upstream model。
	var upBody map[string]json.RawMessage
	if err := json.Unmarshal(compactAdapter.gotBody, &upBody); err != nil {
		t.Fatalf("decode upstream body: %v", err)
	}
	if string(upBody["model"]) != `"gpt-5.5-upstream"` {
		t.Fatalf("upstream model = %s, want \"gpt-5.5-upstream\"", upBody["model"])
	}

	// 客户响应：上游原文透传，仅顶层 model 回显改写为客户请求名；compaction item 原样保留。
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got["model"] != "gpt-5.5" {
		t.Fatalf("client model = %v, want gpt-5.5 (rewritten)", got["model"])
	}
	output, ok := got["output"].([]any)
	if !ok || len(output) != 1 {
		t.Fatalf("expected single passthrough output item, got %v", got["output"])
	}
	item, _ := output[0].(map[string]any)
	if item["type"] != "compaction" || item["encrypted_content"] != "enc-blob" {
		t.Fatalf("native compaction item lost in passthrough: %v", item)
	}

	// settlement 落原生 ResponseFacts。
	if len(settlement.params) != 1 {
		t.Fatalf("expected 1 settlement, got %d", len(settlement.params))
	}
}

// TestCompactHistory_NativeFallbackToSynthetic 验证 Q2：原生 compact 命中「上游不支持」时自动回落 Synthetic——
// 同一候选改走 chat 摘要，输出包成单条 assistant message，仍走一次 settlement（不中断 Codex）。
func TestCompactHistory_NativeFallbackToSynthetic(t *testing.T) {
	compactAdapter := &fakeCompactAdapter{err: compactUnsupportedError(http.StatusNotFound)}
	chatAdapter := &fakeChatAdapter{resp: okChatResponse()}
	registry := &fakeRegistry{
		adapters:                 map[string]chatcompletionsadapter.ChatAdapter{"openai": chatAdapter},
		tokenizers:               map[string]chatcompletionsadapter.ChatInputTokenizer{"openai": fakeTokenizer{}},
		responsesCompactAdapters: map[string]responsesadapter.ResponsesCompactAdapter{"openai": compactAdapter},
	}
	router := &fakeRouter{plan: routing.ChatRoutePlan{Candidates: []routing.ChatRouteCandidate{candidate("openai", 1, "gpt-5.5-upstream")}}}
	settlement := &fakeSettlement{}
	requestLog := newFakeRequestLog()
	permitStore := &compactPermitStore{}

	svc := newServiceForTest(router, registry, settlement, &fakeAuthorizer{}, requestLog)
	setCompactPermitManager(svc, permitStore)
	ctx, session := compactPermitContext()

	result, err := svc.CompactHistory(ctx, compactNativeRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	resp := result.Response
	// 原生先尝试一次（失败），随后回落 synthetic chat（用 upstream model）。
	if compactAdapter.called != 1 {
		t.Fatalf("expected native compact attempted once, got %d", compactAdapter.called)
	}
	if chatAdapter.req.Model != "gpt-5.5-upstream" {
		t.Fatalf("expected synthetic fallback chat with upstream model, got %q", chatAdapter.req.Model)
	}

	// synthetic 输出：单条 assistant message 承载摘要。
	if len(resp.Output) != 1 || resp.Output[0].Type != "message" ||
		len(resp.Output[0].Content) != 1 || resp.Output[0].Content[0].Text != "hi there" {
		t.Fatalf("unexpected synthetic compaction output: %+v", resp.Output)
	}
	if len(settlement.params) != 1 {
		t.Fatalf("expected 1 settlement, got %d", len(settlement.params))
	}
	if session.reserved != 16 {
		t.Fatalf("request admission reserved tokens = %d, want 16 once for both transports", session.reserved)
	}
	if len(permitStore.acquireInputs) != 2 {
		t.Fatalf("attempt permit acquire count = %d, want 2", len(permitStore.acquireInputs))
	}
	firstPermit, secondPermit := permitStore.acquireInputs[0], permitStore.acquireInputs[1]
	if firstPermit.UpstreamOperation != breakerstore.OpResponsesCompact || secondPermit.UpstreamOperation != breakerstore.OpChatCompletions {
		t.Fatalf("permit operations = %q/%q, want responses_compact/chat_completions", firstPermit.UpstreamOperation, secondPermit.UpstreamOperation)
	}
	if firstPermit.PermitID == secondPermit.PermitID || firstPermit.RequestAdmissionID != secondPermit.RequestAdmissionID || firstPermit.RequestAdmissionID != session.requestID {
		t.Fatalf("permits must be unique under one request session: first=%+v second=%+v", firstPermit, secondPermit)
	}
	if firstPermit.EstimatedInputTokens != 16 || secondPermit.EstimatedInputTokens != 16 {
		t.Fatalf("permit estimates = %d/%d, want shared conservative estimate 16", firstPermit.EstimatedInputTokens, secondPermit.EstimatedInputTokens)
	}
	if len(permitStore.finishPermits) != 2 || len(permitStore.finishOutcomes) != 2 {
		t.Fatalf("permit finishes = %d/%d, want 2/2", len(permitStore.finishPermits), len(permitStore.finishOutcomes))
	}
	firstOutcome := permitStore.finishOutcomes[0]
	if firstOutcome.EndpointOutcome != breakerstore.OutcomeIgnored || firstOutcome.ChannelOutcome != breakerstore.OutcomeIgnored || firstOutcome.ChannelTPMActual != nil {
		t.Fatalf("native 404 finish must be breaker-ignored and release TPM estimate: %+v", firstOutcome)
	}
	if permitStore.finishOutcomes[1].ChannelTPMActual == nil {
		t.Fatalf("synthetic success must finish with authoritative TPM actual: %+v", permitStore.finishOutcomes[1])
	}
	if len(requestLog.createAttempts) != 2 {
		t.Fatalf("attempt count = %d, want 2", len(requestLog.createAttempts))
	}
	if len(requestLog.markAttemptFailed) != 1 || requestLog.markAttemptFailed[0].UpstreamStatusCode == nil ||
		*requestLog.markAttemptFailed[0].UpstreamStatusCode != http.StatusNotFound {
		t.Fatalf("native unsupported attempt did not preserve 404 facts: %+v", requestLog.markAttemptFailed)
	}
	firstAttempt, secondAttempt := requestLog.createAttempts[0], requestLog.createAttempts[1]
	if firstAttempt.AttemptIndex != 0 || secondAttempt.AttemptIndex != 1 ||
		firstAttempt.RoutingCandidateIndex == nil || secondAttempt.RoutingCandidateIndex == nil ||
		*firstAttempt.RoutingCandidateIndex != 0 || *secondAttempt.RoutingCandidateIndex != 0 ||
		firstAttempt.UpstreamOperation != requestlog.UpstreamOperationResponsesCompact ||
		secondAttempt.UpstreamOperation != requestlog.UpstreamOperationChatCompletions {
		t.Fatalf("unexpected compact fallback attempts: first=%+v second=%+v", firstAttempt, secondAttempt)
	}
}

// TestCompactHistory_NativeFallbackToSyntheticLocalUpstream drives both production adapters against
// one local HTTP upstream. A native 404/405 and the synthetic success are two real transports under
// one ingress request, so each must own its own permit, attempt, timing and Channel observation.
func TestCompactHistory_NativeFallbackToSyntheticLocalUpstream(t *testing.T) {
	for _, nativeStatus := range []int{http.StatusNotFound, http.StatusMethodNotAllowed} {
		t.Run(http.StatusText(nativeStatus), func(t *testing.T) {
			type upstreamCall struct {
				method string
				path   string
				auth   string
				model  string
			}

			var (
				callsMu sync.Mutex
				calls   []upstreamCall
			)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Model string `json:"model"`
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				callsMu.Lock()
				calls = append(calls, upstreamCall{
					method: r.Method,
					path:   r.URL.Path,
					auth:   r.Header.Get("Authorization"),
					model:  body.Model,
				})
				callsMu.Unlock()

				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/v1/responses/compact":
					w.Header().Set("X-Request-Id", "req-native-unsupported")
					w.WriteHeader(nativeStatus)
					_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","message":"compact unsupported"}}`))
				case "/v1/chat/completions":
					w.Header().Set("X-Request-Id", "req-synthetic-success")
					_, _ = w.Write([]byte(`{"id":"chatcmpl-local","object":"chat.completion","created":1700000123,"model":"gpt-5.5-upstream","choices":[{"index":0,"message":{"role":"assistant","content":"local summary"},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":8,"total_tokens":20}}`))
				default:
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = w.Write([]byte(`{"error":{"message":"unexpected path"}}`))
				}
			}))
			defer server.Close()

			nativeAdapter := responsesadapter.NewAdapter(server.Client())
			syntheticAdapter := chatcompletionsadapter.NewAdapter(server.Client())
			registry := &fakeRegistry{
				adapters:                 map[string]chatcompletionsadapter.ChatAdapter{"openai": syntheticAdapter},
				tokenizers:               map[string]chatcompletionsadapter.ChatInputTokenizer{"openai": syntheticAdapter},
				responsesCompactAdapters: map[string]responsesadapter.ResponsesCompactAdapter{"openai": nativeAdapter},
			}
			routeCandidate := candidate("openai", 1, "gpt-5.5-upstream")
			routeCandidate.Protocol = string(requestlog.ProtocolOpenAI)
			routeCandidate.ProviderEndpointID = 701
			routeCandidate.ProviderEndpointBaseURLRevision = 3
			routeCandidate.ProviderEndpointStatusRevision = 4
			routeCandidate.ChannelConfigRevision = 5
			routeCandidate.Channel.BaseURL = server.URL
			routeCandidate.Channel.APIKey = "local-test-secret"
			router := &fakeRouter{plan: routing.ChatRoutePlan{Candidates: []routing.ChatRouteCandidate{routeCandidate}}}
			requestLog := newFakeRequestLog()
			settlement := &fakeSettlement{}
			metricsRecorder := &compactMetricsRecorder{}
			permitStore := &compactPermitStore{}
			svc := NewResponsesService(
				router,
				registry,
				passthroughPreparer{},
				lifecycle.NeverRetryClassifier{},
				requestLog,
				settlement,
				&fakeAuthorizer{},
				metricsRecorder,
				zap.NewNop(),
			)
			setCompactPermitManager(svc, permitStore)
			ctx, session := compactPermitContext()

			result, err := svc.CompactHistory(ctx, compactNativeRequest())
			if err != nil {
				t.Fatalf("CompactHistory returned error: %v", err)
			}
			if err := result.FinalizeDelivery(func(*gatewayapi.CompactHistoryResponse) error { return nil }); err != nil {
				t.Fatalf("FinalizeDelivery returned error: %v", err)
			}
			if len(result.Response.Output) != 1 || len(result.Response.Output[0].Content) != 1 ||
				result.Response.Output[0].Content[0].Text != "local summary" {
				t.Fatalf("unexpected local synthetic response: %+v", result.Response.Output)
			}

			callsMu.Lock()
			gotCalls := append([]upstreamCall(nil), calls...)
			callsMu.Unlock()
			if len(gotCalls) != 2 {
				t.Fatalf("local upstream calls = %d, want 2: %+v", len(gotCalls), gotCalls)
			}
			if gotCalls[0].method != http.MethodPost || gotCalls[0].path != "/v1/responses/compact" ||
				gotCalls[1].method != http.MethodPost || gotCalls[1].path != "/v1/chat/completions" {
				t.Fatalf("local upstream sequence = %+v", gotCalls)
			}
			for _, call := range gotCalls {
				if call.auth != "Bearer local-test-secret" || call.model != "gpt-5.5-upstream" {
					t.Fatalf("unexpected local upstream request: %+v", call)
				}
			}

			if len(requestLog.createRequests) != 1 || requestLog.createRequests[0].Stream ||
				requestLog.createRequests[0].Operation != requestlog.OperationResponses {
				t.Fatalf("ingress audit must contain one non-stream Responses request: %+v", requestLog.createRequests)
			}
			if len(requestLog.createAttempts) != 2 {
				t.Fatalf("attempt audit count = %d, want 2", len(requestLog.createAttempts))
			}
			firstAttempt, secondAttempt := requestLog.createAttempts[0], requestLog.createAttempts[1]
			if firstAttempt.RequestRecordID != secondAttempt.RequestRecordID ||
				firstAttempt.AttemptIndex != 0 || secondAttempt.AttemptIndex != 1 ||
				firstAttempt.UpstreamOperation != requestlog.UpstreamOperationResponsesCompact ||
				secondAttempt.UpstreamOperation != requestlog.UpstreamOperationChatCompletions {
				t.Fatalf("unexpected two-transport attempt audit: first=%+v second=%+v", firstAttempt, secondAttempt)
			}
			if len(requestLog.markAttemptFailed) != 1 || requestLog.markAttemptFailed[0].ID != 1 ||
				requestLog.markAttemptFailed[0].UpstreamStatusCode == nil ||
				*requestLog.markAttemptFailed[0].UpstreamStatusCode != nativeStatus ||
				requestLog.markAttemptFailed[0].UpstreamRequestID == nil ||
				*requestLog.markAttemptFailed[0].UpstreamRequestID != "req-native-unsupported" {
				t.Fatalf("native unsupported failure attribution = %+v", requestLog.markAttemptFailed)
			}
			if len(settlement.params) != 1 || settlement.params[0].AttemptRecord.ID != 2 ||
				settlement.params[0].Facts.Metadata.StatusCode != http.StatusOK ||
				settlement.params[0].Facts.Metadata.RequestID != "req-synthetic-success" {
				t.Fatalf("synthetic success attribution = %+v", settlement.params)
			}
			if len(requestLog.markFailed) != 0 || len(requestLog.deliveryCompleted) != 1 {
				t.Fatalf("request terminal audit failed=%+v delivery_completed=%+v", requestLog.markFailed, requestLog.deliveryCompleted)
			}

			if session.reserved != 16 || len(permitStore.acquireInputs) != 2 ||
				len(permitStore.finishPermits) != 2 || len(permitStore.abortPermits) != 0 {
				t.Fatalf("request/permit counts reserved=%d acquire=%d finish=%d abort=%d",
					session.reserved, len(permitStore.acquireInputs), len(permitStore.finishPermits), len(permitStore.abortPermits))
			}
			firstPermit, secondPermit := permitStore.acquireInputs[0], permitStore.acquireInputs[1]
			if firstPermit.PermitID == secondPermit.PermitID ||
				firstPermit.RequestAdmissionID != session.requestID || secondPermit.RequestAdmissionID != session.requestID ||
				firstPermit.UpstreamOperation != breakerstore.OpResponsesCompact ||
				secondPermit.UpstreamOperation != breakerstore.OpChatCompletions {
				t.Fatalf("unexpected independent permits: first=%+v second=%+v", firstPermit, secondPermit)
			}
			if len(permitStore.finishOutcomes) != 2 ||
				permitStore.finishOutcomes[0].EndpointOutcome != breakerstore.OutcomeIgnored ||
				permitStore.finishOutcomes[0].ChannelOutcome != breakerstore.OutcomeIgnored ||
				permitStore.finishOutcomes[0].ChannelTPMActual != nil ||
				permitStore.finishOutcomes[1].EndpointOutcome != breakerstore.OutcomeEligibleSuccess ||
				permitStore.finishOutcomes[1].ChannelOutcome != breakerstore.OutcomeEligibleSuccess ||
				permitStore.finishOutcomes[1].ChannelTPMActual == nil ||
				*permitStore.finishOutcomes[1].ChannelTPMActual != 20 {
				t.Fatalf("breaker attribution = %+v", permitStore.finishOutcomes)
			}

			if len(metricsRecorder.requests) != 1 || metricsRecorder.requests[0] != (compactRequestMetric{
				stream: false, outcome: observabilitymetrics.ChatOutcomeSuccess,
			}) {
				t.Fatalf("ingress metrics = %+v, want one success", metricsRecorder.requests)
			}
			if len(metricsRecorder.upstreams) != 2 ||
				metricsRecorder.upstreams[0] != (compactUpstreamMetric{provider: "9001", channel: "1", success: false, errorCategory: string(adapter.UpstreamErrorBadRequest)}) ||
				metricsRecorder.upstreams[1] != (compactUpstreamMetric{provider: "9001", channel: "1", success: true}) {
				t.Fatalf("Channel upstream metrics = %+v, want ignored native failure plus synthetic success", metricsRecorder.upstreams)
			}
			if metricsRecorder.routing != 1 || len(metricsRecorder.settlements) != 1 ||
				metricsRecorder.settlements[0] != observabilitymetrics.SettlementOutcomeSuccess {
				t.Fatalf("final success metrics routing=%d settlements=%+v", metricsRecorder.routing, metricsRecorder.settlements)
			}
		})
	}
}

func TestCompactHistory_NativeFallbackDeniedSkipsSecondTransportAndContinuesCandidates(t *testing.T) {
	compactAdapter := &fakeCompactAdapter{err: compactUnsupportedError(http.StatusMethodNotAllowed)}
	deniedChat := &fakeChatAdapter{resp: okChatResponse()}
	fallbackChat := &fakeChatAdapter{resp: okChatResponse()}
	registry := &fakeRegistry{
		adapters: map[string]chatcompletionsadapter.ChatAdapter{
			"openai":   deniedChat,
			"deepseek": fallbackChat,
		},
		tokenizers: map[string]chatcompletionsadapter.ChatInputTokenizer{
			"openai":   fakeTokenizer{},
			"deepseek": fakeTokenizer{},
		},
		responsesCompactAdapters: map[string]responsesadapter.ResponsesCompactAdapter{"openai": compactAdapter},
	}
	router := &fakeRouter{plan: routing.ChatRoutePlan{Candidates: []routing.ChatRouteCandidate{
		candidate("openai", 1, "gpt-5.5-upstream"),
		candidate("deepseek", 2, "deepseek-chat"),
	}}}
	requestLog := newFakeRequestLog()
	permitStore := &compactPermitStore{steps: []compactPermitStep{
		{},
		{admission: breakerstore.AttemptAdmission{Mode: breakerstore.AdmissionDenied, Reason: breakerstore.ReasonRateLimited}},
		{},
	}}
	svc := newServiceForTest(router, registry, &fakeSettlement{}, &fakeAuthorizer{}, requestLog)
	setCompactPermitManager(svc, permitStore)
	ctx, _ := compactPermitContext()

	if _, err := svc.CompactHistory(ctx, compactNativeRequest()); err != nil {
		t.Fatalf("unexpected error after ordinary candidate fallback: %v", err)
	}
	if deniedChat.called != 0 {
		t.Fatalf("denied synthetic admission reached transport %d times", deniedChat.called)
	}
	if fallbackChat.called != 1 {
		t.Fatalf("next candidate transport calls = %d, want 1", fallbackChat.called)
	}
	if len(permitStore.acquireInputs) != 3 {
		t.Fatalf("permit acquires = %d, want native + denied synthetic + next candidate", len(permitStore.acquireInputs))
	}
	if len(permitStore.finishPermits) != 2 {
		t.Fatalf("permit finishes = %d, want only two real transports", len(permitStore.finishPermits))
	}
	if len(requestLog.createAttempts) != 2 {
		t.Fatalf("attempts = %d, want native plus next candidate (denied creates none)", len(requestLog.createAttempts))
	}
	first, second := requestLog.createAttempts[0], requestLog.createAttempts[1]
	if first.AttemptIndex != 0 || second.AttemptIndex != 1 ||
		first.RoutingCandidateIndex == nil || *first.RoutingCandidateIndex != 0 ||
		second.RoutingCandidateIndex == nil || *second.RoutingCandidateIndex != 1 {
		t.Fatalf("attempt sequence after denied transparent fallback = first=%+v second=%+v", first, second)
	}
}

func TestCompactHistory_NativeFallbackStoreErrorTerminates(t *testing.T) {
	compactAdapter := &fakeCompactAdapter{err: compactUnsupportedError(http.StatusNotFound)}
	firstChat := &fakeChatAdapter{resp: okChatResponse()}
	nextChat := &fakeChatAdapter{resp: okChatResponse()}
	registry := &fakeRegistry{
		adapters: map[string]chatcompletionsadapter.ChatAdapter{"openai": firstChat, "deepseek": nextChat},
		tokenizers: map[string]chatcompletionsadapter.ChatInputTokenizer{
			"openai": fakeTokenizer{}, "deepseek": fakeTokenizer{},
		},
		responsesCompactAdapters: map[string]responsesadapter.ResponsesCompactAdapter{"openai": compactAdapter},
	}
	router := &fakeRouter{plan: routing.ChatRoutePlan{Candidates: []routing.ChatRouteCandidate{
		candidate("openai", 1, "gpt-5.5-upstream"),
		candidate("deepseek", 2, "deepseek-chat"),
	}}}
	requestLog := newFakeRequestLog()
	permitStore := &compactPermitStore{steps: []compactPermitStep{{}, {err: breakerstore.ErrStoreUnavailable}}}
	svc := newServiceForTest(router, registry, &fakeSettlement{}, &fakeAuthorizer{}, requestLog)
	setCompactPermitManager(svc, permitStore)
	ctx, _ := compactPermitContext()

	_, err := svc.CompactHistory(ctx, compactNativeRequest())
	if failure.CodeOf(err) != failure.CodeGatewayBreakerStoreUnavailable {
		t.Fatalf("store error code = %q, want %q (err=%v)", failure.CodeOf(err), failure.CodeGatewayBreakerStoreUnavailable, err)
	}
	if firstChat.called != 0 || nextChat.called != 0 {
		t.Fatalf("store failure must stop all later transports, got first=%d next=%d", firstChat.called, nextChat.called)
	}
	if len(requestLog.createAttempts) != 1 || len(permitStore.finishPermits) != 1 {
		t.Fatalf("store failure should retain only native attempt/finish, attempts=%d finishes=%d", len(requestLog.createAttempts), len(permitStore.finishPermits))
	}
}

func TestCompactHistory_NativeOtherStatusesNeverInvokeSynthetic(t *testing.T) {
	for _, status := range []int{
		http.StatusBadRequest,
		http.StatusUnauthorized,
		http.StatusForbidden,
		http.StatusTooManyRequests,
		http.StatusUnprocessableEntity,
		http.StatusInternalServerError,
	} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			category := adapter.UpstreamErrorBadRequest
			if status >= 500 {
				category = adapter.UpstreamErrorServer
			}
			compactAdapter := &fakeCompactAdapter{err: adapter.NewUpstreamError(
				category,
				adapter.UpstreamMetadata{StatusCode: status},
				failure.New(failure.CodeAdapterUpstreamStatus),
			)}
			chatAdapter := &fakeChatAdapter{resp: okChatResponse()}
			registry := &fakeRegistry{
				adapters:                 map[string]chatcompletionsadapter.ChatAdapter{"openai": chatAdapter},
				tokenizers:               map[string]chatcompletionsadapter.ChatInputTokenizer{"openai": fakeTokenizer{}},
				responsesCompactAdapters: map[string]responsesadapter.ResponsesCompactAdapter{"openai": compactAdapter},
			}
			router := &fakeRouter{plan: routing.ChatRoutePlan{Candidates: []routing.ChatRouteCandidate{candidate("openai", 1, "gpt-5.5-upstream")}}}
			svc := newServiceForTest(router, registry, &fakeSettlement{}, &fakeAuthorizer{}, newFakeRequestLog())

			if _, err := svc.CompactHistory(ctxWithPrincipal(), compactNativeRequest()); err == nil {
				t.Fatalf("expected native status %d to fail", status)
			}
			if compactAdapter.called != 1 || chatAdapter.called != 0 {
				t.Fatalf("status %d transport calls native=%d synthetic=%d, want 1/0", status, compactAdapter.called, chatAdapter.called)
			}
		})
	}
}

// TestCompactHistory_NativeMissingUsageRecordsRiskExposure 验证 P0-3：原生 compact 返回 2xx 但缺可计费 usage
// 时，绝不静默回落 synthetic 白嫖；而是不再调用 chat、记 risk_exposure（账务异常释放）并向客户返回错误。
func TestCompactHistory_NativeMissingUsageRecordsRiskExposure(t *testing.T) {
	missingUsageErr := adapter.NewUpstreamError(
		adapter.UpstreamErrorServer,
		adapter.UpstreamMetadata{StatusCode: 200, RequestID: "req-missing-usage"},
		failure.Wrap(
			failure.CodeAdapterInvalidResponse,
			responsesadapter.ErrCompactMissingUsage,
			failure.WithMessage("simulated 200 without usage"),
		),
	)
	compactAdapter := &fakeCompactAdapter{err: missingUsageErr}
	chatAdapter := &fakeChatAdapter{resp: okChatResponse()}
	registry := &fakeRegistry{
		adapters:                 map[string]chatcompletionsadapter.ChatAdapter{"openai": chatAdapter},
		tokenizers:               map[string]chatcompletionsadapter.ChatInputTokenizer{"openai": fakeTokenizer{}},
		responsesCompactAdapters: map[string]responsesadapter.ResponsesCompactAdapter{"openai": compactAdapter},
	}
	router := &fakeRouter{plan: routing.ChatRoutePlan{Candidates: []routing.ChatRouteCandidate{candidate("openai", 1, "gpt-5.5-upstream")}}}
	authorizer := &fakeAuthorizer{}
	settlement := &fakeSettlement{}

	svc := newServiceForTest(router, registry, settlement, authorizer, newFakeRequestLog())
	// 即便回落开关打开，缺 usage 也不得回落（开关只管真 404/405）。
	svc.compactNativeFallback = true

	_, err := svc.CompactHistory(ctxWithPrincipal(), compactNativeRequest())
	if err == nil {
		t.Fatal("expected error when native compact returns 2xx without usage")
	}

	// 原生尝试一次；synthetic chat 绝不触达（避免双调上游、只收一次费白嫖）。
	if compactAdapter.called != 1 {
		t.Fatalf("expected native compact attempted once, got %d", compactAdapter.called)
	}
	if chatAdapter.req.Model != "" {
		t.Fatalf("synthetic must not run on missing-usage (no freeloading), got model %q", chatAdapter.req.Model)
	}

	// 不结算（无可靠 usage 不向用户扣费）。
	if len(settlement.params) != 0 {
		t.Fatalf("expected no settlement on missing-usage, got %d", len(settlement.params))
	}

	// 记一条 risk_exposure（账务异常释放），保留「平台可能承担成本」审计事实。
	if len(authorizer.billingExceptions) != 1 {
		t.Fatalf("expected exactly one billing-exception (risk_exposure) release, got %d", len(authorizer.billingExceptions))
	}
	if authorizer.billingExceptions[0].ReasonCode != "responses_compact_missing_usage" {
		t.Fatalf("unexpected risk_exposure reason code: %q", authorizer.billingExceptions[0].ReasonCode)
	}
	// 普通释放不应发生（走的是账务异常释放）。
	if authorizer.releaseCount != 0 {
		t.Fatalf("expected no plain release on missing-usage, got %d", authorizer.releaseCount)
	}
}

// TestCompactHistory_NativeFallbackDisabled 验证：关闭回落开关时，原生「不支持」错误直接上抛为请求失败，
// 不静默回落 synthetic（运营可显式关闭回落）。
func TestCompactHistory_NativeFallbackDisabled(t *testing.T) {
	compactAdapter := &fakeCompactAdapter{err: failure.Wrap(
		failure.CodeAdapterRequestUnsupported,
		responsesadapter.ErrCompactUnsupported,
		failure.WithMessage("simulated upstream 404"),
	)}
	chatAdapter := &fakeChatAdapter{resp: okChatResponse()}
	registry := &fakeRegistry{
		adapters:                 map[string]chatcompletionsadapter.ChatAdapter{"openai": chatAdapter},
		tokenizers:               map[string]chatcompletionsadapter.ChatInputTokenizer{"openai": fakeTokenizer{}},
		responsesCompactAdapters: map[string]responsesadapter.ResponsesCompactAdapter{"openai": compactAdapter},
	}
	router := &fakeRouter{plan: routing.ChatRoutePlan{Candidates: []routing.ChatRouteCandidate{candidate("openai", 1, "gpt-5.5-upstream")}}}

	svc := newServiceForTest(router, registry, &fakeSettlement{}, &fakeAuthorizer{}, newFakeRequestLog())
	svc.compactNativeFallback = false

	if _, err := svc.CompactHistory(ctxWithPrincipal(), compactNativeRequest()); err == nil {
		t.Fatal("expected error when native compact unsupported and fallback disabled")
	}
	if chatAdapter.req.Model != "" {
		t.Fatalf("synthetic must not run when fallback disabled, got model %q", chatAdapter.req.Model)
	}
}
