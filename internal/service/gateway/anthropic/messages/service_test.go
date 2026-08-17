package messages

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	gatewayapi "github.com/ThankCat/unio-gateway/internal/app/gatewayapi/anthropic/messages"
	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	messagesadapter "github.com/ThankCat/unio-gateway/internal/core/adapter/anthropic/messages"
	"github.com/ThankCat/unio-gateway/internal/core/auth"
	"github.com/ThankCat/unio-gateway/internal/core/channel"
	"github.com/ThankCat/unio-gateway/internal/core/requestlog"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	coreusage "github.com/ThankCat/unio-gateway/internal/core/usage"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/lifecycle"
)

// fakeMessagesRouter 是 messages 测试使用的 routing 替身。
type fakeMessagesRouter struct {
	req         routing.ChatRouteRequest
	plan        routing.ChatRoutePlan
	err         error
	validateErr error
}

func (r *fakeMessagesRouter) ValidateChat(ctx context.Context, req routing.ChatRouteRequest) error {
	r.req = req
	return r.validateErr
}

func (r *fakeMessagesRouter) PlanChat(ctx context.Context, req routing.ChatRouteRequest) (routing.ChatRoutePlan, error) {
	r.req = req
	return r.plan, r.err
}

// fakeMessagesRegistry 是 messages 测试使用的 adapter registry 替身。
type fakeMessagesRegistry struct {
	messages       map[string]messagesadapter.MessagesAdapter
	streamMessages map[string]messagesadapter.StreamMessagesAdapter
	tokenizers     map[string]messagesadapter.MessagesInputTokenizer
}

func (r *fakeMessagesRegistry) Messages(adapterKey string) (messagesadapter.MessagesAdapter, bool) {
	a, ok := r.messages[adapterKey]
	return a, ok
}

func (r *fakeMessagesRegistry) StreamMessages(adapterKey string) (messagesadapter.StreamMessagesAdapter, bool) {
	a, ok := r.streamMessages[adapterKey]
	return a, ok
}

func (r *fakeMessagesRegistry) MessagesInputTokenizer(adapterKey string) (messagesadapter.MessagesInputTokenizer, bool) {
	t, ok := r.tokenizers[adapterKey]
	return t, ok
}

// fakeMessagesAdapter 同时实现非流式、流式与 tokenizer 能力。
type fakeMessagesAdapter struct {
	messagesCalled int
	messagesReq    messagesadapter.MessageRequest
	messagesResp   *messagesadapter.MessageResponse
	messagesErr    error

	streamCalled  int
	streamReq     messagesadapter.MessageRequest
	streamEvents  []messagesadapter.MessageStreamEvent
	streamOutcome *adapter.StreamOutcome
	streamErr     error
}

func (a *fakeMessagesAdapter) Messages(ctx context.Context, ch channel.Runtime, req messagesadapter.MessageRequest) (*messagesadapter.MessageResponse, error) {
	a.messagesCalled++
	a.messagesReq = req
	return a.messagesResp, a.messagesErr
}

func (a *fakeMessagesAdapter) StreamMessages(ctx context.Context, ch channel.Runtime, req messagesadapter.MessageRequest, emit func(messagesadapter.MessageStreamEvent) error) (adapter.StreamOutcome, error) {
	a.streamCalled++
	a.streamReq = req
	adapter.MarkTransportStarted(ctx)

	for _, ev := range a.streamEvents {
		if messagesadapter.FirstTokenPayload(ev) != "" {
			adapter.MarkFirstTokenEligible(ctx)
		}
		if err := emit(ev); err != nil {
			return adapter.StreamOutcome{}, err
		}
	}

	if a.streamOutcome != nil {
		return *a.streamOutcome, a.streamErr
	}
	return adapter.StreamOutcome{}, a.streamErr
}

func (a *fakeMessagesAdapter) CountMessagesInputTokens(req messagesadapter.MessagesInputTokenizeRequest) (int64, error) {
	return 1, nil
}

// fakeMessagesRequestLog 是 messages 测试使用的 requestlog 替身。
type fakeMessagesRequestLog struct {
	nextRequestID int64
	nextAttemptID int64

	createRequests        []requestlog.CreateRequestParams
	markRequestRunning    []int64
	markRequestFailedArgs []requestlog.MarkRequestFailedParams
	markRequestCanceled   []requestlog.MarkRequestCanceledParams
	deliveryStarted       []int64
	deliveryCompleted     []int64
	deliveryInterrupted   []int64
	createAttempts        []requestlog.CreateAttemptParams
	capabilityResults     []string
}

func (s *fakeMessagesRequestLog) SetCapabilityCheckResult(_ context.Context, _ int64, result string) error {
	s.capabilityResults = append(s.capabilityResults, result)
	return nil
}

func newFakeMessagesRequestLog() *fakeMessagesRequestLog {
	return &fakeMessagesRequestLog{nextRequestID: 1, nextAttemptID: 1}
}

