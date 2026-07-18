package responses

import (
	"context"
	"errors"
	"testing"
	"time"

	gatewayapi "github.com/ThankCat/unio-gateway/internal/app/gatewayapi/openai/responses"
	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	chatcompletionsadapter "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/chatcompletions"
	responsesadapter "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/responses"
	"github.com/ThankCat/unio-gateway/internal/core/auth"
	"github.com/ThankCat/unio-gateway/internal/core/channel"
	"github.com/ThankCat/unio-gateway/internal/core/requestlog"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/lifecycle"
)

// --- 替身 ---

type fakeRouter struct {
	plan routing.ChatRoutePlan
	err  error
}

func (r *fakeRouter) PlanChat(_ context.Context, _ routing.ChatRouteRequest) (routing.ChatRoutePlan, error) {
	return r.plan, r.err
}

type fakeTokenizer struct{}

func (fakeTokenizer) CountChatInputTokens(_ chatcompletionsadapter.ChatRequest) (int64, error) {
	return 16, nil
}

type fakeRegistry struct {
	adapters       map[string]chatcompletionsadapter.ChatAdapter
	streamAdapters map[string]chatcompletionsadapter.StreamChatAdapter
	tokenizers     map[string]chatcompletionsadapter.ChatInputTokenizer

	responsesAdapters        map[string]responsesadapter.ResponsesAdapter
	streamResponsesAdapters  map[string]responsesadapter.StreamResponsesAdapter
	responsesTokenizers      map[string]responsesadapter.ResponsesInputTokenizer
	responsesCompactAdapters map[string]responsesadapter.ResponsesCompactAdapter
}

func (r *fakeRegistry) Chat(key string) (chatcompletionsadapter.ChatAdapter, bool) {
	a, ok := r.adapters[key]
	return a, ok
}

func (r *fakeRegistry) StreamChat(key string) (chatcompletionsadapter.StreamChatAdapter, bool) {
	a, ok := r.streamAdapters[key]
	return a, ok
}

func (r *fakeRegistry) ChatInputTokenizer(key string) (chatcompletionsadapter.ChatInputTokenizer, bool) {
	t, ok := r.tokenizers[key]
	return t, ok
}

func (r *fakeRegistry) Responses(key string) (responsesadapter.ResponsesAdapter, bool) {
	a, ok := r.responsesAdapters[key]
	return a, ok
}

func (r *fakeRegistry) StreamResponses(key string) (responsesadapter.StreamResponsesAdapter, bool) {
	a, ok := r.streamResponsesAdapters[key]
	return a, ok
}

func (r *fakeRegistry) ResponsesInputTokenizer(key string) (responsesadapter.ResponsesInputTokenizer, bool) {
	t, ok := r.responsesTokenizers[key]
	return t, ok
}

func (r *fakeRegistry) HasResponses(key string) bool {
	_, ok := r.responsesAdapters[key]
	return ok
}

func (r *fakeRegistry) HasStreamResponses(key string) bool {
	_, ok := r.streamResponsesAdapters[key]
	return ok
}

func (r *fakeRegistry) ResponsesCompact(key string) (responsesadapter.ResponsesCompactAdapter, bool) {
	a, ok := r.responsesCompactAdapters[key]
	return a, ok
}

func (r *fakeRegistry) HasResponsesCompact(key string) bool {
	_, ok := r.responsesCompactAdapters[key]
	return ok
}

type fakeChatAdapter struct {
	req  chatcompletionsadapter.ChatRequest
	resp *chatcompletionsadapter.ChatResponse
	err  error
}

func (a *fakeChatAdapter) ChatCompletions(_ context.Context, _ channel.Runtime, req chatcompletionsadapter.ChatRequest) (*chatcompletionsadapter.ChatResponse, error) {
	a.req = req
	return a.resp, a.err
}

type fakeSettlement struct {
	params []lifecycle.ChatSettlementParams
}

func (s *fakeSettlement) SettleSuccessfulChat(_ context.Context, params lifecycle.ChatSettlementParams) error {
	s.params = append(s.params, params)
	return nil
}

