package chatcompletions

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	gatewayapi "github.com/ThankCat/unio-api/internal/app/gatewayapi/openai/chatcompletions"
	"github.com/ThankCat/unio-api/internal/core/adapter"
	"github.com/ThankCat/unio-api/internal/core/adapter/openai"
	"github.com/ThankCat/unio-api/internal/core/auth"
	"github.com/ThankCat/unio-api/internal/core/channel"
	"github.com/ThankCat/unio-api/internal/core/requestlog"
	"github.com/ThankCat/unio-api/internal/core/routing"
	coreusage "github.com/ThankCat/unio-api/internal/core/usage"
	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/httpx"
	"github.com/ThankCat/unio-api/internal/service/gateway/lifecycle"
)

// fakeChatRouter 是 gateway 测试使用的 routing 替身。
type fakeChatRouter struct {
	called bool
	req    routing.ChatRouteRequest
	plan   routing.ChatRoutePlan
	err    error
}

// PlanChat 记录 gateway 传入的 routing 请求，并返回测试预设计划。
func (r *fakeChatRouter) PlanChat(ctx context.Context, req routing.ChatRouteRequest) (routing.ChatRoutePlan, error) {
	r.called = true
	r.req = req
	return r.plan, r.err
}

// fakeAdapterRegistry 是 gateway 测试使用的 adapter registry 替身。
type fakeAdapterRegistry struct {
	chatKeys               []string
	streamChatKeys         []string
	chatInputTokenizerKeys []string
	chatAdapters           map[string]openai.ChatAdapter
	streamChatAdapters     map[string]openai.StreamChatAdapter
	chatInputTokenizers    map[string]openai.ChatInputTokenizer
}

// Chat 记录 adapter key，并按 key 返回测试预设非流式 adapter。
func (r *fakeAdapterRegistry) Chat(adapterKey string) (openai.ChatAdapter, bool) {
	r.chatKeys = append(r.chatKeys, adapterKey)
	chatAdapter, ok := r.chatAdapters[adapterKey]
	return chatAdapter, ok
}

// StreamChat 记录 adapter key，并按 key 返回测试预设流式 adapter。
func (r *fakeAdapterRegistry) StreamChat(adapterKey string) (openai.StreamChatAdapter, bool) {
	r.streamChatKeys = append(r.streamChatKeys, adapterKey)
	streamChatAdapter, ok := r.streamChatAdapters[adapterKey]
	return streamChatAdapter, ok
}

// ChatInputTokenizer 记录 adapter key，并按 key 返回测试预设输入 tokenizer。
func (r *fakeAdapterRegistry) ChatInputTokenizer(adapterKey string) (openai.ChatInputTokenizer, bool) {
	r.chatInputTokenizerKeys = append(r.chatInputTokenizerKeys, adapterKey)
	tokenizer, ok := r.chatInputTokenizers[adapterKey]
	return tokenizer, ok
}

// fakeRetryClassifier 是 gateway 测试使用的 retry 判断替身。
type fakeRetryClassifier struct {
	retryable bool
	called    int
}

// IsRetryable 记录调用次数，并返回测试预设的 retry 判断结果。
func (c *fakeRetryClassifier) IsRetryable(err error) bool {
	c.called++
	return c.retryable
}

// fakeRequestLogService 是 gateway 测试使用的 requestlog 替身。
type fakeRequestLogService struct {
	nextRequestID int64
	nextAttemptID int64

	createRequests           []requestlog.CreateRequestParams
	markRequestRunningIDs    []int64
	markRequestSucceededArgs []requestlog.MarkRequestSucceededParams
	markRequestFailedArgs    []requestlog.MarkRequestFailedParams
	markRequestCanceledArgs  []requestlog.MarkRequestCanceledParams

	createAttempts           []requestlog.CreateAttemptParams
	markAttemptSucceededArgs []requestlog.MarkAttemptSucceededParams
	markAttemptFailedArgs    []requestlog.MarkAttemptFailedParams
	markAttemptCanceledArgs  []requestlog.MarkAttemptCanceledParams
	createAttemptErr         error
}

// fakeChatSettlementExecutor 是 gateway 测试使用的 chat settlement 替身。
type fakeChatSettlementExecutor struct {
	params []lifecycle.ChatSettlementParams
	err    error
}

// fakeChatAuthorizer 是 gateway 测试使用的 chat authorization 替身。
type fakeChatAuthorizer struct {
	authorizeParams               []lifecycle.ChatAuthorizeParams
	releaseParams                 []lifecycle.ChatReleaseAuthorizationParams
	releaseBillingExceptionParams []lifecycle.ChatReleaseBillingExceptionParams
	authorization                 lifecycle.ChatAuthorization
	authorizeErr                  error
	releaseErr                    error
	releaseBillingExceptionErr    error
}

// passthroughCandidatePreparer 是通用 service 测试使用的候选计划替身。
//
// 共享 executor 的 capability、熔断与保守估算行为由 lifecycle 包单测覆盖；
// service 测试默认保留 routing 顺序并提供固定估算，聚焦协议编排行为。
type passthroughCandidatePreparer struct {
	inputTokens int64
}

func (p passthroughCandidatePreparer) PrepareCandidates(_ context.Context, params lifecycle.PrepareCandidatesParams) (lifecycle.CandidatePlan, error) {
	plan := lifecycle.CandidatePlan{
		Candidates:              make([]lifecycle.Candidate, 0, len(params.Candidates)),
		ConservativeInputTokens: p.inputTokens,
	}
	for index, candidate := range params.Candidates {
		plan.Candidates = append(plan.Candidates, lifecycle.Candidate{
			RouteIndex: index,
			Route:      candidate,
		})
	}
	return plan, nil
}

// newFakeRequestLogService 创建测试用 requestlog.Service。
func newFakeRequestLogService() *fakeRequestLogService {
	return &fakeRequestLogService{
		nextRequestID: 1,
		nextAttemptID: 1,
	}
}

// SettleSuccessfulChat 记录结算参数，并返回测试预设错误。
func (s *fakeChatSettlementExecutor) SettleSuccessfulChat(ctx context.Context, params lifecycle.ChatSettlementParams) error {
	s.params = append(s.params, params)
	return s.err
}

// AuthorizeChat 记录冻结余额参数，并返回测试预设授权。
func (a *fakeChatAuthorizer) AuthorizeChat(ctx context.Context, params lifecycle.ChatAuthorizeParams) (lifecycle.ChatAuthorization, error) {
	a.authorizeParams = append(a.authorizeParams, params)
	if a.authorizeErr != nil {
		return lifecycle.ChatAuthorization{}, a.authorizeErr
	}

	authorization := a.authorization
	if authorization.ReservationID == 0 {
		authorization.ReservationID = 7000 + int64(len(a.authorizeParams))
	}
	authorization.RequestRecordID = params.RequestRecord.ID
	if authorization.Currency == "" {
		authorization.Currency = "USD"
	}

	return authorization, nil
}

// ReleaseChat 记录释放冻结余额参数，并返回测试预设错误。
func (a *fakeChatAuthorizer) ReleaseChat(ctx context.Context, params lifecycle.ChatReleaseAuthorizationParams) error {
	a.releaseParams = append(a.releaseParams, params)
	return a.releaseErr
}

// ReleaseChatForBillingException 记录异常释放冻结余额参数，并返回测试预设错误。
func (a *fakeChatAuthorizer) ReleaseChatForBillingException(ctx context.Context, params lifecycle.ChatReleaseBillingExceptionParams) error {
	a.releaseBillingExceptionParams = append(a.releaseBillingExceptionParams, params)
	return a.releaseBillingExceptionErr
}

// CreateRequest 记录创建 request record 的参数并返回 pending 记录。
func (s *fakeRequestLogService) CreateRequest(ctx context.Context, params requestlog.CreateRequestParams) (requestlog.RequestRecord, error) {
	s.createRequests = append(s.createRequests, params)

	id := s.nextRequestID
	s.nextRequestID++

	return requestlog.RequestRecord{
		ID:               id,
		RequestID:        params.RequestID,
		UserID:           params.UserID,
		ProjectID:        params.ProjectID,
		APIKeyID:         params.APIKeyID,
		RequestedModelID: params.RequestedModelID,
		Stream:           params.Stream,
		Status:           requestlog.RequestStatusPending,
		StartedAt:        params.StartedAt,
	}, nil
}

// MarkRequestRunning 记录 request running 状态变更。
func (s *fakeRequestLogService) MarkRequestRunning(ctx context.Context, id int64) (requestlog.RequestRecord, error) {
	s.markRequestRunningIDs = append(s.markRequestRunningIDs, id)

	return requestlog.RequestRecord{
		ID:     id,
		Status: requestlog.RequestStatusRunning,
	}, nil
}

// MarkRequestSucceeded 记录 request succeeded 状态变更。
func (s *fakeRequestLogService) MarkRequestSucceeded(ctx context.Context, params requestlog.MarkRequestSucceededParams) (requestlog.RequestRecord, error) {
	s.markRequestSucceededArgs = append(s.markRequestSucceededArgs, params)

	return requestlog.RequestRecord{
		ID:     params.ID,
		Status: requestlog.RequestStatusSucceeded,
	}, nil
}

// MarkRequestFailed 记录 request failed 状态变更。
func (s *fakeRequestLogService) MarkRequestFailed(ctx context.Context, params requestlog.MarkRequestFailedParams) (requestlog.RequestRecord, error) {
	s.markRequestFailedArgs = append(s.markRequestFailedArgs, params)

	return requestlog.RequestRecord{
		ID:     params.ID,
		Status: requestlog.RequestStatusFailed,
	}, nil
}

// MarkRequestCanceled 记录 request canceled 状态变更。
func (s *fakeRequestLogService) MarkRequestCanceled(ctx context.Context, params requestlog.MarkRequestCanceledParams) (requestlog.RequestRecord, error) {
	s.markRequestCanceledArgs = append(s.markRequestCanceledArgs, params)

	return requestlog.RequestRecord{
		ID:     params.ID,
		Status: requestlog.RequestStatusCanceled,
	}, nil
}