func (s *fakeMessagesRequestLog) CreateRequest(ctx context.Context, params requestlog.CreateRequestParams) (requestlog.RequestRecord, error) {
	s.createRequests = append(s.createRequests, params)
	id := s.nextRequestID
	s.nextRequestID++
	return requestlog.RequestRecord{ID: id, RequestID: params.RequestID, UserID: params.UserID, APIKeyID: params.APIKeyID, Status: requestlog.RequestStatusPending}, nil
}

func (s *fakeMessagesRequestLog) MarkRequestRunning(ctx context.Context, id int64) (requestlog.RequestRecord, error) {
	s.markRequestRunning = append(s.markRequestRunning, id)
	return requestlog.RequestRecord{ID: id, Status: requestlog.RequestStatusRunning}, nil
}

// MarkRequestDeliveryStarted 只推进 delivery 状态；Gateway 首字是独立事实，由 MarkRequestGatewayFirstToken 记录。
func (s *fakeMessagesRequestLog) MarkRequestDeliveryStarted(_ context.Context, id int64) (requestlog.RequestRecord, error) {
	s.deliveryStarted = append(s.deliveryStarted, id)
	return requestlog.RequestRecord{ID: id, DeliveryStatus: requestlog.DeliveryStatusInProgress}, nil
}

func (s *fakeMessagesRequestLog) MarkRequestGatewayFirstToken(ctx context.Context, params requestlog.MarkGatewayFirstTokenParams) (requestlog.RequestRecord, error) {
	return requestlog.RequestRecord{ID: params.ID, Status: requestlog.RequestStatusRunning, GatewayFirstTokenAt: &params.GatewayFirstTokenAt}, nil
}

func (s *fakeMessagesRequestLog) MarkRequestDeliveryCompleted(_ context.Context, id int64, completedAt time.Time) (requestlog.RequestRecord, error) {
	s.deliveryCompleted = append(s.deliveryCompleted, id)
	return requestlog.RequestRecord{ID: id, DeliveryStatus: requestlog.DeliveryStatusCompleted, ResponseCompletedAt: &completedAt}, nil
}

func (s *fakeMessagesRequestLog) MarkRequestDeliveryInterrupted(_ context.Context, id int64) (requestlog.RequestRecord, error) {
	s.deliveryInterrupted = append(s.deliveryInterrupted, id)
	return requestlog.RequestRecord{ID: id, DeliveryStatus: requestlog.DeliveryStatusInterrupted}, nil
}

func (s *fakeMessagesRequestLog) MarkRequestSucceeded(ctx context.Context, params requestlog.MarkRequestSucceededParams) (requestlog.RequestRecord, error) {
	return requestlog.RequestRecord{ID: params.ID, Status: requestlog.RequestStatusSucceeded}, nil
}

func (s *fakeMessagesRequestLog) MarkSettledRequestFailed(ctx context.Context, params requestlog.MarkSettledRequestFailedParams) (requestlog.RequestRecord, error) {
	return requestlog.RequestRecord{ID: params.ID, Status: requestlog.RequestStatusFailed}, nil
}

func (s *fakeMessagesRequestLog) MarkSettledRequestCanceled(ctx context.Context, params requestlog.MarkSettledRequestCanceledParams) (requestlog.RequestRecord, error) {
	return requestlog.RequestRecord{ID: params.ID, Status: requestlog.RequestStatusCanceled}, nil
}

func (s *fakeMessagesRequestLog) MarkRequestFailed(ctx context.Context, params requestlog.MarkRequestFailedParams) (requestlog.RequestRecord, error) {
	s.markRequestFailedArgs = append(s.markRequestFailedArgs, params)
	return requestlog.RequestRecord{ID: params.ID, Status: requestlog.RequestStatusFailed}, nil
}

func (s *fakeMessagesRequestLog) MarkRequestCanceled(ctx context.Context, params requestlog.MarkRequestCanceledParams) (requestlog.RequestRecord, error) {
	s.markRequestCanceled = append(s.markRequestCanceled, params)
	return requestlog.RequestRecord{ID: params.ID, Status: requestlog.RequestStatusCanceled}, nil
}

func (s *fakeMessagesRequestLog) CreateAttempt(ctx context.Context, params requestlog.CreateAttemptParams) (requestlog.AttemptRecord, error) {
	s.createAttempts = append(s.createAttempts, params)
	id := s.nextAttemptID
	s.nextAttemptID++
	return requestlog.AttemptRecord{ID: id, RequestRecordID: params.RequestRecordID, AttemptIndex: params.AttemptIndex, ProviderID: params.ProviderID, ChannelID: params.ChannelID, AdapterKey: params.AdapterKey, UpstreamModel: params.UpstreamModel, Status: requestlog.AttemptStatusRunning}, nil
}