type fakeAuthorizer struct {
	authorizeErr      error
	releaseCount      int
	billingExceptions []lifecycle.ChatReleaseBillingExceptionParams
}

func (a *fakeAuthorizer) AuthorizeChat(_ context.Context, params lifecycle.ChatAuthorizeParams) (lifecycle.ChatAuthorization, error) {
	if a.authorizeErr != nil {
		return lifecycle.ChatAuthorization{}, a.authorizeErr
	}
	return lifecycle.ChatAuthorization{
		RequestRecordID: params.RequestRecord.ID,
		ReservationID:   42,
		Currency:        "USD",
	}, nil
}

func (a *fakeAuthorizer) ReleaseChat(_ context.Context, _ lifecycle.ChatReleaseAuthorizationParams) error {
	a.releaseCount++
	return nil
}

func (a *fakeAuthorizer) ReleaseChatForBillingException(_ context.Context, params lifecycle.ChatReleaseBillingExceptionParams) error {
	a.billingExceptions = append(a.billingExceptions, params)
	return nil
}

type fakeRequestLog struct {
	createRequests    []requestlog.CreateRequestParams
	markFailed        []requestlog.MarkRequestFailedParams
	markAttemptFailed []requestlog.MarkAttemptFailedParams
	capabilityResults []string
	nextRequestID     int64
	nextAttemptID     int64
}

func (s *fakeRequestLog) SetCapabilityCheckResult(_ context.Context, _ int64, result string) error {
	s.capabilityResults = append(s.capabilityResults, result)
	return nil
}

func newFakeRequestLog() *fakeRequestLog {
	return &fakeRequestLog{nextRequestID: 1, nextAttemptID: 1}
}

func (s *fakeRequestLog) CreateRequest(_ context.Context, params requestlog.CreateRequestParams) (requestlog.RequestRecord, error) {
	s.createRequests = append(s.createRequests, params)
	id := s.nextRequestID
	s.nextRequestID++
	return requestlog.RequestRecord{
		ID:              id,
		RequestID:       params.RequestID,
		UserID:          params.UserID,
		APIKeyID:        params.APIKeyID,
		IngressProtocol: params.IngressProtocol,
		Operation:       params.Operation,
		Status:          requestlog.RequestStatusPending,
		StartedAt:       params.StartedAt,
	}, nil
}

func (s *fakeRequestLog) MarkRequestRunning(_ context.Context, id int64) (requestlog.RequestRecord, error) {
	return requestlog.RequestRecord{ID: id, Status: requestlog.RequestStatusRunning}, nil
}

func (s *fakeRequestLog) MarkRequestResponseStarted(_ context.Context, params requestlog.MarkResponseStartedParams) (requestlog.RequestRecord, error) {
	return requestlog.RequestRecord{ID: params.ID, Status: requestlog.RequestStatusRunning, ResponseStartedAt: &params.ResponseStartedAt}, nil
}

func (s *fakeRequestLog) MarkRequestSucceeded(_ context.Context, params requestlog.MarkRequestSucceededParams) (requestlog.RequestRecord, error) {
	return requestlog.RequestRecord{ID: params.ID, Status: requestlog.RequestStatusSucceeded}, nil
}

func (s *fakeRequestLog) MarkRequestFailed(_ context.Context, params requestlog.MarkRequestFailedParams) (requestlog.RequestRecord, error) {
	s.markFailed = append(s.markFailed, params)
	return requestlog.RequestRecord{ID: params.ID, Status: requestlog.RequestStatusFailed}, nil
}

func (s *fakeRequestLog) MarkRequestCanceled(_ context.Context, params requestlog.MarkRequestCanceledParams) (requestlog.RequestRecord, error) {
	return requestlog.RequestRecord{ID: params.ID, Status: requestlog.RequestStatusCanceled}, nil
}

func (s *fakeRequestLog) MarkSettledRequestCanceled(_ context.Context, params requestlog.MarkSettledRequestCanceledParams) (requestlog.RequestRecord, error) {
	return requestlog.RequestRecord{ID: params.ID, Status: requestlog.RequestStatusCanceled}, nil
}

