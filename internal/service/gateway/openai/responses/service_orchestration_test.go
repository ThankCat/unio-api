package responses

import (
	"context"
	"errors"
	"testing"
	"time"

	gatewayapi "github.com/ThankCat/unio-api/internal/app/gatewayapi/openai/responses"
	"github.com/ThankCat/unio-api/internal/core/adapter"
	"github.com/ThankCat/unio-api/internal/core/adapter/openai"
	"github.com/ThankCat/unio-api/internal/core/auth"
	"github.com/ThankCat/unio-api/internal/core/channel"
	"github.com/ThankCat/unio-api/internal/core/requestlog"
	"github.com/ThankCat/unio-api/internal/core/routing"
	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/httpx"
	"github.com/ThankCat/unio-api/internal/service/gateway/lifecycle"
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

func (fakeTokenizer) CountChatInputTokens(_ openai.ChatRequest) (int64, error) { return 16, nil }

type fakeRegistry struct {
	adapters       map[string]openai.ChatAdapter
	streamAdapters map[string]openai.StreamChatAdapter
	tokenizers     map[string]openai.ChatInputTokenizer
}

func (r *fakeRegistry) Chat(key string) (openai.ChatAdapter, bool) {
	a, ok := r.adapters[key]
	return a, ok
}

func (r *fakeRegistry) StreamChat(key string) (openai.StreamChatAdapter, bool) {
	a, ok := r.streamAdapters[key]
	return a, ok
}

func (r *fakeRegistry) ChatInputTokenizer(key string) (openai.ChatInputTokenizer, bool) {
	t, ok := r.tokenizers[key]
	return t, ok
}

type fakeChatAdapter struct {
	req  openai.ChatRequest
	resp *openai.ChatResponse
	err  error
}

func (a *fakeChatAdapter) ChatCompletions(_ context.Context, _ channel.Runtime, req openai.ChatRequest) (*openai.ChatResponse, error) {
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
	authorizeErr error
	releaseCount int
}

func (a *fakeAuthorizer) AuthorizeChat(_ context.Context, params lifecycle.ChatAuthorizeParams) (lifecycle.ChatAuthorization, error) {
	if a.authorizeErr != nil {
		return lifecycle.ChatAuthorization{}, a.authorizeErr
	}
	return lifecycle.ChatAuthorization{
		RequestRecordID: params.RequestRecord.ID,
		ReservationID:   42,
		Currency:        "USD",
		PriceID:         1,
	}, nil
}

func (a *fakeAuthorizer) ReleaseChat(_ context.Context, _ lifecycle.ChatReleaseAuthorizationParams) error {
	a.releaseCount++
	return nil
}

func (a *fakeAuthorizer) ReleaseChatForBillingException(_ context.Context, _ lifecycle.ChatReleaseBillingExceptionParams) error {
	return nil
}

type fakeRequestLog struct {
	createRequests    []requestlog.CreateRequestParams
	markFailed        []requestlog.MarkRequestFailedParams
	markAttemptFailed []requestlog.MarkAttemptFailedParams
	nextRequestID     int64
	nextAttemptID     int64
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
		ProjectID:       params.ProjectID,
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

func (s *fakeRequestLog) MarkAttemptFailed(_ context.Context, params requestlog.MarkAttemptFailedParams) (requestlog.AttemptRecord, error) {
	s.markAttemptFailed = append(s.markAttemptFailed, params)
	return requestlog.AttemptRecord{ID: params.ID, Status: requestlog.AttemptStatusFailed}, nil
}

func (s *fakeRequestLog) MarkAttemptCanceled(_ context.Context, params requestlog.MarkAttemptCanceledParams) (requestlog.AttemptRecord, error) {
	return requestlog.AttemptRecord{ID: params.ID, Status: requestlog.AttemptStatusCanceled}, nil
}

type passthroughPreparer struct{}

func (passthroughPreparer) PrepareCandidates(_ context.Context, params lifecycle.PrepareCandidatesParams) (lifecycle.CandidatePlan, error) {
	plan := lifecycle.CandidatePlan{ConservativeInputTokens: 16}
	for index, candidate := range params.Candidates {
		plan.Candidates = append(plan.Candidates, lifecycle.Candidate{RouteIndex: index, Route: candidate})
	}
	return plan, nil
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

func okChatResponse() *openai.ChatResponse {
	usage := adapter.ChatUsage{PromptTokens: 12, CompletionTokens: 8, TotalTokens: 20}
	meta := adapter.UpstreamMetadata{StatusCode: 200, RequestID: "req-1"}
	return &openai.ChatResponse{
		ID:           "chatcmpl_upstream",
		Model:        "deepseek-chat",
		Content:      "hi there",
		FinishReason: "stop",
		Usage:        usage,
		Created:      1700000123,
		Upstream:     meta,
		Facts: adapter.ResponseFacts{
			UpstreamProtocol:    "openai",
			UpstreamResponseID:  "chatcmpl_upstream",
			UpstreamModel:       "deepseek-chat",
			Finish:              adapter.FinishFacts{Class: adapter.FinishStop, RawReason: "stop"},
			UsageMappingVersion: "openai.v1",
			Metadata:            meta,
		},
	}
}

func ctxWithPrincipal() context.Context {
	ctx := httpx.ContextWithRequestID(context.Background(), "responses-test-request-id")
	return auth.ContextWithAPIKeyPrincipal(ctx, &auth.APIKeyPrincipal{
		APIKeyID: 1, UserID: 7, ProjectID: 3, KeyPrefix: "unio_sk_test",
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

func TestCreateResponse_HappyPath(t *testing.T) {
	chatAdapter := &fakeChatAdapter{resp: okChatResponse()}
	registry := &fakeRegistry{
		adapters:   map[string]openai.ChatAdapter{"deepseek": chatAdapter},
		tokenizers: map[string]openai.ChatInputTokenizer{"deepseek": fakeTokenizer{}},
	}
	router := &fakeRouter{plan: routing.ChatRoutePlan{Candidates: []routing.ChatRouteCandidate{candidate("deepseek", 1, "deepseek-chat")}}}
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
	if chatAdapter.req.Model != "deepseek-chat" {
		t.Fatalf("expected upstream model deepseek-chat, got %q", chatAdapter.req.Model)
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
		adapters:   map[string]openai.ChatAdapter{},
		tokenizers: map[string]openai.ChatInputTokenizer{"deepseek": fakeTokenizer{}},
	}
	router := &fakeRouter{plan: routing.ChatRoutePlan{Candidates: []routing.ChatRouteCandidate{candidate("deepseek", 1, "deepseek-chat")}}}
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
		adapters:   map[string]openai.ChatAdapter{"deepseek": chatAdapter},
		tokenizers: map[string]openai.ChatInputTokenizer{"deepseek": fakeTokenizer{}},
	}
	router := &fakeRouter{plan: routing.ChatRoutePlan{Candidates: []routing.ChatRouteCandidate{candidate("deepseek", 1, "deepseek-chat")}}}
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