func (s *fakeMessagesRequestLog) MarkAttemptSucceeded(ctx context.Context, params requestlog.MarkAttemptSucceededParams) (requestlog.AttemptRecord, error) {
	return requestlog.AttemptRecord{ID: params.ID, Status: requestlog.AttemptStatusSucceeded}, nil
}

func (s *fakeMessagesRequestLog) MarkSettledAttemptFailed(ctx context.Context, params requestlog.MarkSettledAttemptFailedParams) (requestlog.AttemptRecord, error) {
	return requestlog.AttemptRecord{ID: params.ID, Status: requestlog.AttemptStatusFailed}, nil
}

func (s *fakeMessagesRequestLog) MarkSettledAttemptCanceled(ctx context.Context, params requestlog.MarkSettledAttemptCanceledParams) (requestlog.AttemptRecord, error) {
	return requestlog.AttemptRecord{ID: params.ID, Status: requestlog.AttemptStatusCanceled}, nil
}

func (s *fakeMessagesRequestLog) MarkAttemptGatewayFirstToken(ctx context.Context, params requestlog.MarkAttemptGatewayFirstTokenParams) (requestlog.AttemptRecord, error) {
	return requestlog.AttemptRecord{ID: params.ID, Status: requestlog.AttemptStatusRunning, GatewayFirstTokenAt: &params.GatewayFirstTokenAt}, nil
}

func (s *fakeMessagesRequestLog) MarkAttemptFailed(ctx context.Context, params requestlog.MarkAttemptFailedParams) (requestlog.AttemptRecord, error) {
	return requestlog.AttemptRecord{ID: params.ID, Status: requestlog.AttemptStatusFailed}, nil
}

func (s *fakeMessagesRequestLog) MarkAttemptCanceled(ctx context.Context, params requestlog.MarkAttemptCanceledParams) (requestlog.AttemptRecord, error) {
	return requestlog.AttemptRecord{ID: params.ID, Status: requestlog.AttemptStatusCanceled}, nil
}

// fakeMessagesSettlement 是 messages 测试使用的结算替身。
type fakeMessagesSettlement struct {
	params []lifecycle.ChatSettlementParams
	err    error
}

func (s *fakeMessagesSettlement) SettleSuccessfulChat(ctx context.Context, params lifecycle.ChatSettlementParams) error {
	s.params = append(s.params, params)
	return s.err
}

// fakeMessagesAuthorizer 是 messages 测试使用的授权替身。
type fakeMessagesAuthorizer struct {
	authorizeParams               []lifecycle.ChatAuthorizeNewRequestParams
	releaseParams                 []lifecycle.ChatReleaseAuthorizationParams
	releaseBillingExceptionParams []lifecycle.ChatReleaseBillingExceptionParams
	authorization                 lifecycle.ChatAuthorization
	authorizeErr                  error
}

func (a *fakeMessagesAuthorizer) AuthorizeNewChat(_ context.Context, params lifecycle.ChatAuthorizeNewRequestParams) (lifecycle.ChatAuthorizedRequest, error) {
	a.authorizeParams = append(a.authorizeParams, params)
	if a.authorizeErr != nil {
		return lifecycle.ChatAuthorizedRequest{}, a.authorizeErr
	}

	requestRecord := requestlog.RequestRecord{
		ID:                   int64(len(a.authorizeParams)),
		RequestID:            params.Request.RequestID,
		UserID:               params.Request.UserID,
		APIKeyID:             params.Request.APIKeyID,
		RequestedModelID:     params.Request.RequestedModelID,
		IngressProtocol:      params.Request.IngressProtocol,
		Endpoint:             params.Request.Endpoint,
		Stream:               params.Request.Stream,
		Status:               requestlog.RequestStatusRunning,
		DeliveryStatus:       requestlog.DeliveryStatusNotStarted,
		StartedAt:            params.Request.StartedAt,
		RequestedServiceTier: params.Request.RequestedServiceTier,
	}
	authorization := a.authorization
	if authorization.ReservationID == 0 {
		authorization.ReservationID = 7000 + int64(len(a.authorizeParams))
	}
	authorization.RequestRecordID = requestRecord.ID
	if authorization.Currency == "" {
		authorization.Currency = "USD"
	}
	return lifecycle.ChatAuthorizedRequest{
		RequestRecord: requestRecord,
		Authorization: authorization,
	}, nil
}

func (a *fakeMessagesAuthorizer) ReleaseChat(ctx context.Context, params lifecycle.ChatReleaseAuthorizationParams) error {
	a.releaseParams = append(a.releaseParams, params)
	return nil
}

func (a *fakeMessagesAuthorizer) ReleaseChatForBillingException(ctx context.Context, params lifecycle.ChatReleaseBillingExceptionParams) error {
	a.releaseBillingExceptionParams = append(a.releaseBillingExceptionParams, params)
	return nil
}