func (s *fakeRequestLog) MarkSettledRequestFailed(_ context.Context, params requestlog.MarkSettledRequestFailedParams) (requestlog.RequestRecord, error) {
	return requestlog.RequestRecord{ID: params.ID, Status: requestlog.RequestStatusFailed}, nil
}

func (s *fakeRequestLog) CreateAttempt(_ context.Context, params requestlog.CreateAttemptParams) (requestlog.AttemptRecord, error) {
	id := s.nextAttemptID
	s.nextAttemptID++
	return requestlog.AttemptRecord{
		ID:              id,
		RequestRecordID: params.RequestRecordID,
		AttemptIndex:    params.AttemptIndex,
		ProviderID:      params.ProviderID,
		ChannelID:       params.ChannelID,
		AdapterKey:      params.AdapterKey,
		UpstreamModel:   params.UpstreamModel,
		Status:          requestlog.AttemptStatusRunning,
		StartedAt:       params.StartedAt,
	}, nil
}

func (s *fakeRequestLog) MarkAttemptSucceeded(_ context.Context, params requestlog.MarkAttemptSucceededParams) (requestlog.AttemptRecord, error) {
	return requestlog.AttemptRecord{ID: params.ID, Status: requestlog.AttemptStatusSucceeded}, nil
}

func (s *fakeRequestLog) MarkAttemptResponseStarted(_ context.Context, params requestlog.MarkAttemptResponseStartedParams) (requestlog.AttemptRecord, error) {
	return requestlog.AttemptRecord{ID: params.ID, Status: requestlog.AttemptStatusRunning, ResponseStartedAt: &params.ResponseStartedAt}, nil
}

func (s *fakeRequestLog) MarkAttemptFailed(_ context.Context, params requestlog.MarkAttemptFailedParams) (requestlog.AttemptRecord, error) {
	s.markAttemptFailed = append(s.markAttemptFailed, params)
	return requestlog.AttemptRecord{ID: params.ID, Status: requestlog.AttemptStatusFailed}, nil
}

func (s *fakeRequestLog) MarkAttemptCanceled(_ context.Context, params requestlog.MarkAttemptCanceledParams) (requestlog.AttemptRecord, error) {
	return requestlog.AttemptRecord{ID: params.ID, Status: requestlog.AttemptStatusCanceled}, nil
}

func (s *fakeRequestLog) MarkSettledAttemptCanceled(_ context.Context, params requestlog.MarkSettledAttemptCanceledParams) (requestlog.AttemptRecord, error) {
	return requestlog.AttemptRecord{ID: params.ID, Status: requestlog.AttemptStatusCanceled}, nil
}

func (s *fakeRequestLog) MarkSettledAttemptFailed(_ context.Context, params requestlog.MarkSettledAttemptFailedParams) (requestlog.AttemptRecord, error) {
	return requestlog.AttemptRecord{ID: params.ID, Status: requestlog.AttemptStatusFailed}, nil
}

type passthroughPreparer struct{}

func (passthroughPreparer) PrepareCandidates(_ context.Context, params lifecycle.PrepareCandidatesParams) (lifecycle.CandidatePlan, error) {
	plan := lifecycle.CandidatePlan{ConservativeInputTokens: 16}
	for index, candidate := range params.Candidates {
		plan.Candidates = append(plan.Candidates, lifecycle.Candidate{RouteIndex: index, Route: candidate})
	}
	return plan, nil
}

// recordingPreparer 记录最近一次 PrepareCandidates 传入的能力过滤集，用于断言 stream/非 stream 选取。
type recordingPreparer struct {
	capabilities []lifecycle.AdapterCapability
}

func (p *recordingPreparer) PrepareCandidates(_ context.Context, params lifecycle.PrepareCandidatesParams) (lifecycle.CandidatePlan, error) {
	p.capabilities = params.Capabilities
	plan := lifecycle.CandidatePlan{ConservativeInputTokens: 16}
	for index, candidate := range params.Candidates {
		plan.Candidates = append(plan.Candidates, lifecycle.Candidate{RouteIndex: index, Route: candidate})
	}
	return plan, nil
}