// CreateAttempt 记录创建 request attempt 的参数并返回 running 记录。
func (s *fakeRequestLogService) CreateAttempt(ctx context.Context, params requestlog.CreateAttemptParams) (requestlog.AttemptRecord, error) {
	s.createAttempts = append(s.createAttempts, params)
	if s.createAttemptErr != nil {
		return requestlog.AttemptRecord{}, s.createAttemptErr
	}

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

// MarkAttemptSucceeded 记录 attempt succeeded 状态变更。
func (s *fakeRequestLogService) MarkAttemptSucceeded(ctx context.Context, params requestlog.MarkAttemptSucceededParams) (requestlog.AttemptRecord, error) {
	s.markAttemptSucceededArgs = append(s.markAttemptSucceededArgs, params)

	return requestlog.AttemptRecord{
		ID:     params.ID,
		Status: requestlog.AttemptStatusSucceeded,
	}, nil
}

// MarkAttemptFailed 记录 attempt failed 状态变更。
func (s *fakeRequestLogService) MarkAttemptFailed(ctx context.Context, params requestlog.MarkAttemptFailedParams) (requestlog.AttemptRecord, error) {
	s.markAttemptFailedArgs = append(s.markAttemptFailedArgs, params)

	return requestlog.AttemptRecord{
		ID:     params.ID,
		Status: requestlog.AttemptStatusFailed,
	}, nil
}

// MarkAttemptCanceled 记录 attempt canceled 状态变更。
func (s *fakeRequestLogService) MarkAttemptCanceled(ctx context.Context, params requestlog.MarkAttemptCanceledParams) (requestlog.AttemptRecord, error) {
	s.markAttemptCanceledArgs = append(s.markAttemptCanceledArgs, params)

	return requestlog.AttemptRecord{
		ID:     params.ID,
		Status: requestlog.AttemptStatusCanceled,
	}, nil
}

// fakeChatAdapter 是 gateway 测试使用的 adapter 替身。
type fakeChatAdapter struct {
	chatCalled   int
	chatReq      openai.ChatRequest
	chatResp     *openai.ChatResponse
	chatErr      error
	streamCalled  int
	streamReq     openai.ChatRequest
	streamResp    []openai.ChatStreamChunk
	streamOutcome *adapter.StreamOutcome
	streamErr     error
	ch            channel.Runtime
}

// ChatCompletions 记录 gateway 传入的请求，并返回测试预设响应。
func (a *fakeChatAdapter) ChatCompletions(ctx context.Context, ch channel.Runtime, req openai.ChatRequest) (*openai.ChatResponse, error) {
	a.chatCalled++
	a.chatReq = req
	a.ch = ch

	return a.chatResp, a.chatErr
}

// StreamChatCompletions 记录 gateway 传入的流式请求，并逐个发出测试预设 chunk。
func (a *fakeChatAdapter) StreamChatCompletions(ctx context.Context, ch channel.Runtime, req openai.ChatRequest, emit func(openai.ChatStreamChunk) error) (adapter.StreamOutcome, error) {
	a.streamCalled++
	a.streamReq = req
	a.ch = ch

	if a.streamErr != nil && len(a.streamResp) == 0 {
		return adapter.StreamOutcome{}, a.streamErr
	}

	for _, chunk := range a.streamResp {
		if err := emit(chunk); err != nil {
			return adapter.StreamOutcome{}, err
		}
	}

	if a.streamOutcome != nil {
		return *a.streamOutcome, a.streamErr
	}

	// 模拟真实 adapter：即使发生 tail error，只要已收到 final usage chunk 仍产出协议无关
	// ResponseFacts（10.10A），供 lifecycle 按可靠 usage 记账；无 final usage 时返回空 outcome。
	return syntheticStreamOutcome(a.streamResp), a.streamErr
}

// syntheticStreamOutcome 从测试 stream chunk 的 final usage 合成 StreamOutcome.Facts，
// 模拟真实 OpenAI adapter 在流式终态生成的不可变账务事实；无 final usage 时返回空 outcome。
func syntheticStreamOutcome(chunks []openai.ChatStreamChunk) adapter.StreamOutcome {
	for i := len(chunks) - 1; i >= 0; i-- {
		if chunks[i].Usage == nil {
			continue
		}

		chunk := chunks[i]
		facts := adapter.ResponseFacts{
			UpstreamProtocol:    "openai",
			UpstreamResponseID:  chunk.ID,
			UpstreamModel:       chunk.Model,
			Finish:              adapter.FinishFacts{Class: adapter.FinishStop, RawReason: "stop"},
			Usage:               chunk.Usage.ToUsageFacts(),
			UsageSource:         coreusage.SourceUpstreamStream,
			UsageMappingVersion: "openai.v1",
		}
		if chunk.Upstream != nil {
			facts.Metadata = *chunk.Upstream
		}
		return adapter.StreamOutcome{Facts: &facts}
	}

	return adapter.StreamOutcome{}
}

// contextWithPrincipal 创建带 API key principal 的测试 context。
func contextWithPrincipal(projectID int64) context.Context {
	ctx := httpx.ContextWithRequestID(context.Background(), "gateway-test-request-id")

	return auth.ContextWithAPIKeyPrincipal(ctx, &auth.APIKeyPrincipal{
		APIKeyID:  1,
		UserID:    7,
		ProjectID: projectID,
		KeyPrefix: "unio_sk_test",
	})
}

// chatRequest 创建测试用 HTTP chat completion 请求。
func chatRequest() gatewayapi.ChatCompletionRequest {
	return gatewayapi.ChatCompletionRequest{
		Model:    "openai/gpt-4.1",
		Messages: []gatewayapi.ChatMessage{{Role: "user", Content: jsonContent("hello")}},
	}
}

// chatRequestWithParams 创建带 OpenAI-compatible 可透传参数的测试请求。
func chatRequestWithParams() gatewayapi.ChatCompletionRequest {
	temperature := 0.0
	topP := 0.8
	maxTokens := 128
	presencePenalty := 0.5
	frequencyPenalty := 0.25
	user := "end-user-1"

	req := chatRequest()
	req.Temperature = &temperature
	req.TopP = &topP
	req.MaxTokens = &maxTokens
	req.PresencePenalty = &presencePenalty
	req.FrequencyPenalty = &frequencyPenalty
	req.Stop = []string{"END", "STOP"}
	req.User = &user

	return req
}

// assertAdapterChatRequestParams 断言 gateway 没有丢弃 HTTP DTO 中的可透传参数。
func assertAdapterChatRequestParams(t *testing.T, req openai.ChatRequest) {
	t.Helper()

	if req.Temperature == nil || *req.Temperature != 0 {
		t.Fatalf("expected temperature 0, got %v", req.Temperature)
	}
	if req.TopP == nil || *req.TopP != 0.8 {
		t.Fatalf("expected top_p 0.8, got %v", req.TopP)
	}
	if req.MaxTokens == nil || *req.MaxTokens != 128 {
		t.Fatalf("expected max_tokens 128, got %v", req.MaxTokens)
	}
	if req.PresencePenalty == nil || *req.PresencePenalty != 0.5 {
		t.Fatalf("expected presence_penalty 0.5, got %v", req.PresencePenalty)
	}
	if req.FrequencyPenalty == nil || *req.FrequencyPenalty != 0.25 {
		t.Fatalf("expected frequency_penalty 0.25, got %v", req.FrequencyPenalty)
	}
	if len(req.Stop) != 2 || req.Stop[0] != "END" || req.Stop[1] != "STOP" {
		t.Fatalf("expected stop [END STOP], got %#v", req.Stop)
	}
	if req.User == nil || *req.User != "end-user-1" {
		t.Fatalf("expected user end-user-1, got %v", req.User)
	}
}

// routePlan 创建测试用同模型 route plan。
func routePlan(candidates ...routing.ChatRouteCandidate) routing.ChatRoutePlan {
	return routing.ChatRoutePlan{
		RequestedModel: "openai/gpt-4.1",
		Candidates:     candidates,
	}
}

// routeCandidate 创建测试用 route candidate。
func routeCandidate(adapterKey string, channelID int64, upstreamModel string) routing.ChatRouteCandidate {
	return routing.ChatRouteCandidate{
		ModelDBID:  1000 + channelID,
		ProviderID: 9000 + channelID,
		AdapterKey: adapterKey,
		Channel: channel.Runtime{
			ID:      channelID,
			BaseURL: "https://example.test/v1",
			APIKey:  "test-secret",
			Timeout: 30 * time.Second,
		},
		UpstreamModel: upstreamModel,
	}
}

// chatResponse 创建测试用 adapter 响应。
func chatResponse(content string) *openai.ChatResponse {
	usage := adapter.ChatUsage{
		PromptTokens:     10,
		CompletionTokens: 11,
		TotalTokens:      21,
	}
	metadata := adapter.UpstreamMetadata{
		StatusCode: 200,
		RequestID:  "req-nonstream-1",
	}
	return &openai.ChatResponse{
		ID:       "chatcmpl_provider_test",
		Model:    "gpt-4.1",
		Content:  content,
		Usage:    usage,
		Upstream: metadata,
		Facts: adapter.ResponseFacts{
			UpstreamProtocol:    "openai",
			UpstreamResponseID:  "chatcmpl_provider_test",
			UpstreamModel:       "gpt-4.1",
			Finish:              adapter.FinishFacts{Class: adapter.FinishStop, RawReason: "stop"},
			Usage:               usage.ToUsageFacts(),
			UsageSource:         coreusage.SourceUpstreamResponse,
			UsageMappingVersion: "openai.v1",
			Metadata:            metadata,
		},
	}
}

// streamUsageChunk 创建测试用 stream final usage chunk。
func streamUsageChunk(model string) openai.ChatStreamChunk {
	return openai.ChatStreamChunk{
		ID:    "chatcmpl_mock",
		Model: model,
		Usage: &adapter.ChatUsage{
			PromptTokens:     10,
			CompletionTokens: 11,
			TotalTokens:      21,
			CachedTokens:     3,
			ReasoningTokens:  2,
		},
		Upstream: &adapter.UpstreamMetadata{
			StatusCode: 200,
			RequestID:  "req-stream-1",
		},
	}
}

// newChatCompletionSettlementForTest 创建 chat completion 测试用结算替身。
func newChatCompletionSettlementForTest() *fakeChatSettlementExecutor {
	return &fakeChatSettlementExecutor{}
}

// newChatCompletionAuthorizerForTest 创建 chat completion 测试用授权替身。
func newChatCompletionAuthorizerForTest() *fakeChatAuthorizer {
	return &fakeChatAuthorizer{}
}

// newChatCompletionServiceForTest 创建带默认授权替身的 gateway service。
func newChatCompletionServiceForTest(router ChatRouter, registry AdapterRegistry, retryClassifier lifecycle.RetryClassifier, requestLog requestlog.Service, settlement lifecycle.ChatSettlementExecutor) *ChatCompletionService {
	return newChatCompletionServiceForTestWithAuthorizer(router, registry, retryClassifier, requestLog, settlement, newChatCompletionAuthorizerForTest())
}

// newChatCompletionServiceForTestWithAuthorizer 创建可注入授权替身的 gateway service。
func newChatCompletionServiceForTestWithAuthorizer(router ChatRouter, registry AdapterRegistry, retryClassifier lifecycle.RetryClassifier, requestLog requestlog.Service, settlement lifecycle.ChatSettlementExecutor, authorizer lifecycle.ChatAuthorizer) *ChatCompletionService {
	return NewChatCompletionService(
		router,
		registry,
		passthroughCandidatePreparer{inputTokens: 1},
		retryClassifier,
		requestLog,
		settlement,
		authorizer,
		nil,
		nil,
	)
}

func TestChatCompletionServiceCreateChatCompletionRoutesAndCallsAdapter(t *testing.T) {
	fakeAdapter := &fakeChatAdapter{
		chatResp: chatResponse("adapter response"),
	}
	router := &fakeChatRouter{
		plan: routePlan(routeCandidate("openai", 123, "gpt-4.1")),
	}
	registry := &fakeAdapterRegistry{
		chatAdapters: map[string]openai.ChatAdapter{
			"openai": fakeAdapter,
		},
	}
	requestLog := newFakeRequestLogService()
	settlement := newChatCompletionSettlementForTest()
	authorizer := &fakeChatAuthorizer{
		authorization: lifecycle.ChatAuthorization{ReservationID: 7788},
	}
	service := newChatCompletionServiceForTestWithAuthorizer(router, registry, nil, requestLog, settlement, authorizer)

	got, err := service.CreateChatCompletion(contextWithPrincipal(42), chatRequestWithParams())
	if err != nil {
		t.Fatalf("CreateChatCompletion returned err: %v", err)
	}

	if !router.called {
		t.Fatal("expected routing to be called")
	}
	if router.req.ProjectID != 42 {
		t.Fatalf("expected project id %d, got %d", int64(42), router.req.ProjectID)
	}
	if router.req.ModelID != "openai/gpt-4.1" {
		t.Fatalf("expected requested model %q, got %q", "openai/gpt-4.1", router.req.ModelID)
	}
	if len(registry.chatKeys) != 1 || registry.chatKeys[0] != "openai" {
		t.Fatalf("expected adapter key %q, got %#v", "openai", registry.chatKeys)
	}
	if fakeAdapter.chatCalled != 1 {
		t.Fatalf("expected adapter to be called once, got %d", fakeAdapter.chatCalled)
	}
	if fakeAdapter.chatReq.Model != "gpt-4.1" {
		t.Fatalf("expected upstream model %q, got %q", "gpt-4.1", fakeAdapter.chatReq.Model)
	}
	if fakeAdapter.ch.ID != 123 {
		t.Fatalf("expected channel id %d, got %d", int64(123), fakeAdapter.ch.ID)
	}
	if fakeAdapter.chatReq.Messages[0].ContentString() != "hello" {
		t.Fatalf("expected message content %q, got %q", "hello", fakeAdapter.chatReq.Messages[0].Content)
	}
	assertAdapterChatRequestParams(t, fakeAdapter.chatReq)
	if got.Model != "openai/gpt-4.1" {
		t.Fatalf("expected response model %q, got %q", "openai/gpt-4.1", got.Model)
	}
	if got.Choices[0].Message.ContentString() != "adapter response" {
		t.Fatalf("expected content %q, got %q", "adapter response", got.Choices[0].Message.Content)
	}
	if got.Usage.TotalTokens != 21 {
		t.Fatalf("expected total_tokens %d, got %d", 21, got.Usage.TotalTokens)
	}
	if len(requestLog.createRequests) != 1 {
		t.Fatalf("expected one request record, got %d", len(requestLog.createRequests))
	}
	if requestLog.createRequests[0].RequestID == "gateway-test-request-id" {
		t.Fatal("expected server-generated request record id, got correlation id from context")
	}
	if !strings.HasPrefix(requestLog.createRequests[0].RequestID, "req_") {
		t.Fatalf("expected server-generated request id prefix %q, got %q", "req_", requestLog.createRequests[0].RequestID)
	}
	if requestLog.createRequests[0].UserID != 7 {
		t.Fatalf("expected user id %d, got %d", int64(7), requestLog.createRequests[0].UserID)
	}
	if requestLog.createRequests[0].ProjectID != 42 {
		t.Fatalf("expected request project id %d, got %d", int64(42), requestLog.createRequests[0].ProjectID)
	}
	if requestLog.createRequests[0].RequestedModelID != "openai/gpt-4.1" {
		t.Fatalf("expected requested model %q, got %q", "openai/gpt-4.1", requestLog.createRequests[0].RequestedModelID)
	}
	if len(requestLog.markRequestRunningIDs) != 1 || requestLog.markRequestRunningIDs[0] != 1 {
		t.Fatalf("expected request to be marked running once, got %#v", requestLog.markRequestRunningIDs)
	}
	if len(requestLog.createAttempts) != 1 {
		t.Fatalf("expected one request attempt, got %d", len(requestLog.createAttempts))
	}
	if requestLog.createAttempts[0].ProviderID != 9123 {
		t.Fatalf("expected provider id %d, got %d", int64(9123), requestLog.createAttempts[0].ProviderID)
	}
	if requestLog.createAttempts[0].ChannelID != 123 {
		t.Fatalf("expected attempt channel id %d, got %d", int64(123), requestLog.createAttempts[0].ChannelID)
	}
	if len(settlement.params) != 1 {
		t.Fatalf("expected one settlement call, got %d", len(settlement.params))
	}
	settlementParams := settlement.params[0]
	if settlementParams.RequestRecord.ID != 1 {
		t.Fatalf("expected settlement request record id 1, got %d", settlementParams.RequestRecord.ID)
	}
	if settlementParams.AttemptRecord.ID != 1 {
		t.Fatalf("expected settlement attempt id 1, got %d", settlementParams.AttemptRecord.ID)
	}
	if settlementParams.ResponseModelID != "openai/gpt-4.1" {
		t.Fatalf("expected response model %q, got %q", "openai/gpt-4.1", settlementParams.ResponseModelID)
	}
	if settlementParams.ModelDBID != 1123 {
		t.Fatalf("expected model db id %d, got %d", int64(1123), settlementParams.ModelDBID)
	}
	if settlementParams.FinalProviderID != 9123 {
		t.Fatalf("expected final provider id %d, got %d", int64(9123), settlementParams.FinalProviderID)
	}
	if settlementParams.FinalChannelID != 123 {
		t.Fatalf("expected final channel id %d, got %d", int64(123), settlementParams.FinalChannelID)
	}
	if settlementParams.Facts.UpstreamModel != "gpt-4.1" {
		t.Fatalf("expected upstream response model %q, got %q", "gpt-4.1", settlementParams.Facts.UpstreamModel)
	}
	if settlementParams.Facts.Metadata.StatusCode != 200 {
		t.Fatalf("expected settlement upstream status 200, got %d", settlementParams.Facts.Metadata.StatusCode)
	}
	if settlementParams.Facts.Metadata.RequestID != "req-nonstream-1" {
		t.Fatalf("expected settlement upstream request id %q, got %q", "req-nonstream-1", settlementParams.Facts.Metadata.RequestID)
	}
	if v, ok := settlementParams.Facts.Usage.OutputTokensTotal.BillableValue(); !ok || v != 11 {
		t.Fatalf("expected settlement output tokens 11, got %d (ok=%v)", v, ok)
	}
	if v, ok := settlementParams.Facts.Usage.UncachedInputTokens.BillableValue(); !ok || v != 10 {
		t.Fatalf("expected settlement uncached input tokens 10, got %d (ok=%v)", v, ok)
	}
	if len(authorizer.authorizeParams) != 1 {
		t.Fatalf("expected one authorization, got %d", len(authorizer.authorizeParams))
	}
	if authorizer.authorizeParams[0].ModelDBID != 1123 {
		t.Fatalf("expected authorization model db id %d, got %d", int64(1123), authorizer.authorizeParams[0].ModelDBID)
	}
	if authorizer.authorizeParams[0].InputTokens != 1 {
		t.Fatalf("expected candidate plan input token estimate %d, got %d", int64(1), authorizer.authorizeParams[0].InputTokens)
	}
	if authorizer.authorizeParams[0].MaxCompletionTokens != 128 {
		t.Fatalf("expected max completion tokens %d, got %d", int64(128), authorizer.authorizeParams[0].MaxCompletionTokens)
	}
	if settlementParams.Authorization.ReservationID != 7788 {
		t.Fatalf("expected settlement authorization reservation id %d, got %d", int64(7788), settlementParams.Authorization.ReservationID)
	}
	if len(authorizer.releaseParams) != 0 {
		t.Fatalf("expected no authorization release on success, got %d", len(authorizer.releaseParams))
	}
}

func TestChatCompletionServiceCreateChatCompletionDoesNotCallAdapterOnRoutingError(t *testing.T) {
	routingErr := errors.New("no route")
	fakeAdapter := &fakeChatAdapter{}
	requestLog := newFakeRequestLogService()
	service := newChatCompletionServiceForTest(
		&fakeChatRouter{err: routingErr},
		&fakeAdapterRegistry{
			chatAdapters: map[string]openai.ChatAdapter{
				"openai": fakeAdapter,
			},
		},
		nil,
		requestLog,
		newChatCompletionSettlementForTest(),
	)

	_, err := service.CreateChatCompletion(contextWithPrincipal(42), chatRequest())
	if !errors.Is(err, routingErr) {
		t.Fatalf("expected routing error, got %v", err)
	}
	if fakeAdapter.chatCalled != 0 {
		t.Fatalf("expected adapter not to be called, got %d calls", fakeAdapter.chatCalled)
	}
	if len(requestLog.createRequests) != 1 {
		t.Fatalf("expected one request record, got %d", len(requestLog.createRequests))
	}
	if len(requestLog.createAttempts) != 0 {
		t.Fatalf("expected no attempts on routing error, got %d", len(requestLog.createAttempts))
	}
	if len(requestLog.markRequestFailedArgs) != 1 {
		t.Fatalf("expected one failed request, got %d", len(requestLog.markRequestFailedArgs))
	}
	if requestLog.markRequestFailedArgs[0].ErrorCode != "routing_error" {
		t.Fatalf("expected routing_error, got %q", requestLog.markRequestFailedArgs[0].ErrorCode)
	}
	if requestLog.markRequestFailedArgs[0].ErrorMessage != "Request routing failed." {
		t.Fatalf("expected safe routing message, got %q", requestLog.markRequestFailedArgs[0].ErrorMessage)
	}
	if requestLog.markRequestFailedArgs[0].InternalErrorDetail != routingErr.Error() {
		t.Fatalf("expected internal error detail %q, got %q", routingErr.Error(), requestLog.markRequestFailedArgs[0].InternalErrorDetail)
	}
}

// TestRequestLogErrorFactsSeparateSafeMessageAndInternalDetail 验证 lifecycle 通过
// service 注入的 chatCompletionsSafeMessage 闭包 + 协议无关 BaseSafeRequestLogErrorMessage
// 兜底 + InternalErrorDetail 截断三段语义，确保 internal 字段保留原始诊断而 ErrorMessage
// 永远是脱敏后的可展示文案。
//
// 原本测试直接对包级 requestLogErrorFacts 断言；TASK-10.05 把实现 hoist 到
// lifecycle.RequestLifecycle.MarkRequestFailed/MarkAttemptFailed/MarkRequestCanceled 共享后，
// 这里改成通过对外 API（MarkRequestFailed）观察 fakeRequestLogService 收到的三元结果。
func TestRequestLogErrorFactsSeparateSafeMessageAndInternalDetail(t *testing.T) {
	rawErr := errors.New("postgres query failed: select * from secret_table")

	requestLog := newFakeRequestLogService()
	lc := lifecycle.NewRequestLifecycle(lifecycle.RequestLifecycleParams{
		RequestLog:      requestLog,
		Authorizer:      &fakeChatAuthorizer{},
		IngressProtocol: requestlog.ProtocolOpenAI,
		Operation:       requestlog.OperationChatCompletions,
		SafeMessage:     chatCompletionsSafeMessage,
	})

	lc.MarkRequestFailed(context.Background(), requestlog.RequestRecord{ID: 9001}, "adapter_error", rawErr)

	if len(requestLog.markRequestFailedArgs) != 1 {
		t.Fatalf("expected MarkRequestFailed to be called once, got %d", len(requestLog.markRequestFailedArgs))
	}
	got := requestLog.markRequestFailedArgs[0]
	if got.ErrorCode != "adapter_error" {
		t.Fatalf("expected fallback code adapter_error, got %q", got.ErrorCode)
	}
	if got.ErrorMessage != "Upstream provider request failed." {
		t.Fatalf("expected safe adapter message, got %q", got.ErrorMessage)
	}
	if got.InternalErrorDetail != rawErr.Error() {
		t.Fatalf("expected raw error in internal detail, got %q", got.InternalErrorDetail)
	}
}

func TestChatCompletionServiceCreateChatCompletionMarksRequestFailedOnSettlementError(t *testing.T) {
	settlementErr := errors.New("settlement failed")
	settlement := &fakeChatSettlementExecutor{err: settlementErr}
	authorizer := &fakeChatAuthorizer{
		authorization: lifecycle.ChatAuthorization{ReservationID: 7701},
	}

	fakeAdapter := &fakeChatAdapter{
		chatResp: chatResponse("adapter response"),
	}
	requestLog := newFakeRequestLogService()
	service := newChatCompletionServiceForTestWithAuthorizer(
		&fakeChatRouter{plan: routePlan(routeCandidate("openai", 123, "gpt-4.1"))},
		&fakeAdapterRegistry{
			chatAdapters: map[string]openai.ChatAdapter{
				"openai": fakeAdapter,
			},
		},
		nil,
		requestLog,
		settlement,
		authorizer,
	)

	_, err := service.CreateChatCompletion(contextWithPrincipal(42), chatRequest())
	if !errors.Is(err, settlementErr) {
		t.Fatalf("expected settlement error, got %v", err)
	}
	if len(settlement.params) != 1 {
		t.Fatalf("expected settlement to be called once, got %d", len(settlement.params))
	}
	if settlement.params[0].Authorization.ReservationID != 7701 {
		t.Fatalf("expected settlement reservation id %d, got %d", int64(7701), settlement.params[0].Authorization.ReservationID)
	}
	if len(authorizer.releaseParams) != 0 {
		t.Fatalf("expected no authorization release when settlement fails, got %d", len(authorizer.releaseParams))
	}
	if len(requestLog.markRequestFailedArgs) != 1 {
		t.Fatalf("expected request to be marked failed once, got %d", len(requestLog.markRequestFailedArgs))
	}
	if requestLog.markRequestFailedArgs[0].ErrorCode != "chat_settlement_failed" {
		t.Fatalf("expected chat_settlement_failed, got %q", requestLog.markRequestFailedArgs[0].ErrorCode)
	}
	if len(requestLog.markRequestSucceededArgs) != 0 {
		t.Fatalf("expected request not to be marked succeeded, got %d", len(requestLog.markRequestSucceededArgs))
	}
}

func TestChatCompletionServiceCreateChatCompletionDoesNotCallAdapterWhenAuthorizationFails(t *testing.T) {
	authorizationErr := errors.New("authorization failed")
	fakeAdapter := &fakeChatAdapter{chatResp: chatResponse("should not call")}
	requestLog := newFakeRequestLogService()
	settlement := newChatCompletionSettlementForTest()
	authorizer := &fakeChatAuthorizer{authorizeErr: authorizationErr}
	service := newChatCompletionServiceForTestWithAuthorizer(
		&fakeChatRouter{plan: routePlan(routeCandidate("openai", 123, "gpt-4.1"))},
		&fakeAdapterRegistry{
			chatAdapters: map[string]openai.ChatAdapter{
				"openai": fakeAdapter,
			},
		},
		nil,
		requestLog,
		settlement,
		authorizer,
	)

	_, err := service.CreateChatCompletion(contextWithPrincipal(42), chatRequest())
	if !errors.Is(err, authorizationErr) {
		t.Fatalf("expected authorization error, got %v", err)
	}
	if len(authorizer.authorizeParams) != 1 {
		t.Fatalf("expected one authorization attempt, got %d", len(authorizer.authorizeParams))
	}
	if authorizer.authorizeParams[0].RequestRecord.ID != 1 {
		t.Fatalf("expected authorization request record id 1, got %d", authorizer.authorizeParams[0].RequestRecord.ID)
	}
	if authorizer.authorizeParams[0].ModelDBID != 1123 {
		t.Fatalf("expected authorization model db id %d, got %d", int64(1123), authorizer.authorizeParams[0].ModelDBID)
	}
	if fakeAdapter.chatCalled != 0 {
		t.Fatalf("expected adapter not to be called after authorization failure, got %d", fakeAdapter.chatCalled)
	}
	if len(authorizer.releaseParams) != 0 {
		t.Fatalf("expected no release when authorization was not created, got %d", len(authorizer.releaseParams))
	}
	if len(settlement.params) != 0 {
		t.Fatalf("expected settlement not to be called, got %d", len(settlement.params))
	}
	if len(requestLog.createAttempts) != 0 {
		t.Fatalf("expected no attempt before authorization succeeds, got %d", len(requestLog.createAttempts))
	}
	if len(requestLog.markAttemptFailedArgs) != 0 {
		t.Fatalf("expected no failed attempt before authorization succeeds, got %d", len(requestLog.markAttemptFailedArgs))
	}
	if len(requestLog.markRequestFailedArgs) != 1 {
		t.Fatalf("expected request to fail once, got %d", len(requestLog.markRequestFailedArgs))
	}
	if requestLog.markRequestFailedArgs[0].ErrorCode != "chat_authorization_failed" {
		t.Fatalf("expected request error code %q, got %q", "chat_authorization_failed", requestLog.markRequestFailedArgs[0].ErrorCode)
	}
}

func TestChatCompletionServiceCreateChatCompletionReleasesAuthorizationWhenAttemptCreateFails(t *testing.T) {
	attemptErr := errors.New("create attempt failed")
	fakeAdapter := &fakeChatAdapter{chatResp: chatResponse("should not call")}
	requestLog := newFakeRequestLogService()
	requestLog.createAttemptErr = attemptErr
	authorizer := &fakeChatAuthorizer{
		authorization: lifecycle.ChatAuthorization{ReservationID: 7711},
	}
	service := newChatCompletionServiceForTestWithAuthorizer(
		&fakeChatRouter{plan: routePlan(routeCandidate("openai", 123, "gpt-4.1"))},
		&fakeAdapterRegistry{
			chatAdapters: map[string]openai.ChatAdapter{
				"openai": fakeAdapter,
			},
		},
		nil,
		requestLog,
		newChatCompletionSettlementForTest(),
		authorizer,
	)

	_, err := service.CreateChatCompletion(contextWithPrincipal(42), chatRequest())
	if !errors.Is(err, attemptErr) {
		t.Fatalf("expected attempt creation error, got %v", err)
	}
	if fakeAdapter.chatCalled != 0 {
		t.Fatalf("expected no adapter call after attempt creation failure, got %d", fakeAdapter.chatCalled)
	}
	if len(authorizer.releaseParams) != 1 || authorizer.releaseParams[0].ReservationID != 7711 {
		t.Fatalf("expected reservation 7711 to be released, got %#v", authorizer.releaseParams)
	}
	if len(requestLog.markRequestFailedArgs) != 1 || requestLog.markRequestFailedArgs[0].ErrorCode != "request_attempt_create_failed" {
		t.Fatalf("expected request_attempt_create_failed, got %#v", requestLog.markRequestFailedArgs)
	}
}

func TestChatCompletionServiceCreateChatCompletionReturnsMissingAdapterWithoutRetry(t *testing.T) {
	classifier := &fakeRetryClassifier{retryable: true}
	requestLog := newFakeRequestLogService()
	settlement := newChatCompletionSettlementForTest()
	authorizer := newChatCompletionAuthorizerForTest()
	service := newChatCompletionServiceForTestWithAuthorizer(
		&fakeChatRouter{plan: routePlan(routeCandidate("missing", 123, "gpt-4.1"))},
		&fakeAdapterRegistry{chatAdapters: map[string]openai.ChatAdapter{}},
		classifier,
		requestLog,
		settlement,
		authorizer,
	)

	_, err := service.CreateChatCompletion(contextWithPrincipal(42), chatRequest())
	if err == nil {
		t.Fatal("expected missing adapter error")
	}
	if classifier.called != 0 {
		t.Fatalf("expected retry classifier not to be called, got %d calls", classifier.called)
	}
	if len(settlement.params) != 0 {
		t.Fatalf("expected settlement not to be called, got %d calls", len(settlement.params))
	}
	if len(authorizer.authorizeParams) != 1 {
		t.Fatalf("expected one authorization before defensive adapter lookup, got %d", len(authorizer.authorizeParams))
	}
	if len(authorizer.releaseParams) != 1 {
		t.Fatalf("expected authorization release after defensive adapter lookup fails, got %d", len(authorizer.releaseParams))
	}
	if len(requestLog.createAttempts) != 1 {
		t.Fatalf("expected one attempt for missing adapter, got %d", len(requestLog.createAttempts))
	}
	if len(requestLog.markAttemptFailedArgs) != 1 {
		t.Fatalf("expected one failed attempt, got %d", len(requestLog.markAttemptFailedArgs))
	}
	if requestLog.markAttemptFailedArgs[0].ErrorCode != string(failure.CodeGatewayAdapterNotRegistered) {
		t.Fatalf("expected %q, got %q", failure.CodeGatewayAdapterNotRegistered, requestLog.markAttemptFailedArgs[0].ErrorCode)
	}
	if requestLog.markAttemptFailedArgs[0].UpstreamStatusCode != nil {
		t.Fatalf("expected unknown upstream status to stay nil, got %v", requestLog.markAttemptFailedArgs[0].UpstreamStatusCode)
	}
	if len(requestLog.markRequestFailedArgs) != 1 {
		t.Fatalf("expected one failed request, got %d", len(requestLog.markRequestFailedArgs))
	}
	if requestLog.markRequestFailedArgs[0].ErrorCode != string(failure.CodeGatewayAdapterNotRegistered) {
		t.Fatalf("expected %q, got %q", failure.CodeGatewayAdapterNotRegistered, requestLog.markRequestFailedArgs[0].ErrorCode)
	}
}

func TestChatCompletionServiceCreateChatCompletionReleasesAuthorizationWhenFallbackAdapterMissing(t *testing.T) {
	upstreamErr := errors.New("temporary upstream error")
	firstAdapter := &fakeChatAdapter{chatErr: upstreamErr}
	classifier := &fakeRetryClassifier{retryable: true}
	requestLog := newFakeRequestLogService()
	settlement := newChatCompletionSettlementForTest()
	authorizer := &fakeChatAuthorizer{
		authorization: lifecycle.ChatAuthorization{ReservationID: 8811},
	}
	service := newChatCompletionServiceForTestWithAuthorizer(
		&fakeChatRouter{plan: routePlan(
			routeCandidate("openai-primary", 101, "gpt-4.1"),
			routeCandidate("missing-secondary", 102, "gpt-4.1"),
		)},
		&fakeAdapterRegistry{
			chatAdapters: map[string]openai.ChatAdapter{
				"openai-primary": firstAdapter,
			},
		},
		classifier,
		requestLog,
		settlement,
		authorizer,
	)

	_, err := service.CreateChatCompletion(contextWithPrincipal(42), chatRequest())
	if err == nil {
		t.Fatal("expected missing fallback adapter error")
	}
	if firstAdapter.chatCalled != 1 {
		t.Fatalf("expected first adapter to be called once, got %d", firstAdapter.chatCalled)
	}
	if classifier.called != 1 {
		t.Fatalf("expected retry classifier to be called once, got %d", classifier.called)
	}
	if len(settlement.params) != 0 {
		t.Fatalf("expected settlement not to be called, got %d", len(settlement.params))
	}
	if len(authorizer.releaseParams) != 1 {
		t.Fatalf("expected authorization release when fallback adapter is missing, got %d", len(authorizer.releaseParams))
	}
	if authorizer.releaseParams[0].ReservationID != 8811 {
		t.Fatalf("expected released reservation id %d, got %d", int64(8811), authorizer.releaseParams[0].ReservationID)
	}
}

func TestChatCompletionServiceCreateChatCompletionFallsBackOnRetryableAdapterError(t *testing.T) {
	upstreamErr := errors.New("temporary upstream error")
	firstAdapter := &fakeChatAdapter{chatErr: upstreamErr}
	secondAdapter := &fakeChatAdapter{chatResp: chatResponse("fallback response")}
	classifier := &fakeRetryClassifier{retryable: true}
	requestLog := newFakeRequestLogService()
	settlement := newChatCompletionSettlementForTest()
	authorizer := &fakeChatAuthorizer{
		authorization: lifecycle.ChatAuthorization{ReservationID: 7799},
	}
	service := newChatCompletionServiceForTestWithAuthorizer(
		&fakeChatRouter{plan: routePlan(
			routeCandidate("openai-primary", 101, "gpt-4.1"),
			routeCandidate("openai-secondary", 102, "gpt-4.1"),
		)},
		&fakeAdapterRegistry{
			chatAdapters: map[string]openai.ChatAdapter{
				"openai-primary":   firstAdapter,
				"openai-secondary": secondAdapter,
			},
		},
		classifier,
		requestLog,
		settlement,
		authorizer,
	)

	got, err := service.CreateChatCompletion(contextWithPrincipal(42), chatRequest())
	if err != nil {
		t.Fatalf("CreateChatCompletion returned err: %v", err)
	}
	if firstAdapter.chatCalled != 1 {
		t.Fatalf("expected first adapter to be called once, got %d", firstAdapter.chatCalled)
	}
	if secondAdapter.chatCalled != 1 {
		t.Fatalf("expected second adapter to be called once, got %d", secondAdapter.chatCalled)
	}
	if classifier.called != 1 {
		t.Fatalf("expected retry classifier to be called once, got %d", classifier.called)
	}
	if got.Choices[0].Message.ContentString() != "fallback response" {
		t.Fatalf("expected fallback response, got %q", got.Choices[0].Message.Content)
	}
	if len(requestLog.createAttempts) != 2 {
		t.Fatalf("expected two attempts, got %d", len(requestLog.createAttempts))
	}
	if requestLog.createAttempts[0].AttemptIndex != 0 || requestLog.createAttempts[1].AttemptIndex != 1 {
		t.Fatalf("expected attempt indexes 0 and 1, got %#v", requestLog.createAttempts)
	}
	if len(requestLog.markAttemptFailedArgs) != 1 {
		t.Fatalf("expected first attempt to fail once, got %d", len(requestLog.markAttemptFailedArgs))
	}
	if requestLog.markAttemptFailedArgs[0].ErrorCode != "adapter_error" {
		t.Fatalf("expected adapter_error, got %q", requestLog.markAttemptFailedArgs[0].ErrorCode)
	}
	if len(requestLog.markAttemptSucceededArgs) != 0 {
		t.Fatalf("expected attempt success to be handled by settlement, got requestlog succeeded attempts %d", len(requestLog.markAttemptSucceededArgs))
	}
	if len(settlement.params) != 1 {
		t.Fatalf("expected request to be settled once, got %d", len(settlement.params))
	}
	if len(authorizer.authorizeParams) != 1 {
		t.Fatalf("expected one request-level authorization across fallback, got %d", len(authorizer.authorizeParams))
	}
	if settlement.params[0].Authorization.ReservationID != 7799 {
		t.Fatalf("expected settlement reservation id %d, got %d", int64(7799), settlement.params[0].Authorization.ReservationID)
	}
	if len(authorizer.releaseParams) != 0 {
		t.Fatalf("expected no authorization release after successful fallback settlement, got %d", len(authorizer.releaseParams))
	}
	if len(requestLog.markRequestFailedArgs) != 0 {
		t.Fatalf("expected request not to fail, got %#v", requestLog.markRequestFailedArgs)
	}
}

func TestChatCompletionServiceCreateChatCompletionReleasesAuthorizationWhenAllRetryableAttemptsFail(t *testing.T) {
	firstErr := errors.New("temporary upstream error")
	secondErr := errors.New("second upstream error")
	firstAdapter := &fakeChatAdapter{chatErr: firstErr}
	secondAdapter := &fakeChatAdapter{chatErr: secondErr}
	classifier := &fakeRetryClassifier{retryable: true}
	requestLog := newFakeRequestLogService()
	settlement := newChatCompletionSettlementForTest()
	authorizer := &fakeChatAuthorizer{
		authorization: lifecycle.ChatAuthorization{ReservationID: 7710},
	}
	service := newChatCompletionServiceForTestWithAuthorizer(
		&fakeChatRouter{plan: routePlan(
			routeCandidate("openai-primary", 101, "gpt-4.1"),
			routeCandidate("openai-secondary", 102, "gpt-4.1"),
		)},
		&fakeAdapterRegistry{
			chatAdapters: map[string]openai.ChatAdapter{
				"openai-primary":   firstAdapter,
				"openai-secondary": secondAdapter,
			},
		},
		classifier,
		requestLog,
		settlement,
		authorizer,
	)

	_, err := service.CreateChatCompletion(contextWithPrincipal(42), chatRequest())
	if !errors.Is(err, secondErr) {
		t.Fatalf("expected last retryable adapter error, got %v", err)
	}
	if firstAdapter.chatCalled != 1 || secondAdapter.chatCalled != 1 {
		t.Fatalf("expected both adapters to be called once, got first=%d second=%d", firstAdapter.chatCalled, secondAdapter.chatCalled)
	}
	if classifier.called != 2 {
		t.Fatalf("expected retry classifier to be called twice, got %d", classifier.called)
	}
	if len(authorizer.authorizeParams) != 1 {
		t.Fatalf("expected one request-level authorization, got %d", len(authorizer.authorizeParams))
	}
	if len(authorizer.releaseParams) != 1 {
		t.Fatalf("expected one authorization release after all retryable attempts fail, got %d", len(authorizer.releaseParams))
	}
	if authorizer.releaseParams[0].ReservationID != 7710 {
		t.Fatalf("expected released reservation id %d, got %d", int64(7710), authorizer.releaseParams[0].ReservationID)
	}
	if len(settlement.params) != 0 {
		t.Fatalf("expected settlement not to be called, got %d", len(settlement.params))
	}
	if len(requestLog.markAttemptFailedArgs) != 2 {
		t.Fatalf("expected two failed attempts, got %d", len(requestLog.markAttemptFailedArgs))
	}
	if len(requestLog.markRequestFailedArgs) != 1 {
		t.Fatalf("expected request to fail once, got %d", len(requestLog.markRequestFailedArgs))
	}
	if requestLog.markRequestFailedArgs[0].ErrorCode != "adapter_error" {
		t.Fatalf("expected request error code %q, got %q", "adapter_error", requestLog.markRequestFailedArgs[0].ErrorCode)
	}
}

func TestChatCompletionServiceCreateChatCompletionDoesNotFallbackOnNonRetryableAdapterError(t *testing.T) {
	upstreamErr := errors.New("invalid upstream request")
	firstAdapter := &fakeChatAdapter{chatErr: upstreamErr}
	secondAdapter := &fakeChatAdapter{chatResp: chatResponse("fallback response")}
	classifier := &fakeRetryClassifier{retryable: false}
	requestLog := newFakeRequestLogService()
	authorizer := &fakeChatAuthorizer{
		authorization: lifecycle.ChatAuthorization{ReservationID: 7720},
	}
	service := newChatCompletionServiceForTestWithAuthorizer(
		&fakeChatRouter{plan: routePlan(
			routeCandidate("openai-primary", 101, "gpt-4.1"),
			routeCandidate("openai-secondary", 102, "gpt-4.1"),
		)},
		&fakeAdapterRegistry{
			chatAdapters: map[string]openai.ChatAdapter{
				"openai-primary":   firstAdapter,
				"openai-secondary": secondAdapter,
			},
		},
		classifier,
		requestLog,
		newChatCompletionSettlementForTest(),
		authorizer,
	)

	_, err := service.CreateChatCompletion(contextWithPrincipal(42), chatRequest())
	if !errors.Is(err, upstreamErr) {
		t.Fatalf("expected non-retryable adapter error, got %v", err)
	}
	if firstAdapter.chatCalled != 1 {
		t.Fatalf("expected first adapter to be called once, got %d", firstAdapter.chatCalled)
	}
	if secondAdapter.chatCalled != 0 {
		t.Fatalf("expected second adapter not to be called, got %d", secondAdapter.chatCalled)
	}
	if classifier.called != 1 {
		t.Fatalf("expected retry classifier to be called once, got %d", classifier.called)
	}
	if len(authorizer.releaseParams) != 1 {
		t.Fatalf("expected authorization release after non-retryable adapter error, got %d", len(authorizer.releaseParams))
	}
	if authorizer.releaseParams[0].ReservationID != 7720 {
		t.Fatalf("expected released reservation id %d, got %d", int64(7720), authorizer.releaseParams[0].ReservationID)
	}
	if len(requestLog.createAttempts) != 1 {
		t.Fatalf("expected only first attempt to be created, got %d", len(requestLog.createAttempts))
	}
	if len(requestLog.markAttemptFailedArgs) != 1 {
		t.Fatalf("expected first attempt to fail once, got %d", len(requestLog.markAttemptFailedArgs))
	}
	if requestLog.markAttemptFailedArgs[0].ErrorCode != "adapter_error" {
		t.Fatalf("expected adapter_error, got %q", requestLog.markAttemptFailedArgs[0].ErrorCode)
	}
	if len(requestLog.markRequestFailedArgs) != 1 {
		t.Fatalf("expected request to fail once, got %d", len(requestLog.markRequestFailedArgs))
	}
	if requestLog.markRequestFailedArgs[0].ErrorCode != "adapter_error" {
		t.Fatalf("expected adapter_error, got %q", requestLog.markRequestFailedArgs[0].ErrorCode)
	}
}

func TestChatCompletionServiceCreateChatCompletionReturnsReleaseErrorWhenAdapterErrorReleaseFails(t *testing.T) {
	upstreamErr := errors.New("invalid upstream request")
	releaseErr := errors.New("release failed")
	firstAdapter := &fakeChatAdapter{chatErr: upstreamErr}
	classifier := &fakeRetryClassifier{retryable: false}
	requestLog := newFakeRequestLogService()
	authorizer := &fakeChatAuthorizer{
		authorization: lifecycle.ChatAuthorization{ReservationID: 7730},
		releaseErr:    releaseErr,
	}
	service := newChatCompletionServiceForTestWithAuthorizer(
		&fakeChatRouter{plan: routePlan(routeCandidate("openai-primary", 101, "gpt-4.1"))},
		&fakeAdapterRegistry{
			chatAdapters: map[string]openai.ChatAdapter{
				"openai-primary": firstAdapter,
			},
		},
		classifier,
		requestLog,
		newChatCompletionSettlementForTest(),
		authorizer,
	)

	_, err := service.CreateChatCompletion(contextWithPrincipal(42), chatRequest())
	if !errors.Is(err, releaseErr) {
		t.Fatalf("expected release error to be returned, got %v", err)
	}
	if len(authorizer.releaseParams) != 1 {
		t.Fatalf("expected one release attempt, got %d", len(authorizer.releaseParams))
	}
	if authorizer.releaseParams[0].ReservationID != 7730 {
		t.Fatalf("expected released reservation id %d, got %d", int64(7730), authorizer.releaseParams[0].ReservationID)
	}
	if len(requestLog.markAttemptFailedArgs) != 1 {
		t.Fatalf("expected original adapter attempt to be marked failed once, got %d", len(requestLog.markAttemptFailedArgs))
	}
	if len(requestLog.markRequestFailedArgs) != 1 {
		t.Fatalf("expected request to be marked failed by release failure, got %d", len(requestLog.markRequestFailedArgs))
	}
	if requestLog.markRequestFailedArgs[0].ErrorCode != "chat_authorization_release_failed" {
		t.Fatalf("expected request error code %q, got %q", "chat_authorization_release_failed", requestLog.markRequestFailedArgs[0].ErrorCode)
	}
}

func TestChatCompletionServiceCreateChatCompletionMarksCanceledWithoutFallback(t *testing.T) {
	firstAdapter := &fakeChatAdapter{chatErr: context.Canceled}
	secondAdapter := &fakeChatAdapter{chatResp: chatResponse("should not call")}
	classifier := &fakeRetryClassifier{retryable: true}
	requestLog := newFakeRequestLogService()
	settlement := newChatCompletionSettlementForTest()
	authorizer := &fakeChatAuthorizer{
		authorization: lifecycle.ChatAuthorization{ReservationID: 7799},
	}
	service := newChatCompletionServiceForTestWithAuthorizer(
		&fakeChatRouter{plan: routePlan(
			routeCandidate("openai-primary", 101, "gpt-4.1"),
			routeCandidate("openai-secondary", 102, "gpt-4.1"),
		)},
		&fakeAdapterRegistry{
			chatAdapters: map[string]openai.ChatAdapter{
				"openai-primary":   firstAdapter,
				"openai-secondary": secondAdapter,
			},
		},
		classifier,
		requestLog,
		settlement,
		authorizer,
	)

	_, err := service.CreateChatCompletion(contextWithPrincipal(42), chatRequest())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled error, got %v", err)
	}
	if firstAdapter.chatCalled != 1 {
		t.Fatalf("expected first adapter to be called once, got %d", firstAdapter.chatCalled)
	}
	if secondAdapter.chatCalled != 0 {
		t.Fatalf("expected second adapter not to be called, got %d", secondAdapter.chatCalled)
	}
	if classifier.called != 0 {
		t.Fatalf("expected retry classifier not to be called after client cancel, got %d", classifier.called)
	}
	if len(settlement.params) != 0 {
		t.Fatalf("expected settlement not to be called, got %d calls", len(settlement.params))
	}
	if len(authorizer.releaseParams) != 1 {
		t.Fatalf("expected one authorization release after client cancel, got %d", len(authorizer.releaseParams))
	}
	if authorizer.releaseParams[0].ReservationID != 7799 {
		t.Fatalf("expected released reservation id %d, got %d", int64(7799), authorizer.releaseParams[0].ReservationID)
	}
	if len(requestLog.markAttemptCanceledArgs) != 1 {
		t.Fatalf("expected 1 attempt canceled call, got %d", len(requestLog.markAttemptCanceledArgs))
	}
	if requestLog.markAttemptCanceledArgs[0].ErrorCode != "client_canceled" {
		t.Fatalf("expected attempt canceled code %q, got %q", "client_canceled", requestLog.markAttemptCanceledArgs[0].ErrorCode)
	}
	if len(requestLog.markRequestCanceledArgs) != 1 {
		t.Fatalf("expected 1 request canceled call, got %d", len(requestLog.markRequestCanceledArgs))
	}
	if requestLog.markRequestCanceledArgs[0].ErrorCode != "client_canceled" {
		t.Fatalf("expected request canceled code %q, got %q", "client_canceled", requestLog.markRequestCanceledArgs[0].ErrorCode)
	}
	if len(requestLog.markAttemptFailedArgs) != 0 {
		t.Fatalf("expected no attempt failed call, got %d", len(requestLog.markAttemptFailedArgs))
	}
	if len(requestLog.markRequestFailedArgs) != 0 {
		t.Fatalf("expected no request failed call, got %d", len(requestLog.markRequestFailedArgs))
	}
}