// passthroughCandidatePreparer 保留 routing 顺序并提供固定估算，聚焦协议编排行为。
type passthroughCandidatePreparer struct {
	inputTokens int64
}

func (p passthroughCandidatePreparer) PrepareCandidates(_ context.Context, params lifecycle.PrepareCandidatesParams) (lifecycle.CandidatePlan, error) {
	plan := lifecycle.CandidatePlan{
		Candidates:              make([]lifecycle.Candidate, 0, len(params.Candidates)),
		ConservativeInputTokens: p.inputTokens,
	}
	for index, candidate := range params.Candidates {
		plan.Candidates = append(plan.Candidates, lifecycle.Candidate{RouteIndex: index, Route: candidate})
	}
	return plan, nil
}

func contextWithPrincipal(userID int64) context.Context {
	ctx := httpx.ContextWithRequestID(context.Background(), "messages-test-request-id")
	return auth.ContextWithAPIKeyPrincipal(ctx, &auth.APIKeyPrincipal{
		APIKeyID:  1,
		UserID:    userID,
		KeyPrefix: "unio_sk_test",
	})
}

func messageRequest() gatewayapi.MessageRequest {
	maxTokens := 1024
	return gatewayapi.MessageRequest{
		Model:     "anthropic/claude-sonnet-4",
		MaxTokens: &maxTokens,
		Messages:  []gatewayapi.Message{{Role: "user", Content: json.RawMessage(`"hello"`)}},
	}
}

func routePlan(candidates ...routing.ChatRouteCandidate) routing.ChatRoutePlan {
	return routing.ChatRoutePlan{RequestedModel: "anthropic/claude-sonnet-4", Candidates: candidates}
}

func routeCandidate(adapterKey string, channelID int64, upstreamModel string) routing.ChatRouteCandidate {
	return routing.ChatRouteCandidate{
		ModelDBID:  1000 + channelID,
		ProviderID: 9000 + channelID,
		AdapterKey: adapterKey,
		Channel: channel.Runtime{
			ID:              channelID,
			Origin:          "https://example.test",
			APIKey:          "test-secret",
			ResponseTimeout: 30 * time.Second,
		},
		UpstreamModel: upstreamModel,
	}
}

func messageResponse() *messagesadapter.MessageResponse {
	usage := messagesadapter.MessageUsage{InputTokens: 10, OutputTokens: 11}
	metadata := adapter.UpstreamMetadata{StatusCode: 200, RequestID: "req-msg-1"}
	stopReason := "end_turn"
	return &messagesadapter.MessageResponse{
		ID:         "msg_provider_test",
		Model:      "deepseek-v4-flash",
		Role:       "assistant",
		Content:    []json.RawMessage{json.RawMessage(`{"type":"text","text":"hi there"}`)},
		StopReason: &stopReason,
		Usage:      usage,
		Upstream:   metadata,
		Facts: adapter.ResponseFacts{
			UpstreamProtocol:    "anthropic",
			UpstreamResponseID:  "msg_provider_test",
			UpstreamModel:       "deepseek-v4-flash",
			Finish:              adapter.FinishFacts{Class: adapter.FinishStop, RawReason: "end_turn"},
			Usage:               usage.ToUsageFacts(),
			UsageSource:         coreusage.SourceUpstreamResponse,
			UsageMappingVersion: "messagesadapter.v1",
			Metadata:            metadata,
		},
	}
}

func newMessagesServiceForTest(router MessagesRouter, registry AdapterRegistry, settlement lifecycle.ChatSettlementExecutor, authorizer lifecycle.ChatAuthorizer) *MessagesService {
	return NewMessagesService(
		router,
		registry,
		passthroughCandidatePreparer{inputTokens: 1},
		lifecycle.NeverRetryClassifier{},
		newFakeMessagesRequestLog(),
		settlement,
		authorizer,
		nil,
	)
}