func hasCapability(caps []lifecycle.AdapterCapability, want lifecycle.AdapterCapability) bool {
	for _, c := range caps {
		if c == want {
			return true
		}
	}
	return false
}

// --- helpers ---

func candidate(adapterKey string, channelID int64, upstreamModel string) routing.ChatRouteCandidate {
	return routing.ChatRouteCandidate{
		ModelDBID:  1000 + channelID,
		ProviderID: 9000 + channelID,
		AdapterKey: adapterKey,
		Channel: channel.Runtime{
			ID:      channelID,
			BaseURL: "https://example.test/v1",
			APIKey:  "secret",
			Timeout: 30 * time.Second,
		},
		UpstreamModel: upstreamModel,
	}
}

func okChatResponse() *chatcompletionsadapter.ChatResponse {
	usage := adapter.ChatUsage{PromptTokens: 12, CompletionTokens: 8, TotalTokens: 20}
	meta := adapter.UpstreamMetadata{StatusCode: 200, RequestID: "req-1"}
	return &chatcompletionsadapter.ChatResponse{
		ID:           "chatcmpl_upstream",
		Model:        "deepseek-v4-flash",
		Content:      "hi there",
		FinishReason: "stop",
		Usage:        usage,
		Created:      1700000123,
		Upstream:     meta,
		Facts: adapter.ResponseFacts{
			UpstreamProtocol:    "openai",
			UpstreamResponseID:  "chatcmpl_upstream",
			UpstreamModel:       "deepseek-v4-flash",
			Finish:              adapter.FinishFacts{Class: adapter.FinishStop, RawReason: "stop"},
			UsageMappingVersion: "chatcompletionsadapter.v1",
			Metadata:            meta,
		},
	}
}

func ctxWithPrincipal() context.Context {
	ctx := httpx.ContextWithRequestID(context.Background(), "responses-test-request-id")
	return auth.ContextWithAPIKeyPrincipal(ctx, &auth.APIKeyPrincipal{
		APIKeyID: 1, UserID: 7, KeyPrefix: "unio_sk_test",
	})
}

func newServiceForTest(router ChatRouter, registry AdapterRegistry, settlement lifecycle.ChatSettlementExecutor, authorizer lifecycle.ChatAuthorizer, requestLog requestlog.Service) *ResponsesService {
	return NewResponsesService(
		router,
		registry,
		passthroughPreparer{},
		lifecycle.NeverRetryClassifier{},
		requestLog,
		settlement,
		authorizer,
		nil,
		nil,
		nil,
	)
}

func instructionsRequest() gatewayapi.ResponsesRequest {
	instructions := "You are Unio."
	text := "create hello.txt"
	return gatewayapi.ResponsesRequest{
		Model:        "unio-deepseek",
		Instructions: &instructions,
		Input:        gatewayapi.ResponsesInput{Text: &text},
	}
}

// --- tests ---