func TestChatCompletionServiceStreamChatCompletionRoutesAndCallsAdapter(t *testing.T) {
	fakeAdapter := &fakeChatAdapter{
		streamResp: []openai.ChatStreamChunk{
			{
				ID:      "chatcmpl_mock",
				Model:   "gpt-4.1",
				Role:    "assistant",
				Content: "mock response",
			},
			streamUsageChunk("gpt-4.1"),
		},
	}
	router := &fakeChatRouter{
		plan: routePlan(routeCandidate("openai", 123, "gpt-4.1")),
	}
	registry := &fakeAdapterRegistry{
		streamChatAdapters: map[string]openai.StreamChatAdapter{
			"openai": fakeAdapter,
		},
	}
	requestLog := newFakeRequestLogService()
	settlement := newChatCompletionSettlementForTest()
	authorizer := &fakeChatAuthorizer{
		authorization: lifecycle.ChatAuthorization{ReservationID: 8820},
	}
	service := newChatCompletionServiceForTestWithAuthorizer(router, registry, nil, requestLog, settlement, authorizer)

	chunks := make([]gatewayapi.ChatCompletionStreamResponse, 0)
	err := service.StreamChatCompletion(contextWithPrincipal(42), chatRequestWithParams(), func(chunk gatewayapi.ChatCompletionStreamResponse) error {
		chunks = append(chunks, chunk)
		return nil
	})
	if err != nil {
		t.Fatalf("StreamChatCompletion returned err: %v", err)
	}

	if !router.called {
		t.Fatal("expected routing to be called")
	}
	if len(registry.streamChatKeys) != 1 || registry.streamChatKeys[0] != "openai" {
		t.Fatalf("expected adapter key %q, got %#v", "openai", registry.streamChatKeys)
	}
	if fakeAdapter.streamCalled != 1 {
		t.Fatalf("expected stream adapter to be called once, got %d", fakeAdapter.streamCalled)
	}
	if fakeAdapter.streamReq.Model != "gpt-4.1" {
		t.Fatalf("expected upstream model %q, got %q", "gpt-4.1", fakeAdapter.streamReq.Model)
	}
	assertAdapterChatRequestParams(t, fakeAdapter.streamReq)
	if fakeAdapter.ch.ID != 123 {
		t.Fatalf("expected channel id %d, got %d", int64(123), fakeAdapter.ch.ID)
	}
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if chunks[0].Model != "openai/gpt-4.1" {
		t.Fatalf("expected stream response model %q, got %q", "openai/gpt-4.1", chunks[0].Model)
	}
	if chunks[0].Choices[0].Delta.Content != "mock response" {
		t.Fatalf("expected content %q, got %q", "mock response", chunks[0].Choices[0].Delta.Content)
	}
	if len(requestLog.createRequests) != 1 {
		t.Fatalf("expected one stream request record, got %d", len(requestLog.createRequests))
	}
	if !requestLog.createRequests[0].Stream {
		t.Fatal("expected request record stream flag to be true")
	}
	if len(requestLog.markRequestRunningIDs) != 1 || requestLog.markRequestRunningIDs[0] != 1 {
		t.Fatalf("expected request to be marked running once, got %#v", requestLog.markRequestRunningIDs)
	}
	if len(requestLog.createAttempts) != 1 {
		t.Fatalf("expected one request attempt, got %d", len(requestLog.createAttempts))
	}
	if len(requestLog.markAttemptSucceededArgs) != 0 {
		t.Fatalf("expected attempt success to be handled by settlement, got %d direct calls", len(requestLog.markAttemptSucceededArgs))
	}
	if len(requestLog.markRequestSucceededArgs) != 0 {
		t.Fatalf("expected request success to be handled by settlement, got %d direct calls", len(requestLog.markRequestSucceededArgs))
	}
	if len(settlement.params) != 1 {
		t.Fatalf("expected one settlement call, got %d", len(settlement.params))
	}
	if settlement.params[0].AttemptRecord.ID != 1 {
		t.Fatalf("expected settlement attempt id 1, got %d", settlement.params[0].AttemptRecord.ID)
	}
	if settlement.params[0].ResponseModelID != "openai/gpt-4.1" {
		t.Fatalf("expected settlement response model %q, got %q", "openai/gpt-4.1", settlement.params[0].ResponseModelID)
	}
	if settlement.params[0].Facts.UpstreamModel != "gpt-4.1" {
		t.Fatalf("expected settlement upstream model %q, got %q", "gpt-4.1", settlement.params[0].Facts.UpstreamModel)
	}
	if settlement.params[0].Facts.Metadata.StatusCode != 200 {
		t.Fatalf("expected stream settlement upstream status 200, got %d", settlement.params[0].Facts.Metadata.StatusCode)
	}
	if settlement.params[0].Facts.Metadata.RequestID != "req-stream-1" {
		t.Fatalf("expected stream settlement upstream request id %q, got %q", "req-stream-1", settlement.params[0].Facts.Metadata.RequestID)
	}
	if v, ok := settlement.params[0].Facts.Usage.CacheReadInputTokens.BillableValue(); !ok || v != 3 {
		t.Fatalf("expected cache-read tokens %d, got %d (ok=%v)", 3, v, ok)
	}
	if v, ok := settlement.params[0].Facts.Usage.ReasoningOutputTokens.BillableValue(); !ok || v != 2 {
		t.Fatalf("expected reasoning tokens %d, got %d (ok=%v)", 2, v, ok)
	}
	if len(authorizer.authorizeParams) != 1 {
		t.Fatalf("expected one stream authorization, got %d", len(authorizer.authorizeParams))
	}
	if settlement.params[0].Authorization.ReservationID != 8820 {
		t.Fatalf("expected settlement reservation id %d, got %d", int64(8820), settlement.params[0].Authorization.ReservationID)
	}
	if len(authorizer.releaseParams) != 0 {
		t.Fatalf("expected no authorization release on successful stream settlement, got %d", len(authorizer.releaseParams))
	}
	if len(authorizer.releaseBillingExceptionParams) != 0 {
		t.Fatalf("expected no billing exception release on successful stream settlement, got %d", len(authorizer.releaseBillingExceptionParams))
	}
}