func TestMessageAuthorizationFailedBeforePersistenceOrUpstream(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stream bool
	}{
		{name: "non-stream"},
		{name: "stream", stream: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			adapterFake := &fakeMessagesAdapter{messagesResp: messageResponse()}
			registry := &fakeMessagesRegistry{
				tokenizers: map[string]messagesadapter.MessagesInputTokenizer{"deepseek": adapterFake},
			}
			if tc.stream {
				registry.streamMessages = map[string]messagesadapter.StreamMessagesAdapter{"deepseek": adapterFake}
			} else {
				registry.messages = map[string]messagesadapter.MessagesAdapter{"deepseek": adapterFake}
			}

			authorizeErr := errors.New("authorize messages boom")
			authorizer := &fakeMessagesAuthorizer{authorizeErr: authorizeErr}
			settlement := &fakeMessagesSettlement{}
			service := newMessagesServiceForTest(
				&fakeMessagesRouter{plan: routePlan(routeCandidate("deepseek", 123, "deepseek-v4-flash"))},
				registry,
				settlement,
				authorizer,
			)
			requestLog := service.requestLog.(*fakeMessagesRequestLog)

			emitCalls := 0
			var err error
			if tc.stream {
				err = service.StreamMessage(contextWithPrincipal(42), messageRequest(), func(gatewayapi.StreamFrame) error {
					emitCalls++
					return nil
				})
			} else {
				_, err = service.CreateMessage(contextWithPrincipal(42), messageRequest())
			}

			if !errors.Is(err, authorizeErr) {
				t.Fatalf("expected authorization error, got %v", err)
			}
			if adapterFake.messagesCalled != 0 || adapterFake.streamCalled != 0 || emitCalls != 0 {
				t.Fatalf("authorization rejection reached downstream: messages=%d stream=%d emit=%d",
					adapterFake.messagesCalled, adapterFake.streamCalled, emitCalls)
			}
			if len(requestLog.createRequests) != 0 || len(requestLog.markRequestRunning) != 0 || len(requestLog.markRequestFailedArgs) != 0 {
				t.Fatalf("authorization rejection persisted a request: create=%d running=%d failed=%+v",
					len(requestLog.createRequests), len(requestLog.markRequestRunning), requestLog.markRequestFailedArgs)
			}
			if len(requestLog.createAttempts) != 0 {
				t.Fatalf("authorization rejection created an upstream attempt: %+v", requestLog.createAttempts)
			}
			if len(settlement.params) != 0 || len(authorizer.releaseParams) != 0 || len(authorizer.releaseBillingExceptionParams) != 0 {
				t.Fatalf("authorization rejection must not settle or release: settlements=%d releases=%d billing_exceptions=%d",
					len(settlement.params), len(authorizer.releaseParams), len(authorizer.releaseBillingExceptionParams))
			}
		})
	}
}

func TestCreateMessageReturnsResponseAndSettlesWithAnthropicFacts(t *testing.T) {
	adapterFake := &fakeMessagesAdapter{messagesResp: messageResponse()}
	registry := &fakeMessagesRegistry{
		messages:   map[string]messagesadapter.MessagesAdapter{"deepseek": adapterFake},
		tokenizers: map[string]messagesadapter.MessagesInputTokenizer{"deepseek": adapterFake},
	}
	settlement := &fakeMessagesSettlement{}
	authorizer := &fakeMessagesAuthorizer{}
	service := newMessagesServiceForTest(
		&fakeMessagesRouter{plan: routePlan(routeCandidate("deepseek", 123, "deepseek-v4-flash"))},
		registry,
		settlement,
		authorizer,
	)

	result, err := service.CreateMessage(contextWithPrincipal(42), messageRequest())
	if err != nil {
		t.Fatalf("CreateMessage returned err: %v", err)
	}
	resp := result.Response
	if len(service.requestLog.(*fakeMessagesRequestLog).deliveryCompleted) != 0 || len(service.requestLog.(*fakeMessagesRequestLog).deliveryInterrupted) != 0 {
		t.Fatal("delivery must stay not_started before the handler write")
	}
	if err := result.FinalizeDelivery(func(*gatewayapi.MessageResponse) error { return nil }); err != nil {
		t.Fatalf("finalize delivery: %v", err)
	}
	if len(service.requestLog.(*fakeMessagesRequestLog).deliveryCompleted) != 1 {
		t.Fatal("expected one completed delivery")
	}
	if resp == nil || resp.Type != "message" || resp.Role != "assistant" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	// gateway 必须把上游响应里的 upstream model 还原为客户 catalog model。
	if resp.Model != "anthropic/claude-sonnet-4" {
		t.Fatalf("expected catalog model echoed back, got %q", resp.Model)
	}
	if len(resp.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(resp.Content))
	}

	if len(settlement.params) != 1 {
		t.Fatalf("expected one settlement attempt, got %d", len(settlement.params))
	}
	settled := settlement.params[0]
	if settled.GatewayFirstTokenAt != nil {
		t.Fatalf("non-stream gateway_first_token_at = %v, want nil", settled.GatewayFirstTokenAt)
	}
	if settled.ResponseProtocol != requestlog.ProtocolAnthropic {
		t.Fatalf("expected anthropic settlement protocol, got %q", settled.ResponseProtocol)
	}
	if settled.Facts.UsageSource != coreusage.SourceUpstreamResponse {
		t.Fatalf("expected upstream_response usage source, got %q", settled.Facts.UsageSource)
	}
	if settled.ResponseModelID != "anthropic/claude-sonnet-4" {
		t.Fatalf("expected catalog model in settlement, got %q", settled.ResponseModelID)
	}
}

