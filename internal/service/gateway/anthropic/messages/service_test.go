package messages

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	gatewayapi "github.com/ThankCat/unio-api/internal/app/gatewayapi/anthropic/messages"
	"github.com/ThankCat/unio-api/internal/core/adapter"
	messagesadapter "github.com/ThankCat/unio-api/internal/core/adapter/anthropic/messages"
	"github.com/ThankCat/unio-api/internal/core/auth"
	"github.com/ThankCat/unio-api/internal/core/channel"
	"github.com/ThankCat/unio-api/internal/core/requestlog"
	"github.com/ThankCat/unio-api/internal/core/routing"
	coreusage "github.com/ThankCat/unio-api/internal/core/usage"
	"github.com/ThankCat/unio-api/internal/platform/httpx"
	"github.com/ThankCat/unio-api/internal/service/gateway/lifecycle"
)

// fakeMessagesRouter 是 messages 测试使用的 routing 替身。
type fakeMessagesRouter struct {
	req  routing.ChatRouteRequest
	plan routing.ChatRoutePlan
	err  error
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

	for _, ev := range a.streamEvents {
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
	markRequestFailedArgs []requestlog.MarkRequestFailedParams
	markRequestCanceled   []requestlog.MarkRequestCanceledParams
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
	return requestlog.RequestRecord{ID: id, RequestID: params.RequestID, UserID: params.UserID, ProjectID: params.ProjectID, APIKeyID: params.APIKeyID, Status: requestlog.RequestStatusPending}, nil
}

func (s *fakeMessagesRequestLog) MarkRequestRunning(ctx context.Context, id int64) (requestlog.RequestRecord, error) {
	return requestlog.RequestRecord{ID: id, Status: requestlog.RequestStatusRunning}, nil
}

func (s *fakeMessagesRequestLog) MarkRequestSucceeded(ctx context.Context, params requestlog.MarkRequestSucceededParams) (requestlog.RequestRecord, error) {
	return requestlog.RequestRecord{ID: params.ID, Status: requestlog.RequestStatusSucceeded}, nil
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
	authorizeParams               []lifecycle.ChatAuthorizeParams
	releaseParams                 []lifecycle.ChatReleaseAuthorizationParams
	releaseBillingExceptionParams []lifecycle.ChatReleaseBillingExceptionParams
	authorization                 lifecycle.ChatAuthorization
	authorizeErr                  error
}

func (a *fakeMessagesAuthorizer) AuthorizeChat(ctx context.Context, params lifecycle.ChatAuthorizeParams) (lifecycle.ChatAuthorization, error) {
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

func contextWithPrincipal(projectID int64) context.Context {
	ctx := httpx.ContextWithRequestID(context.Background(), "messages-test-request-id")
	return auth.ContextWithAPIKeyPrincipal(ctx, &auth.APIKeyPrincipal{
		APIKeyID:  1,
		UserID:    7,
		ProjectID: projectID,
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
			ID:      channelID,
			BaseURL: "https://example.test",
			APIKey:  "test-secret",
			Timeout: 30 * time.Second,
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
		nil,
	)
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

	resp, err := service.CreateMessage(contextWithPrincipal(42), messageRequest())
	if err != nil {
		t.Fatalf("CreateMessage returned err: %v", err)
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

func TestStreamMessageMissingFinalUsageReleasesAndFails(t *testing.T) {
	adapterFake := &fakeMessagesAdapter{
		streamEvents: []messagesadapter.MessageStreamEvent{
			{Type: "message_start", Data: json.RawMessage(`{"type":"message_start","message":{"id":"x","model":"deepseek-v4-flash"}}`)},
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

	err := service.StreamMessage(contextWithPrincipal(42), messageRequest(), func(frame gatewayapi.StreamFrame) error {
		return nil
	})
	if err == nil {
		t.Fatal("expected stream usage missing error")
	}
	if len(settlement.params) != 0 {
		t.Fatalf("expected no settlement without final usage, got %d", len(settlement.params))
	}
	if len(authorizer.releaseBillingExceptionParams) != 1 {
		t.Fatalf("expected billing exception release once, got %d", len(authorizer.releaseBillingExceptionParams))
	}
}