// TestPrepareResponsesCandidatesStreamCapability 验证流式/非流式请求分别按 Stream/NonStream 能力过滤候选，
// 避免流式 Responses 复用非流式过滤（仅支持单一模式的候选会被误选/误排，authorization 后在 adapter 阶段失败）。
func TestPrepareResponsesCandidatesStreamCapability(t *testing.T) {
	rec := &recordingPreparer{}
	svc := NewResponsesService(
		&fakeRouter{},
		&fakeRegistry{tokenizers: map[string]chatcompletionsadapter.ChatInputTokenizer{}},
		rec,
		lifecycle.NeverRetryClassifier{},
		newFakeRequestLog(),
		&fakeSettlement{},
		&fakeAuthorizer{},
		nil,
		nil,
		nil,
	)
	req := gatewayapi.ResponsesRequest{Model: "m"}
	cands := []routing.ChatRouteCandidate{candidate("deepseek", 1, "deepseek-v4-flash")}

	// allowDirect=true（CreateResponse/StreamResponse 生产路径）：按 responses-serve 能力分流过滤，
	// 流式只保留 serve-stream，非流式只保留 serve-nonstream，避免单一模式候选误选。
	if _, err := svc.prepareResponsesCandidates(context.Background(), req, cands, "", true, true, 0); err != nil {
		t.Fatalf("stream prepare: %v", err)
	}
	if !hasCapability(rec.capabilities, lifecycle.AdapterCapabilityResponsesServeStream) || hasCapability(rec.capabilities, lifecycle.AdapterCapabilityResponsesServeNonStream) {
		t.Fatalf("stream=true capabilities = %v, want ResponsesServeStream and not ResponsesServeNonStream", rec.capabilities)
	}
	if !hasCapability(rec.capabilities, lifecycle.AdapterCapabilityResponsesServeTokenizer) {
		t.Fatalf("stream=true capabilities = %v, want ResponsesServeTokenizer present", rec.capabilities)
	}

	if _, err := svc.prepareResponsesCandidates(context.Background(), req, cands, "", false, true, 0); err != nil {
		t.Fatalf("non-stream prepare: %v", err)
	}
	if !hasCapability(rec.capabilities, lifecycle.AdapterCapabilityResponsesServeNonStream) || hasCapability(rec.capabilities, lifecycle.AdapterCapabilityResponsesServeStream) {
		t.Fatalf("stream=false capabilities = %v, want ResponsesServeNonStream and not ResponsesServeStream", rec.capabilities)
	}

	// allowDirect=false（CompactHistory 强制桥接）：退回纯 chat 桥接能力过滤。
	if _, err := svc.prepareResponsesCandidates(context.Background(), req, cands, "", false, false, 0); err != nil {
		t.Fatalf("bridge-only prepare: %v", err)
	}
	if !hasCapability(rec.capabilities, lifecycle.AdapterCapabilityNonStream) || !hasCapability(rec.capabilities, lifecycle.AdapterCapabilityInputTokenizer) {
		t.Fatalf("allowDirect=false capabilities = %v, want chat NonStream + InputTokenizer", rec.capabilities)
	}
}