// TestCreateMessageReleasesAuthorizationOnPermanentSettlementFailure 验证上游成功但 settlement
// 永久失败且无 recovery job 接管时，必须释放冻结余额并记账务异常风险，避免用户余额被永久冻结。
func TestCreateMessageReleasesAuthorizationOnPermanentSettlementFailure(t *testing.T) {
	settlementErr := errors.New("messages settlement commit failed")
	adapterFake := &fakeMessagesAdapter{messagesResp: messageResponse()}
	registry := &fakeMessagesRegistry{
		messages:   map[string]messagesadapter.MessagesAdapter{"deepseek": adapterFake},
		tokenizers: map[string]messagesadapter.MessagesInputTokenizer{"deepseek": adapterFake},
	}
	settlement := &fakeMessagesSettlement{err: settlementErr}
	authorizer := &fakeMessagesAuthorizer{}
	service := newMessagesServiceForTest(
		&fakeMessagesRouter{plan: routePlan(routeCandidate("deepseek", 123, "deepseek-v4-flash"))},
		registry,
		settlement,
		authorizer,
	)

	_, err := service.CreateMessage(contextWithPrincipal(42), messageRequest())
	if !errors.Is(err, settlementErr) {
		t.Fatalf("expected settlement error, got %v", err)
	}
	if len(settlement.params) != 1 {
		t.Fatalf("expected one settlement attempt, got %d", len(settlement.params))
	}
	if len(authorizer.releaseParams) != 0 {
		t.Fatalf("expected no normal release on permanent settlement failure, got %d", len(authorizer.releaseParams))
	}
	if len(authorizer.releaseBillingExceptionParams) != 1 {
		t.Fatalf("expected billing exception release on permanent settlement failure, got %d", len(authorizer.releaseBillingExceptionParams))
	}
	if authorizer.releaseBillingExceptionParams[0].ReasonCode != "messages_settlement_failed_after_upstream_success" {
		t.Fatalf("expected messages_settlement_failed_after_upstream_success reason code, got %q", authorizer.releaseBillingExceptionParams[0].ReasonCode)
	}
}

func TestCreateMessageRoutesWithAnthropicIngressProtocol(t *testing.T) {
	router := &fakeMessagesRouter{plan: routePlan(routeCandidate("deepseek", 123, "deepseek-v4-flash"))}
	adapterFake := &fakeMessagesAdapter{messagesResp: messageResponse()}
	registry := &fakeMessagesRegistry{
		messages:   map[string]messagesadapter.MessagesAdapter{"deepseek": adapterFake},
		tokenizers: map[string]messagesadapter.MessagesInputTokenizer{"deepseek": adapterFake},
	}
	service := newMessagesServiceForTest(router, registry, &fakeMessagesSettlement{}, &fakeMessagesAuthorizer{})

	if _, err := service.CreateMessage(contextWithPrincipal(42), messageRequest()); err != nil {
		t.Fatalf("CreateMessage returned err: %v", err)
	}

	if router.req.IngressProtocol != routing.ProtocolAnthropic {
		t.Fatalf("expected anthropic ingress protocol, got %q", router.req.IngressProtocol)
	}
}

func TestCreateMessageReleasesAuthorizationOnNonRetryableAdapterError(t *testing.T) {
	adapterFake := &fakeMessagesAdapter{messagesErr: errors.New("upstream boom")}
	registry := &fakeMessagesRegistry{
		messages:   map[string]messagesadapter.MessagesAdapter{"deepseek": adapterFake},
		tokenizers: map[string]messagesadapter.MessagesInputTokenizer{"deepseek": adapterFake},
	}
	settlement := &fakeMessagesSettlement{}
	authorizer := &fakeMessagesAuthorizer{}
	service := newMessagesServiceForTest(
		&fakeMessagesRouter{plan: routePlan(routeCandidate("deepseek", 123, "deepseek-v4-flash"))},
		registry,
		settlement,
		authorizer,
	)

	_, err := service.CreateMessage(contextWithPrincipal(42), messageRequest())
	if err == nil {
		t.Fatal("expected adapter error")
	}
	if len(settlement.params) != 0 {
		t.Fatalf("expected no settlement on adapter error, got %d", len(settlement.params))
	}
	if len(authorizer.releaseParams) != 1 {
		t.Fatalf("expected authorization released once, got %d", len(authorizer.releaseParams))
	}
}