func TestChatCompletionServiceStreamChatCompletionEmitsClientUsageWhenRequested(t *testing.T) {
	fakeAdapter := &fakeChatAdapter{
		streamResp: []openai.ChatStreamChunk{
			{
				ID:      "chatcmpl_mock",
				Model:   "gpt-4.1",
				Role:    "assistant",
				Content: "mock response",
			},
			streamUsageChunk("gpt-4.1"),
		},
	}
	service := newChatCompletionServiceForTestWithAuthorizer(
		&fakeChatRouter{plan: routePlan(routeCandidate("openai", 123, "gpt-4.1"))},
		&fakeAdapterRegistry{
			streamChatAdapters: map[string]openai.StreamChatAdapter{
				"openai": fakeAdapter,
			},
		},
		nil,
		newFakeRequestLogService(),
		newChatCompletionSettlementForTest(),
		&fakeChatAuthorizer{authorization: lifecycle.ChatAuthorization{ReservationID: 8820}},
	)

	req := chatRequest()
	includeUsage := true
	req.StreamOptions = &gatewayapi.ChatCompletionStreamOptions{
		IncludeUsage: &includeUsage,
	}

	chunks := make([]gatewayapi.ChatCompletionStreamResponse, 0)
	err := service.StreamChatCompletion(contextWithPrincipal(42), req, func(chunk gatewayapi.ChatCompletionStreamResponse) error {
		chunks = append(chunks, chunk)
		return nil
	})
	if err != nil {
		t.Fatalf("StreamChatCompletion returned err: %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(chunks))
	}
	if chunks[1].Usage == nil {
		t.Fatal("expected final chunk to include usage")
	}
	if chunks[1].Usage.PromptTokens != 10 || chunks[1].Usage.CompletionTokens != 11 || chunks[1].Usage.TotalTokens != 21 {
		t.Fatalf("unexpected client usage %#v", chunks[1].Usage)
	}
	if len(chunks[1].Choices) != 0 {
		t.Fatalf("expected empty choices on usage chunk, got %d", len(chunks[1].Choices))
	}
	if chunks[1].ID != "chatcmpl_mock" {
		t.Fatalf("expected usage chunk id %q, got %q", "chatcmpl_mock", chunks[1].ID)
	}
}

func TestChatCompletionServiceStreamChatCompletionReturnsMissingAdapterWithoutRetry(t *testing.T) {
	classifier := &fakeRetryClassifier{retryable: true}
	service := newChatCompletionServiceForTest(
		&fakeChatRouter{plan: routePlan(routeCandidate("missing", 123, "gpt-4.1"))},
		&fakeAdapterRegistry{streamChatAdapters: map[string]openai.StreamChatAdapter{}},
		classifier,
		newFakeRequestLogService(),
		newChatCompletionSettlementForTest(),
	)

	err := service.StreamChatCompletion(contextWithPrincipal(42), chatRequest(), func(chunk gatewayapi.ChatCompletionStreamResponse) error {
		return nil
	})
	if err == nil {
		t.Fatal("expected missing stream adapter error")
	}
	if classifier.called != 0 {
		t.Fatalf("expected retry classifier not to be called, got %d calls", classifier.called)
	}
}

func TestChatCompletionServiceStreamChatCompletionDoesNotCallAdapterWhenAuthorizationFails(t *testing.T) {
	authorizationErr := errors.New("stream authorization failed")
	fakeAdapter := &fakeChatAdapter{
		streamResp: []openai.ChatStreamChunk{
			{Content: "should not emit"},
			streamUsageChunk("gpt-4.1"),
		},
	}
	requestLog := newFakeRequestLogService()
	settlement := newChatCompletionSettlementForTest()
	authorizer := &fakeChatAuthorizer{authorizeErr: authorizationErr}
	service := newChatCompletionServiceForTestWithAuthorizer(
		&fakeChatRouter{plan: routePlan(routeCandidate("openai", 123, "gpt-4.1"))},
		&fakeAdapterRegistry{
			streamChatAdapters: map[string]openai.StreamChatAdapter{
				"openai": fakeAdapter,
			},
		},
		nil,
		requestLog,
		settlement,
		authorizer,
	)

	err := service.StreamChatCompletion(contextWithPrincipal(42), chatRequest(), func(chunk gatewayapi.ChatCompletionStreamResponse) error {
		t.Fatalf("expected no stream chunk after authorization failure, got %#v", chunk)
		return nil
	})
	if !errors.Is(err, authorizationErr) {
		t.Fatalf("expected authorization error, got %v", err)
	}
	if len(authorizer.authorizeParams) != 1 {
		t.Fatalf("expected one authorization attempt, got %d", len(authorizer.authorizeParams))
	}
	if fakeAdapter.streamCalled != 0 {
		t.Fatalf("expected stream adapter not to be called after authorization failure, got %d", fakeAdapter.streamCalled)
	}
	if len(authorizer.releaseParams) != 0 {
		t.Fatalf("expected no release when stream authorization was not created, got %d", len(authorizer.releaseParams))
	}
	if len(settlement.params) != 0 {
		t.Fatalf("expected settlement not to be called, got %d", len(settlement.params))
	}
	if len(requestLog.createAttempts) != 0 {
		t.Fatalf("expected no stream attempt before authorization succeeds, got %d", len(requestLog.createAttempts))
	}
	if len(requestLog.markAttemptFailedArgs) != 0 {
		t.Fatalf("expected no failed stream attempt before authorization succeeds, got %d", len(requestLog.markAttemptFailedArgs))
	}
	if len(requestLog.markRequestFailedArgs) != 1 {
		t.Fatalf("expected stream request to fail once, got %d", len(requestLog.markRequestFailedArgs))
	}
	if requestLog.markRequestFailedArgs[0].ErrorCode != "chat_authorization_failed" {
		t.Fatalf("expected request error code %q, got %q", "chat_authorization_failed", requestLog.markRequestFailedArgs[0].ErrorCode)
	}
}

func TestChatCompletionServiceStreamChatCompletionReleasesAuthorizationWhenAttemptCreateFails(t *testing.T) {
	attemptErr := errors.New("create stream attempt failed")
	fakeAdapter := &fakeChatAdapter{
		streamResp: []openai.ChatStreamChunk{
			{Content: "should not emit"},
			streamUsageChunk("gpt-4.1"),
		},
	}
	requestLog := newFakeRequestLogService()
	requestLog.createAttemptErr = attemptErr
	authorizer := &fakeChatAuthorizer{
		authorization: lifecycle.ChatAuthorization{ReservationID: 9900},
	}
	service := newChatCompletionServiceForTestWithAuthorizer(
		&fakeChatRouter{plan: routePlan(routeCandidate("openai", 123, "gpt-4.1"))},
		&fakeAdapterRegistry{
			streamChatAdapters: map[string]openai.StreamChatAdapter{
				"openai": fakeAdapter,
			},
		},
		nil,
		requestLog,
		newChatCompletionSettlementForTest(),
		authorizer,
	)

	err := service.StreamChatCompletion(contextWithPrincipal(42), chatRequest(), func(chunk gatewayapi.ChatCompletionStreamResponse) error {
		t.Fatalf("expected no stream chunk after attempt creation failure, got %#v", chunk)
		return nil
	})
	if !errors.Is(err, attemptErr) {
		t.Fatalf("expected stream attempt creation error, got %v", err)
	}
	if fakeAdapter.streamCalled != 0 {
		t.Fatalf("expected no stream adapter call after attempt creation failure, got %d", fakeAdapter.streamCalled)
	}
	if len(authorizer.releaseParams) != 1 || authorizer.releaseParams[0].ReservationID != 9900 {
		t.Fatalf("expected reservation 9900 to be released, got %#v", authorizer.releaseParams)
	}
	if len(requestLog.markRequestFailedArgs) != 1 || requestLog.markRequestFailedArgs[0].ErrorCode != "request_attempt_create_failed" {
		t.Fatalf("expected request_attempt_create_failed, got %#v", requestLog.markRequestFailedArgs)
	}
}

func TestChatCompletionServiceStreamChatCompletionReleasesAuthorizationWhenFallbackAdapterMissing(t *testing.T) {
	upstreamErr := errors.New("temporary stream upstream error")
	firstAdapter := &fakeChatAdapter{streamErr: upstreamErr}
	classifier := &fakeRetryClassifier{retryable: true}
	requestLog := newFakeRequestLogService()
	authorizer := &fakeChatAuthorizer{
		authorization: lifecycle.ChatAuthorization{ReservationID: 9901},
	}
	service := newChatCompletionServiceForTestWithAuthorizer(
		&fakeChatRouter{plan: routePlan(
			routeCandidate("openai-primary", 101, "gpt-4.1"),
			routeCandidate("missing-secondary", 102, "gpt-4.1"),
		)},
		&fakeAdapterRegistry{
			streamChatAdapters: map[string]openai.StreamChatAdapter{
				"openai-primary": firstAdapter,
			},
		},
		classifier,
		requestLog,
		newChatCompletionSettlementForTest(),
		authorizer,
	)

	err := service.StreamChatCompletion(contextWithPrincipal(42), chatRequest(), func(chunk gatewayapi.ChatCompletionStreamResponse) error {
		t.Fatalf("expected no stream chunk, got %#v", chunk)
		return nil
	})
	if err == nil {
		t.Fatal("expected missing fallback stream adapter error")
	}
	if firstAdapter.streamCalled != 1 {
		t.Fatalf("expected first stream adapter to be called once, got %d", firstAdapter.streamCalled)
	}
	if classifier.called != 1 {
		t.Fatalf("expected retry classifier to be called once, got %d", classifier.called)
	}
	if len(authorizer.releaseParams) != 1 {
		t.Fatalf("expected authorization release when fallback stream adapter is missing, got %d", len(authorizer.releaseParams))
	}
	if authorizer.releaseParams[0].ReservationID != 9901 {
		t.Fatalf("expected released reservation id %d, got %d", int64(9901), authorizer.releaseParams[0].ReservationID)
	}
	if len(requestLog.markRequestFailedArgs) != 1 {
		t.Fatalf("expected request to fail once, got %d", len(requestLog.markRequestFailedArgs))
	}
}

func TestChatCompletionServiceStreamChatCompletionReleasesAuthorizationOnNonRetryableAdapterError(t *testing.T) {
	upstreamErr := errors.New("invalid stream upstream request")
	firstAdapter := &fakeChatAdapter{streamErr: upstreamErr}
	secondAdapter := &fakeChatAdapter{
		streamResp: []openai.ChatStreamChunk{
			{Content: "should not fallback"},
			streamUsageChunk("gpt-4.1"),
		},
	}
	classifier := &fakeRetryClassifier{retryable: false}
	requestLog := newFakeRequestLogService()
	settlement := newChatCompletionSettlementForTest()
	authorizer := &fakeChatAuthorizer{
		authorization: lifecycle.ChatAuthorization{ReservationID: 9902},
	}
	service := newChatCompletionServiceForTestWithAuthorizer(
		&fakeChatRouter{plan: routePlan(
			routeCandidate("openai-primary", 101, "gpt-4.1"),
			routeCandidate("openai-secondary", 102, "gpt-4.1"),
		)},
		&fakeAdapterRegistry{
			streamChatAdapters: map[string]openai.StreamChatAdapter{
				"openai-primary":   firstAdapter,
				"openai-secondary": secondAdapter,
			},
		},
		classifier,
		requestLog,
		settlement,
		authorizer,
	)

	err := service.StreamChatCompletion(contextWithPrincipal(42), chatRequest(), func(chunk gatewayapi.ChatCompletionStreamResponse) error {
		t.Fatalf("expected no stream chunk after non-retryable error, got %#v", chunk)
		return nil
	})
	if !errors.Is(err, upstreamErr) {
		t.Fatalf("expected non-retryable stream adapter error, got %v", err)
	}
	if firstAdapter.streamCalled != 1 {
		t.Fatalf("expected first stream adapter to be called once, got %d", firstAdapter.streamCalled)
	}
	if secondAdapter.streamCalled != 0 {
		t.Fatalf("expected second stream adapter not to be called, got %d", secondAdapter.streamCalled)
	}
	if classifier.called != 1 {
		t.Fatalf("expected retry classifier to be called once, got %d", classifier.called)
	}
	if len(authorizer.releaseParams) != 1 {
		t.Fatalf("expected authorization release after non-retryable stream adapter error, got %d", len(authorizer.releaseParams))
	}
	if authorizer.releaseParams[0].ReservationID != 9902 {
		t.Fatalf("expected released reservation id %d, got %d", int64(9902), authorizer.releaseParams[0].ReservationID)
	}
	if len(settlement.params) != 0 {
		t.Fatalf("expected settlement not to be called, got %d", len(settlement.params))
	}
	if len(requestLog.markAttemptFailedArgs) != 1 {
		t.Fatalf("expected one failed attempt, got %d", len(requestLog.markAttemptFailedArgs))
	}
	if requestLog.markAttemptFailedArgs[0].ErrorCode != "stream_adapter_error" {
		t.Fatalf("expected attempt error code %q, got %q", "stream_adapter_error", requestLog.markAttemptFailedArgs[0].ErrorCode)
	}
	if len(requestLog.markRequestFailedArgs) != 1 {
		t.Fatalf("expected one failed request, got %d", len(requestLog.markRequestFailedArgs))
	}
	if requestLog.markRequestFailedArgs[0].ErrorCode != "stream_adapter_error" {
		t.Fatalf("expected request error code %q, got %q", "stream_adapter_error", requestLog.markRequestFailedArgs[0].ErrorCode)
	}
}

func TestChatCompletionServiceStreamChatCompletionFailsWithoutFinalUsage(t *testing.T) {
	fakeAdapter := &fakeChatAdapter{
		streamResp: []openai.ChatStreamChunk{
			{
				ID:      "chatcmpl_mock",
				Model:   "gpt-4.1",
				Role:    "assistant",
				Content: "content without final usage",
			},
		},
	}
	requestLog := newFakeRequestLogService()
	settlement := newChatCompletionSettlementForTest()
	authorizer := &fakeChatAuthorizer{
		authorization: lifecycle.ChatAuthorization{ReservationID: 8801},
	}
	service := newChatCompletionServiceForTestWithAuthorizer(
		&fakeChatRouter{plan: routePlan(routeCandidate("openai", 123, "gpt-4.1"))},
		&fakeAdapterRegistry{
			streamChatAdapters: map[string]openai.StreamChatAdapter{
				"openai": fakeAdapter,
			},
		},
		nil,
		requestLog,
		settlement,
		authorizer,
	)

	chunks := make([]gatewayapi.ChatCompletionStreamResponse, 0)
	err := service.StreamChatCompletion(contextWithPrincipal(42), chatRequest(), func(chunk gatewayapi.ChatCompletionStreamResponse) error {
		chunks = append(chunks, chunk)
		return nil
	})
	if err == nil {
		t.Fatal("expected missing final usage error")
	}
	if !strings.Contains(err.Error(), "stream final usage is missing") {
		t.Fatalf("expected missing final usage error, got %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("expected visible content chunk to be emitted, got %d chunks", len(chunks))
	}
	if len(settlement.params) != 0 {
		t.Fatalf("expected settlement not to run without final usage, got %d calls", len(settlement.params))
	}
	if len(authorizer.releaseParams) != 0 {
		t.Fatalf("expected no normal authorization release without final usage, got %d", len(authorizer.releaseParams))
	}
	if len(authorizer.releaseBillingExceptionParams) != 1 {
		t.Fatalf("expected billing exception release without final usage, got %d", len(authorizer.releaseBillingExceptionParams))
	}
	if authorizer.releaseBillingExceptionParams[0].ReservationID != 8801 {
		t.Fatalf("expected released reservation id %d, got %d", int64(8801), authorizer.releaseBillingExceptionParams[0].ReservationID)
	}
	if authorizer.releaseBillingExceptionParams[0].ReasonCode != "stream_final_usage_missing" {
		t.Fatalf("expected stream_final_usage_missing reason code, got %q", authorizer.releaseBillingExceptionParams[0].ReasonCode)
	}
	if len(requestLog.markAttemptFailedArgs) != 1 {
		t.Fatalf("expected attempt to fail once, got %d", len(requestLog.markAttemptFailedArgs))
	}
	if requestLog.markAttemptFailedArgs[0].ErrorCode != string(failure.CodeGatewayStreamUsageMissing) {
		t.Fatalf("expected attempt error code %q, got %q", failure.CodeGatewayStreamUsageMissing, requestLog.markAttemptFailedArgs[0].ErrorCode)
	}
	if len(requestLog.markRequestFailedArgs) != 1 {
		t.Fatalf("expected request to fail once, got %d", len(requestLog.markRequestFailedArgs))
	}
	if requestLog.markRequestFailedArgs[0].ErrorCode != string(failure.CodeGatewayStreamUsageMissing) {
		t.Fatalf("expected request error code %q, got %q", failure.CodeGatewayStreamUsageMissing, requestLog.markRequestFailedArgs[0].ErrorCode)
	}
	if len(requestLog.markAttemptSucceededArgs) != 0 {
		t.Fatalf("expected no direct attempt success, got %d", len(requestLog.markAttemptSucceededArgs))
	}
	if len(requestLog.markRequestSucceededArgs) != 0 {
		t.Fatalf("expected no direct request success, got %d", len(requestLog.markRequestSucceededArgs))
	}
}

func TestChatCompletionServiceStreamChatCompletionMarksRequestFailedOnSettlementError(t *testing.T) {
	settlementErr := errors.New("stream settlement failed")
	fakeAdapter := &fakeChatAdapter{
		streamResp: []openai.ChatStreamChunk{
			{
				ID:      "chatcmpl_mock",
				Model:   "gpt-4.1",
				Role:    "assistant",
				Content: "stream content",
			},
			streamUsageChunk("gpt-4.1"),
		},
	}
	requestLog := newFakeRequestLogService()
	settlement := &fakeChatSettlementExecutor{err: settlementErr}
	authorizer := &fakeChatAuthorizer{
		authorization: lifecycle.ChatAuthorization{ReservationID: 8830},
	}
	service := newChatCompletionServiceForTestWithAuthorizer(
		&fakeChatRouter{plan: routePlan(routeCandidate("openai", 123, "gpt-4.1"))},
		&fakeAdapterRegistry{
			streamChatAdapters: map[string]openai.StreamChatAdapter{
				"openai": fakeAdapter,
			},
		},
		nil,
		requestLog,
		settlement,
		authorizer,
	)

	err := service.StreamChatCompletion(contextWithPrincipal(42), chatRequest(), func(chunk gatewayapi.ChatCompletionStreamResponse) error {
		return nil
	})
	if !errors.Is(err, settlementErr) {
		t.Fatalf("expected settlement error, got %v", err)
	}
	if len(settlement.params) != 1 {
		t.Fatalf("expected settlement to be called once, got %d", len(settlement.params))
	}
	if settlement.params[0].Authorization.ReservationID != 8830 {
		t.Fatalf("expected settlement reservation id %d, got %d", int64(8830), settlement.params[0].Authorization.ReservationID)
	}
	if len(authorizer.releaseParams) != 0 {
		t.Fatalf("expected no authorization release when stream settlement fails, got %d", len(authorizer.releaseParams))
	}
	if len(requestLog.markRequestFailedArgs) != 1 {
		t.Fatalf("expected request to be marked failed once, got %d", len(requestLog.markRequestFailedArgs))
	}
	if requestLog.markRequestFailedArgs[0].ErrorCode != "stream_chat_settlement_failed" {
		t.Fatalf("expected stream_chat_settlement_failed, got %q", requestLog.markRequestFailedArgs[0].ErrorCode)
	}
	if len(requestLog.markAttemptSucceededArgs) != 0 {
		t.Fatalf("expected attempt success to be handled by settlement, got %d direct calls", len(requestLog.markAttemptSucceededArgs))
	}
	if len(requestLog.markRequestSucceededArgs) != 0 {
		t.Fatalf("expected request success to be handled by settlement, got %d direct calls", len(requestLog.markRequestSucceededArgs))
	}
}

func TestChatCompletionServiceStreamChatCompletionSettlesAfterFinalUsageWithAdapterError(t *testing.T) {
	upstreamErr := errors.New("stream tail error after usage")
	fakeAdapter := &fakeChatAdapter{
		streamResp: []openai.ChatStreamChunk{
			{
				ID:      "chatcmpl_mock",
				Model:   "gpt-4.1",
				Role:    "assistant",
				Content: "billable stream content",
			},
			streamUsageChunk("gpt-4.1"),
		},
		streamErr: upstreamErr,
	}
	requestLog := newFakeRequestLogService()
	settlement := newChatCompletionSettlementForTest()
	authorizer := &fakeChatAuthorizer{
		authorization: lifecycle.ChatAuthorization{ReservationID: 8840},
	}
	service := newChatCompletionServiceForTestWithAuthorizer(
		&fakeChatRouter{plan: routePlan(routeCandidate("openai", 123, "gpt-4.1"))},
		&fakeAdapterRegistry{
			streamChatAdapters: map[string]openai.StreamChatAdapter{
				"openai": fakeAdapter,
			},
		},
		nil,
		requestLog,
		settlement,
		authorizer,
	)

	chunks := make([]gatewayapi.ChatCompletionStreamResponse, 0)
	err := service.StreamChatCompletion(contextWithPrincipal(42), chatRequest(), func(chunk gatewayapi.ChatCompletionStreamResponse) error {
		chunks = append(chunks, chunk)
		return nil
	})
	if !errors.Is(err, upstreamErr) {
		t.Fatalf("expected original stream tail error, got %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("expected only visible content chunk to be emitted, got %d chunks", len(chunks))
	}
	if len(settlement.params) != 1 {
		t.Fatalf("expected settlement after final usage, got %d calls", len(settlement.params))
	}
	if v, ok := settlement.params[0].Facts.Usage.OutputTokensTotal.BillableValue(); !ok || v != 11 {
		t.Fatalf("expected settlement output tokens 11, got %d (ok=%v)", v, ok)
	}
	if settlement.params[0].Authorization.ReservationID != 8840 {
		t.Fatalf("expected settlement reservation id %d, got %d", int64(8840), settlement.params[0].Authorization.ReservationID)
	}
	if len(authorizer.releaseParams) != 0 {
		t.Fatalf("expected no authorization release after final usage settlement, got %d", len(authorizer.releaseParams))
	}
	if len(requestLog.markAttemptCanceledArgs) != 0 {
		t.Fatalf("expected no canceled attempt after final usage, got %d", len(requestLog.markAttemptCanceledArgs))
	}
	if len(requestLog.markRequestCanceledArgs) != 0 {
		t.Fatalf("expected no canceled request after final usage, got %d", len(requestLog.markRequestCanceledArgs))
	}
	if len(requestLog.markAttemptFailedArgs) != 0 {
		t.Fatalf("expected no direct failed attempt after final usage settlement, got %d", len(requestLog.markAttemptFailedArgs))
	}
	if len(requestLog.markRequestFailedArgs) != 0 {
		t.Fatalf("expected no direct failed request after final usage settlement, got %d", len(requestLog.markRequestFailedArgs))
	}
}

func TestChatCompletionServiceStreamChatCompletionSettlesAfterFinalUsageWithClientCancel(t *testing.T) {
	fakeAdapter := &fakeChatAdapter{
		streamResp: []openai.ChatStreamChunk{
			{
				ID:      "chatcmpl_mock",
				Model:   "gpt-4.1",
				Role:    "assistant",
				Content: "billable stream content",
			},
			streamUsageChunk("gpt-4.1"),
		},
		streamErr: context.Canceled,
	}
	classifier := &fakeRetryClassifier{retryable: true}
	requestLog := newFakeRequestLogService()
	settlement := newChatCompletionSettlementForTest()
	authorizer := &fakeChatAuthorizer{
		authorization: lifecycle.ChatAuthorization{ReservationID: 8850},
	}
	service := newChatCompletionServiceForTestWithAuthorizer(
		&fakeChatRouter{plan: routePlan(
			routeCandidate("openai-primary", 101, "gpt-4.1"),
			routeCandidate("openai-secondary", 102, "gpt-4.1"),
		)},
		&fakeAdapterRegistry{
			streamChatAdapters: map[string]openai.StreamChatAdapter{
				"openai-primary": fakeAdapter,
				"openai-secondary": &fakeChatAdapter{
					streamResp: []openai.ChatStreamChunk{
						{Content: "should not fallback"},
						streamUsageChunk("gpt-4.1"),
					},
				},
			},
		},
		classifier,
		requestLog,
		settlement,
		authorizer,
	)

	err := service.StreamChatCompletion(contextWithPrincipal(42), chatRequest(), func(chunk gatewayapi.ChatCompletionStreamResponse) error {
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected original context canceled error, got %v", err)
	}
	if classifier.called != 0 {
		t.Fatalf("expected retry classifier not to run after final usage, got %d calls", classifier.called)
	}
	if len(settlement.params) != 1 {
		t.Fatalf("expected settlement after final usage, got %d calls", len(settlement.params))
	}
	if settlement.params[0].AttemptRecord.ID != 1 {
		t.Fatalf("expected first attempt to be settled, got attempt id %d", settlement.params[0].AttemptRecord.ID)
	}
	if settlement.params[0].Authorization.ReservationID != 8850 {
		t.Fatalf("expected settlement reservation id %d, got %d", int64(8850), settlement.params[0].Authorization.ReservationID)
	}
	if len(authorizer.releaseParams) != 0 {
		t.Fatalf("expected no authorization release after final usage settlement, got %d", len(authorizer.releaseParams))
	}
	if len(requestLog.createAttempts) != 1 {
		t.Fatalf("expected no fallback attempt after final usage, got %d attempts", len(requestLog.createAttempts))
	}
	if len(requestLog.markAttemptCanceledArgs) != 0 {
		t.Fatalf("expected no canceled attempt after final usage, got %d", len(requestLog.markAttemptCanceledArgs))
	}
	if len(requestLog.markRequestCanceledArgs) != 0 {
		t.Fatalf("expected no canceled request after final usage, got %d", len(requestLog.markRequestCanceledArgs))
	}
	if len(requestLog.markAttemptFailedArgs) != 0 {
		t.Fatalf("expected no direct failed attempt after final usage settlement, got %d", len(requestLog.markAttemptFailedArgs))
	}
	if len(requestLog.markRequestFailedArgs) != 0 {
		t.Fatalf("expected no direct failed request after final usage settlement, got %d", len(requestLog.markRequestFailedArgs))
	}
}

func TestChatCompletionServiceStreamChatCompletionFallsBackBeforeFirstChunk(t *testing.T) {
	upstreamErr := errors.New("temporary stream upstream error")
	firstAdapter := &fakeChatAdapter{streamErr: upstreamErr}
	secondAdapter := &fakeChatAdapter{
		streamResp: []openai.ChatStreamChunk{
			{
				ID:      "chatcmpl_mock",
				Model:   "gpt-4.1",
				Role:    "assistant",
				Content: "fallback stream response",
			},
			streamUsageChunk("gpt-4.1"),
		},
	}
	classifier := &fakeRetryClassifier{retryable: true}
	requestLog := newFakeRequestLogService()
	settlement := newChatCompletionSettlementForTest()
	authorizer := &fakeChatAuthorizer{
		authorization: lifecycle.ChatAuthorization{ReservationID: 8860},
	}
	service := newChatCompletionServiceForTestWithAuthorizer(
		&fakeChatRouter{plan: routePlan(
			routeCandidate("openai-primary", 101, "gpt-4.1"),
			routeCandidate("openai-secondary", 102, "gpt-4.1"),
		)},
		&fakeAdapterRegistry{
			streamChatAdapters: map[string]openai.StreamChatAdapter{
				"openai-primary":   firstAdapter,
				"openai-secondary": secondAdapter,
			},
		},
		classifier,
		requestLog,
		settlement,
		authorizer,
	)

	chunks := make([]gatewayapi.ChatCompletionStreamResponse, 0)
	err := service.StreamChatCompletion(contextWithPrincipal(42), chatRequest(), func(chunk gatewayapi.ChatCompletionStreamResponse) error {
		chunks = append(chunks, chunk)
		return nil
	})
	if err != nil {
		t.Fatalf("StreamChatCompletion returned err: %v", err)
	}
	if firstAdapter.streamCalled != 1 {
		t.Fatalf("expected first stream adapter to be called once, got %d", firstAdapter.streamCalled)
	}
	if secondAdapter.streamCalled != 1 {
		t.Fatalf("expected second stream adapter to be called once, got %d", secondAdapter.streamCalled)
	}
	if classifier.called != 1 {
		t.Fatalf("expected retry classifier to be called once, got %d", classifier.called)
	}
	if len(chunks) != 1 {
		t.Fatalf("expected 1 stream chunk, got %d", len(chunks))
	}
	if chunks[0].Choices[0].Delta.Content != "fallback stream response" {
		t.Fatalf("expected fallback stream response, got %q", chunks[0].Choices[0].Delta.Content)
	}
	if len(requestLog.createAttempts) != 2 {
		t.Fatalf("expected two stream attempts, got %d", len(requestLog.createAttempts))
	}
	if requestLog.createAttempts[0].AttemptIndex != 0 || requestLog.createAttempts[1].AttemptIndex != 1 {
		t.Fatalf("expected attempt indexes 0 and 1, got %#v", requestLog.createAttempts)
	}
	if len(requestLog.markAttemptFailedArgs) != 1 {
		t.Fatalf("expected first attempt to fail once, got %d", len(requestLog.markAttemptFailedArgs))
	}
	if requestLog.markAttemptFailedArgs[0].ErrorCode != "stream_adapter_error" {
		t.Fatalf("expected stream_adapter_error, got %q", requestLog.markAttemptFailedArgs[0].ErrorCode)
	}
	if len(requestLog.markAttemptSucceededArgs) != 0 {
		t.Fatalf("expected fallback attempt success to be handled by settlement, got %d direct calls", len(requestLog.markAttemptSucceededArgs))
	}
	if len(requestLog.markRequestSucceededArgs) != 0 {
		t.Fatalf("expected request success to be handled by settlement, got %d direct calls", len(requestLog.markRequestSucceededArgs))
	}
	if len(settlement.params) != 1 {
		t.Fatalf("expected request to be settled once after fallback, got %d", len(settlement.params))
	}
	if settlement.params[0].AttemptRecord.ID != 2 {
		t.Fatalf("expected second attempt to be settled, got attempt id %d", settlement.params[0].AttemptRecord.ID)
	}
	if len(authorizer.authorizeParams) != 1 {
		t.Fatalf("expected one request-level authorization across stream fallback, got %d", len(authorizer.authorizeParams))
	}
	if settlement.params[0].Authorization.ReservationID != 8860 {
		t.Fatalf("expected settlement reservation id %d, got %d", int64(8860), settlement.params[0].Authorization.ReservationID)
	}
	if len(authorizer.releaseParams) != 0 {
		t.Fatalf("expected no authorization release after successful stream fallback settlement, got %d", len(authorizer.releaseParams))
	}
	if len(requestLog.markRequestFailedArgs) != 0 {
		t.Fatalf("expected request not to fail after successful fallback, got %#v", requestLog.markRequestFailedArgs)
	}
}

func TestChatCompletionServiceStreamChatCompletionDoesNotFallbackAfterFirstChunk(t *testing.T) {
	upstreamErr := errors.New("stream failed after first chunk")
	firstAdapter := &fakeChatAdapter{
		streamResp: []openai.ChatStreamChunk{
			{
				ID:      "chatcmpl_mock",
				Model:   "gpt-4.1",
				Role:    "assistant",
				Content: "partial",
			},
		},
		streamErr: upstreamErr,
	}
	secondAdapter := &fakeChatAdapter{
		streamResp: []openai.ChatStreamChunk{
			{
				ID:      "chatcmpl_mock",
				Model:   "gpt-4.1",
				Role:    "assistant",
				Content: "should not emit",
			},
		},
	}
	classifier := &fakeRetryClassifier{retryable: true}
	requestLog := newFakeRequestLogService()
	authorizer := &fakeChatAuthorizer{
		authorization: lifecycle.ChatAuthorization{ReservationID: 8870},
	}
	service := newChatCompletionServiceForTestWithAuthorizer(
		&fakeChatRouter{plan: routePlan(
			routeCandidate("openai-primary", 101, "gpt-4.1"),
			routeCandidate("openai-secondary", 102, "gpt-4.1"),
		)},
		&fakeAdapterRegistry{
			streamChatAdapters: map[string]openai.StreamChatAdapter{
				"openai-primary":   firstAdapter,
				"openai-secondary": secondAdapter,
			},
		},
		classifier,
		requestLog,
		newChatCompletionSettlementForTest(),
		authorizer,
	)

	chunks := make([]gatewayapi.ChatCompletionStreamResponse, 0)
	err := service.StreamChatCompletion(contextWithPrincipal(42), chatRequest(), func(chunk gatewayapi.ChatCompletionStreamResponse) error {
		chunks = append(chunks, chunk)
		return nil
	})
	if !errors.Is(err, upstreamErr) {
		t.Fatalf("expected stream error after first chunk, got %v", err)
	}
	if firstAdapter.streamCalled != 1 {
		t.Fatalf("expected first stream adapter to be called once, got %d", firstAdapter.streamCalled)
	}
	if secondAdapter.streamCalled != 0 {
		t.Fatalf("expected second stream adapter not to be called, got %d", secondAdapter.streamCalled)
	}
	if classifier.called != 0 {
		t.Fatalf("expected retry classifier not to be called after emit, got %d", classifier.called)
	}
	if len(chunks) != 1 || chunks[0].Choices[0].Delta.Content != "partial" {
		t.Fatalf("expected only partial chunk, got %#v", chunks)
	}
	if len(authorizer.releaseParams) != 0 {
		t.Fatalf("expected no normal authorization release after emitted stream error, got %d", len(authorizer.releaseParams))
	}
	if len(authorizer.releaseBillingExceptionParams) != 1 {
		t.Fatalf("expected billing exception release after emitted stream error without final usage, got %d", len(authorizer.releaseBillingExceptionParams))
	}
	if authorizer.releaseBillingExceptionParams[0].ReservationID != 8870 {
		t.Fatalf("expected released reservation id %d, got %d", int64(8870), authorizer.releaseBillingExceptionParams[0].ReservationID)
	}
	if authorizer.releaseBillingExceptionParams[0].ReasonCode != "stream_interrupted_without_final_usage" {
		t.Fatalf("expected stream_interrupted_without_final_usage reason code, got %q", authorizer.releaseBillingExceptionParams[0].ReasonCode)
	}
	if len(requestLog.createAttempts) != 1 {
		t.Fatalf("expected only first attempt to be created, got %d", len(requestLog.createAttempts))
	}
	if len(requestLog.markAttemptFailedArgs) != 1 {
		t.Fatalf("expected first attempt to fail once, got %d", len(requestLog.markAttemptFailedArgs))
	}
	if requestLog.markAttemptFailedArgs[0].ErrorCode != "stream_adapter_error" {
		t.Fatalf("expected attempt error code %q, got %q", "stream_adapter_error", requestLog.markAttemptFailedArgs[0].ErrorCode)
	}
	if len(requestLog.markRequestFailedArgs) != 1 {
		t.Fatalf("expected request to fail once after emitted stream error, got %d", len(requestLog.markRequestFailedArgs))
	}
	if requestLog.markRequestFailedArgs[0].ErrorCode != "stream_adapter_error_after_emit" {
		t.Fatalf("expected request error code %q, got %q", "stream_adapter_error_after_emit", requestLog.markRequestFailedArgs[0].ErrorCode)
	}
	if len(requestLog.markAttemptSucceededArgs) != 0 {
		t.Fatalf("expected no succeeded attempt after emitted stream error, got %d", len(requestLog.markAttemptSucceededArgs))
	}
	if len(requestLog.markRequestSucceededArgs) != 0 {
		t.Fatalf("expected no succeeded request after emitted stream error, got %d", len(requestLog.markRequestSucceededArgs))
	}
}

func TestChatCompletionServiceStreamChatCompletionMarksCanceledWithoutFallback(t *testing.T) {
	firstAdapter := &fakeChatAdapter{streamErr: context.Canceled}
	secondAdapter := &fakeChatAdapter{
		streamResp: []openai.ChatStreamChunk{
			{
				ID:      "chatcmpl_mock",
				Model:   "gpt-4.1",
				Role:    "assistant",
				Content: "should not emit",
			},
		},
	}
	classifier := &fakeRetryClassifier{retryable: true}
	requestLog := newFakeRequestLogService()
	authorizer := &fakeChatAuthorizer{
		authorization: lifecycle.ChatAuthorization{ReservationID: 8880},
	}
	service := newChatCompletionServiceForTestWithAuthorizer(
		&fakeChatRouter{plan: routePlan(
			routeCandidate("openai-primary", 101, "gpt-4.1"),
			routeCandidate("openai-secondary", 102, "gpt-4.1"),
		)},
		&fakeAdapterRegistry{
			streamChatAdapters: map[string]openai.StreamChatAdapter{
				"openai-primary":   firstAdapter,
				"openai-secondary": secondAdapter,
			},
		},
		classifier,
		requestLog,
		newChatCompletionSettlementForTest(),
		authorizer,
	)

	err := service.StreamChatCompletion(contextWithPrincipal(42), chatRequest(), func(chunk gatewayapi.ChatCompletionStreamResponse) error {
		t.Fatalf("expected no stream chunk after client cancel, got %#v", chunk)
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled error, got %v", err)
	}
	if firstAdapter.streamCalled != 1 {
		t.Fatalf("expected first stream adapter to be called once, got %d", firstAdapter.streamCalled)
	}
	if secondAdapter.streamCalled != 0 {
		t.Fatalf("expected second stream adapter not to be called, got %d", secondAdapter.streamCalled)
	}
	if classifier.called != 0 {
		t.Fatalf("expected retry classifier not to be called after client cancel, got %d", classifier.called)
	}
	if len(authorizer.releaseParams) != 0 {
		t.Fatalf("expected no normal authorization release after stream client cancel without final usage, got %d", len(authorizer.releaseParams))
	}
	if len(authorizer.releaseBillingExceptionParams) != 1 {
		t.Fatalf("expected billing exception release after stream client cancel without final usage, got %d", len(authorizer.releaseBillingExceptionParams))
	}
	if authorizer.releaseBillingExceptionParams[0].ReservationID != 8880 {
		t.Fatalf("expected released reservation id %d, got %d", int64(8880), authorizer.releaseBillingExceptionParams[0].ReservationID)
	}
	if authorizer.releaseBillingExceptionParams[0].ReasonCode != "stream_client_canceled_without_final_usage" {
		t.Fatalf("expected stream_client_canceled_without_final_usage reason code, got %q", authorizer.releaseBillingExceptionParams[0].ReasonCode)
	}
	if len(requestLog.markAttemptCanceledArgs) != 1 {
		t.Fatalf("expected 1 attempt canceled call, got %d", len(requestLog.markAttemptCanceledArgs))
	}
	if requestLog.markAttemptCanceledArgs[0].ErrorCode != "client_canceled" {
		t.Fatalf("expected attempt canceled code %q, got %q", "client_canceled", requestLog.markAttemptCanceledArgs[0].ErrorCode)
	}
	if len(requestLog.markRequestCanceledArgs) != 1 {
		t.Fatalf("expected 1 request canceled call, got %d", len(requestLog.markRequestCanceledArgs))
	}
	if requestLog.markRequestCanceledArgs[0].ErrorCode != "client_canceled" {
		t.Fatalf("expected request canceled code %q, got %q", "client_canceled", requestLog.markRequestCanceledArgs[0].ErrorCode)
	}
	if len(requestLog.markAttemptFailedArgs) != 0 {
		t.Fatalf("expected no attempt failed call, got %d", len(requestLog.markAttemptFailedArgs))
	}
	if len(requestLog.markRequestFailedArgs) != 0 {
		t.Fatalf("expected no request failed call, got %d", len(requestLog.markRequestFailedArgs))
	}
}