func TestCreateResponse_HappyPath(t *testing.T) {
	chatAdapter := &fakeChatAdapter{resp: okChatResponse()}
	registry := &fakeRegistry{
		adapters:   map[string]chatcompletionsadapter.ChatAdapter{"deepseek": chatAdapter},
		tokenizers: map[string]chatcompletionsadapter.ChatInputTokenizer{"deepseek": fakeTokenizer{}},
	}
	router := &fakeRouter{plan: routing.ChatRoutePlan{Candidates: []routing.ChatRouteCandidate{candidate("deepseek", 1, "deepseek-v4-flash")}}}
	settlement := &fakeSettlement{}
	authorizer := &fakeAuthorizer{}
	requestLog := newFakeRequestLog()

	svc := newServiceForTest(router, registry, settlement, authorizer, requestLog)

	resp, err := svc.CreateResponse(ctxWithPrincipal(), instructionsRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 请求审计落 Operation=responses、IngressProtocol=openai。
	if len(requestLog.createRequests) != 1 {
		t.Fatalf("expected 1 create request, got %d", len(requestLog.createRequests))
	}
	if requestLog.createRequests[0].Operation != requestlog.OperationResponses {
		t.Fatalf("expected operation responses, got %q", requestLog.createRequests[0].Operation)
	}
	if requestLog.createRequests[0].IngressProtocol != requestlog.ProtocolOpenAI {
		t.Fatalf("expected ingress protocol openai, got %q", requestLog.createRequests[0].IngressProtocol)
	}

	// 请求翻译送达 adapter：上游模型名 + instructions → system 首条。
	if chatAdapter.req.Model != "deepseek-v4-flash" {
		t.Fatalf("expected upstream model deepseek-v4-flash, got %q", chatAdapter.req.Model)
	}
	if len(chatAdapter.req.Messages) != 2 || chatAdapter.req.Messages[0].Role != "system" {
		t.Fatalf("expected system + user messages, got %+v", chatAdapter.req.Messages)
	}

	// settlement 落 responses 协议事实。
	if len(settlement.params) != 1 {
		t.Fatalf("expected 1 settlement, got %d", len(settlement.params))
	}
	if settlement.params[0].ResponseProtocol != requestlog.ProtocolOpenAI {
		t.Fatalf("expected settlement protocol openai, got %q", settlement.params[0].ResponseProtocol)
	}
	if settlement.params[0].ResponseModelID != "unio-deepseek" {
		t.Fatalf("expected settlement response model unio-deepseek, got %q", settlement.params[0].ResponseModelID)
	}

	// 响应翻译回 Responses 形状。
	if resp.Object != "response" || resp.Model != "unio-deepseek" || resp.Status != "completed" {
		t.Fatalf("unexpected response envelope: %+v", resp)
	}
	if len(resp.Output) != 1 || resp.Output[0].Type != "message" ||
		len(resp.Output[0].Content) != 1 || resp.Output[0].Content[0].Text != "hi there" {
		t.Fatalf("unexpected output: %+v", resp.Output)
	}
	if resp.Usage == nil || resp.Usage.InputTokens != 12 || resp.Usage.OutputTokens != 8 {
		t.Fatalf("unexpected usage: %+v", resp.Usage)
	}
}

func TestCreateResponse_AdapterNotRegistered(t *testing.T) {
	registry := &fakeRegistry{
		adapters:   map[string]chatcompletionsadapter.ChatAdapter{},
		tokenizers: map[string]chatcompletionsadapter.ChatInputTokenizer{"deepseek": fakeTokenizer{}},
	}
	router := &fakeRouter{plan: routing.ChatRoutePlan{Candidates: []routing.ChatRouteCandidate{candidate("deepseek", 1, "deepseek-v4-flash")}}}
	authorizer := &fakeAuthorizer{}
	requestLog := newFakeRequestLog()

	svc := newServiceForTest(router, registry, &fakeSettlement{}, authorizer, requestLog)

	_, err := svc.CreateResponse(ctxWithPrincipal(), instructionsRequest())
	if err == nil {
		t.Fatal("expected error for unregistered adapter")
	}
	if len(requestLog.markAttemptFailed) != 1 || requestLog.markAttemptFailed[0].ErrorCode != string(failure.CodeGatewayAdapterNotRegistered) {
		t.Fatalf("expected attempt failure adapter_not_registered, got %+v", requestLog.markAttemptFailed)
	}
	if len(requestLog.markFailed) != 1 || requestLog.markFailed[0].ErrorCode != string(failure.CodeGatewayAdapterNotRegistered) {
		t.Fatalf("expected request failure adapter_not_registered, got %+v", requestLog.markFailed)
	}
	// adapter 不可用是 fatal 配置错误：必须释放已冻结余额。
	if authorizer.releaseCount != 1 {
		t.Fatalf("expected 1 authorization release, got %d", authorizer.releaseCount)
	}
}

func TestCreateResponse_AuthorizationFailed(t *testing.T) {
	chatAdapter := &fakeChatAdapter{resp: okChatResponse()}
	registry := &fakeRegistry{
		adapters:   map[string]chatcompletionsadapter.ChatAdapter{"deepseek": chatAdapter},
		tokenizers: map[string]chatcompletionsadapter.ChatInputTokenizer{"deepseek": fakeTokenizer{}},
	}
	router := &fakeRouter{plan: routing.ChatRoutePlan{Candidates: []routing.ChatRouteCandidate{candidate("deepseek", 1, "deepseek-v4-flash")}}}
	// 用无 failure.Code 的裸错误，触发 lifecycle 兜底 code（FailureCodeOrFallback）。
	authorizer := &fakeAuthorizer{authorizeErr: errors.New("authorize boom")}
	requestLog := newFakeRequestLog()

	svc := newServiceForTest(router, registry, &fakeSettlement{}, authorizer, requestLog)

	_, err := svc.CreateResponse(ctxWithPrincipal(), instructionsRequest())
	if err == nil {
		t.Fatal("expected authorization error")
	}
	if len(requestLog.markFailed) != 1 || requestLog.markFailed[0].ErrorCode != "chat_authorization_failed" {
		t.Fatalf("expected chat_authorization_failed, got %+v", requestLog.markFailed)
	}
}