func TestCreateMessageMissingUsageStopsFallbackAndRecordsRiskExposure(t *testing.T) {
	missingUsageErr := adapter.NewUpstreamError(
		adapter.UpstreamErrorServer,
		adapter.UpstreamMetadata{StatusCode: 200, RequestID: "req-messages-missing-usage"},
		failure.Wrap(
			failure.CodeAdapterInvalidResponse,
			messagesadapter.ErrMessagesMissingUsage,
			failure.WithMessage("simulated messages response without usage"),
		),
	)
	adapterFake := &fakeMessagesAdapter{messagesErr: missingUsageErr}
	registry := &fakeMessagesRegistry{
		messages:   map[string]messagesadapter.MessagesAdapter{"deepseek": adapterFake},
		tokenizers: map[string]messagesadapter.MessagesInputTokenizer{"deepseek": adapterFake},
	}
	settlement := &fakeMessagesSettlement{}
	authorizer := &fakeMessagesAuthorizer{}
	requestLog := newFakeMessagesRequestLog()
	service := NewMessagesService(
		&fakeMessagesRouter{plan: routePlan(
			routeCandidate("deepseek", 123, "deepseek-v4-flash"),
			routeCandidate("deepseek", 124, "deepseek-v4-flash"),
		)},
		registry,
		passthroughCandidatePreparer{inputTokens: 1},
		lifecycle.ProviderErrorClassifier{},
		requestLog,
		settlement,
		authorizer,
		nil,
	)

	_, err := service.CreateMessage(contextWithPrincipal(42), messageRequest())
	if !errors.Is(err, messagesadapter.ErrMessagesMissingUsage) {
		t.Fatalf("expected missing usage error, got %v", err)
	}
	if adapterFake.messagesCalled != 1 {
		t.Fatalf("expected fallback to stop after one upstream call, got %d", adapterFake.messagesCalled)
	}
	if len(settlement.params) != 0 {
		t.Fatalf("expected no settlement without reliable usage, got %d", len(settlement.params))
	}
	if len(authorizer.releaseBillingExceptionParams) != 1 {
		t.Fatalf("expected one risk exposure release, got %d", len(authorizer.releaseBillingExceptionParams))
	}
	if authorizer.releaseBillingExceptionParams[0].ReasonCode != "messages_missing_usage" {
		t.Fatalf("risk exposure reason code = %q, want messages_missing_usage", authorizer.releaseBillingExceptionParams[0].ReasonCode)
	}
	if len(authorizer.releaseParams) != 0 {
		t.Fatalf("expected no plain authorization release, got %d", len(authorizer.releaseParams))
	}
	if len(requestLog.markRequestFailedArgs) != 1 || requestLog.markRequestFailedArgs[0].ErrorCode != string(failure.CodeAdapterInvalidResponse) {
		t.Fatalf("request failure args = %+v, want %q", requestLog.markRequestFailedArgs, failure.CodeAdapterInvalidResponse)
	}
}

func TestStreamMessageEmitsNativeEventsAndStopThenSettles(t *testing.T) {
	finalUsage := &messagesadapter.MessageUsage{InputTokens: 10, OutputTokens: 11}
	upstream := &adapter.UpstreamMetadata{StatusCode: 200, RequestID: "req-msg-stream"}
	facts := adapter.ResponseFacts{
		UpstreamProtocol:    "anthropic",
		UpstreamResponseID:  "msg_stream_test",
		UpstreamModel:       "deepseek-v4-flash",
		Finish:              adapter.FinishFacts{Class: adapter.FinishStop, RawReason: "end_turn"},
		Usage:               finalUsage.ToUsageFacts(),
		UsageSource:         coreusage.SourceUpstreamStream,
		UsageMappingVersion: "messagesadapter.v1",
		Metadata:            *upstream,
	}
	adapterFake := &fakeMessagesAdapter{
		streamEvents: []messagesadapter.MessageStreamEvent{
			{Type: "message_start", Data: json.RawMessage(`{"type":"message_start","message":{"id":"msg_stream_test","model":"deepseek-v4-flash"}}`)},
			{Type: "content_block_delta", Data: json.RawMessage(`{"type":"content_block_delta","index":0}`)},
			{Type: "message_delta", Data: json.RawMessage(`{"type":"message_delta"}`), Usage: finalUsage, Upstream: upstream},
		},
		streamOutcome: &adapter.StreamOutcome{Facts: &facts},
	}
	registry := &fakeMessagesRegistry{
		streamMessages: map[string]messagesadapter.StreamMessagesAdapter{"deepseek": adapterFake},
		tokenizers:     map[string]messagesadapter.MessagesInputTokenizer{"deepseek": adapterFake},
	}
	settlement := &fakeMessagesSettlement{}
	service := newMessagesServiceForTest(
		&fakeMessagesRouter{plan: routePlan(routeCandidate("deepseek", 123, "deepseek-v4-flash"))},
		registry,
		settlement,
		&fakeMessagesAuthorizer{},
	)

	var frames []gatewayapi.StreamFrame
	err := service.StreamMessage(contextWithPrincipal(42), messageRequest(), func(frame gatewayapi.StreamFrame) error {
		frames = append(frames, frame)
		return nil
	})
	if err != nil {
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

	// message_stop 必须由 gateway 在结算收口后写出，而不是由 adapter 透传。
	if len(settlement.params) != 1 {
		t.Fatalf("expected one settlement attempt, got %d", len(settlement.params))
	}
	if settlement.params[0].Facts.UsageSource != coreusage.SourceUpstreamStream {
		t.Fatalf("expected upstream_stream usage source, got %q", settlement.params[0].Facts.UsageSource)
	}

	// message_start 事件里的 model 必须被改写为客户 catalog model。
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
}

func TestStreamMessageReleasesWhenFinalUsageMissingAfterPreludeOnly(t *testing.T) {
	adapterFake := &fakeMessagesAdapter{
		streamEvents: []messagesadapter.MessageStreamEvent{
			{Type: "message_start", Data: json.RawMessage(`{"type":"message_start","message":{"id":"x","model":"deepseek-v4-flash"}}`)},
		},
		// 无 final usage：streamOutcome 为空。仅前导帧不算权威首字。
	}
	registry := &fakeMessagesRegistry{
		streamMessages: map[string]messagesadapter.StreamMessagesAdapter{"deepseek": adapterFake},
		tokenizers:     map[string]messagesadapter.MessagesInputTokenizer{"deepseek": adapterFake},
	}
	settlement := &fakeMessagesSettlement{}
	authorizer := &fakeMessagesAuthorizer{}
	service := newMessagesServiceForTest(
		&fakeMessagesRouter{plan: routePlan(routeCandidate("deepseek", 123, "deepseek-v4-flash"))},
		registry,
		settlement,
		authorizer,
	)

	err := service.StreamMessage(contextWithPrincipal(42), messageRequest(), func(frame gatewayapi.StreamFrame) error {
		return nil
	})
	// 仅前导帧 + 缺 usage：释放预扣、返回 usage-missing，不进入 partial settlement。
	if err == nil {
		t.Fatal("expected usage-missing error when final usage missing after prelude-only stream")
	}
	if failure.CodeOf(err) != failure.CodeGatewayStreamUsageMissing {
		t.Fatalf("expected %q, got %q (%v)", failure.CodeGatewayStreamUsageMissing, failure.CodeOf(err), err)
	}
	if len(settlement.params) != 0 {
		t.Fatalf("expected no partial settlement for prelude-only usage-missing, got %d calls", len(settlement.params))
	}
	if len(authorizer.releaseParams) != 1 {
		t.Fatalf("expected authorization released once, got %d", len(authorizer.releaseParams))
	}
	if len(authorizer.releaseBillingExceptionParams) != 0 {
		t.Fatalf("expected no risk_exposure for prelude-only usage-missing, got %d", len(authorizer.releaseBillingExceptionParams))
	}
}

func TestStreamMessagePartialSettlesWhenFinalUsageMissingAfterEmit(t *testing.T) {
	adapterFake := &fakeMessagesAdapter{
		streamEvents: []messagesadapter.MessageStreamEvent{
			{Type: "message_start", Data: json.RawMessage(`{"type":"message_start","message":{"id":"x","model":"deepseek-v4-flash"}}`)},
			{
				Type: "content_block_delta",
				Data: json.RawMessage(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`),
			},
		},
		// 无 final usage：streamOutcome 为空。
	}
	registry := &fakeMessagesRegistry{
		streamMessages: map[string]messagesadapter.StreamMessagesAdapter{"deepseek": adapterFake},
		tokenizers:     map[string]messagesadapter.MessagesInputTokenizer{"deepseek": adapterFake},
	}
	settlement := &fakeMessagesSettlement{}
	authorizer := &fakeMessagesAuthorizer{}
	service := newMessagesServiceForTest(
		&fakeMessagesRouter{plan: routePlan(routeCandidate("deepseek", 123, "deepseek-v4-flash"))},
		registry,
		settlement,
		authorizer,
	)

	var frames []gatewayapi.StreamFrame
	err := service.StreamMessage(contextWithPrincipal(42), messageRequest(), func(frame gatewayapi.StreamFrame) error {
		frames = append(frames, frame)
		return nil
	})
	// 路线 D：权威首字已交付后上游正常结束但缺 final usage → partial settlement，不报错、不写 risk_exposure。
	if err != nil {
		t.Fatalf("expected partial settlement (no error) when final usage missing after emit, got %v", err)
	}
	if len(settlement.params) != 1 {
		t.Fatalf("expected partial settlement to run once, got %d calls", len(settlement.params))
	}
	if settlement.params[0].Facts.UsageSource != coreusage.SourcePartialStreamEstimate {
		t.Fatalf("expected partial_stream_estimate usage source, got %q", settlement.params[0].Facts.UsageSource)
	}
	if settlement.params[0].Facts.Finish.RawReason != lifecycle.PartialReasonFinalUsageMissing {
		t.Fatalf("expected %q finish reason, got %q", lifecycle.PartialReasonFinalUsageMissing, settlement.params[0].Facts.Finish.RawReason)
	}
	if len(authorizer.releaseBillingExceptionParams) != 0 {
		t.Fatalf("expected no risk_exposure for partial settlement, got %d", len(authorizer.releaseBillingExceptionParams))
	}
	if len(frames) == 0 || frames[len(frames)-1].EventType != "message_stop" {
		t.Fatalf("partial success must end with message_stop, frames=%#v", frames)
	}
}
