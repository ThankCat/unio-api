package chatcompletions

import (
	"errors"
	"reflect"
	"testing"

	gatewayapi "github.com/ThankCat/unio-api/internal/app/gatewayapi/openai/chatcompletions"
	chatcompletionsadapter "github.com/ThankCat/unio-api/internal/core/adapter/openai/chatcompletions"
	"github.com/ThankCat/unio-api/internal/core/capability"
	"github.com/ThankCat/unio-api/internal/core/routing"
	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/service/gateway/lifecycle"
)

// TestCreateChatCompletionThreadsRequiredCapabilities 断言 ingress 推断的 required capabilities
// 既透传到 routing.ChatRouteRequest（供 observe 闸门消费），又快照进 request_attempts。
func TestCreateChatCompletionThreadsRequiredCapabilities(t *testing.T) {
	effort := "high"
	req := gatewayapi.ChatCompletionRequest{
		Model:           "openai/gpt-4.1",
		Messages:        []gatewayapi.ChatMessage{{Role: "user", Content: jsonContent("hi")}},
		ReasoningEffort: &effort,
	}

	expected := gatewayapi.RequiredCapabilities(req)
	// sanity：reasoning_effort 应被推断出非基线能力，避免断言退化为仅文本基线。
	if !expected.Has(capability.KeyReasoningEffort) {
		t.Fatalf("expected inference to include reasoning.effort, got %v", expected.StringKeys())
	}

	router := &fakeChatRouter{plan: routePlan(routeCandidate("openai", 123, "gpt-4.1"))}
	registry := &fakeAdapterRegistry{
		chatAdapters: map[string]chatcompletionsadapter.ChatAdapter{
			"openai": &fakeChatAdapter{chatResp: chatResponse("adapter response")},
		},
	}
	requestLog := newFakeRequestLogService()
	settlement := newChatCompletionSettlementForTest()
	authorizer := &fakeChatAuthorizer{authorization: lifecycle.ChatAuthorization{ReservationID: 1}}
	service := newChatCompletionServiceForTestWithAuthorizer(router, registry, nil, requestLog, settlement, authorizer)

	if _, err := service.CreateChatCompletion(contextWithPrincipal(42), req); err != nil {
		t.Fatalf("CreateChatCompletion returned err: %v", err)
	}

	if !router.req.RequiredCapabilities.Equal(expected) {
		t.Fatalf("routing required = %v, want %v", router.req.RequiredCapabilities.StringKeys(), expected.StringKeys())
	}

	if len(requestLog.createAttempts) != 1 {
		t.Fatalf("expected one attempt, got %d", len(requestLog.createAttempts))
	}
	if got := requestLog.createAttempts[0].RequiredCapabilities; !reflect.DeepEqual(got, expected.StringKeys()) {
		t.Fatalf("attempt required_capabilities = %v, want %v", got, expected.StringKeys())
	}
}

// TestCreateChatCompletionRecordsCapabilityResultOnEnforceReject 锁定审计保真：enforce 闸门拒绝时
// PlanChat 返回 (带 Capability 的 plan, capability 错误)，service 必须在处理错误前先写 capability_check_result，
// 否则最该审计的拒绝反而漏记（NULL=bypassed）。
func TestCreateChatCompletionRecordsCapabilityResultOnEnforceReject(t *testing.T) {
	req := gatewayapi.ChatCompletionRequest{
		Model:    "openai/gpt-4.1",
		Messages: []gatewayapi.ChatMessage{{Role: "user", Content: jsonContent("hi")}},
	}

	router := &fakeChatRouter{
		plan: routing.ChatRoutePlan{
			Capability: &routing.CapabilityObservation{Result: capability.GateResultModelUnavailable},
		},
		err: failure.Wrap(
			failure.CodeRoutingModelCapabilityUnavailable,
			routing.ErrModelCapabilityUnavailable,
			failure.WithMessage(routing.ErrModelCapabilityUnavailable.Error()),
		),
	}
	registry := &fakeAdapterRegistry{}
	requestLog := newFakeRequestLogService()
	settlement := newChatCompletionSettlementForTest()
	authorizer := &fakeChatAuthorizer{authorization: lifecycle.ChatAuthorization{ReservationID: 1}}
	service := newChatCompletionServiceForTestWithAuthorizer(router, registry, nil, requestLog, settlement, authorizer)

	_, err := service.CreateChatCompletion(contextWithPrincipal(42), req)
	if !errors.Is(err, routing.ErrModelCapabilityUnavailable) {
		t.Fatalf("CreateChatCompletion err = %v, want ErrModelCapabilityUnavailable", err)
	}

	// 关键断言：拒绝路径也写了 capability_check_result（而非 NULL）。
	if want := []string{string(capability.GateResultModelUnavailable)}; !reflect.DeepEqual(requestLog.capabilityResults, want) {
		t.Fatalf("capabilityResults = %v, want %v", requestLog.capabilityResults, want)
	}
	// 拒绝后不应进入候选/上游调用。
	if len(requestLog.createAttempts) != 0 {
		t.Fatalf("expected no attempts on capability rejection, got %d", len(requestLog.createAttempts))
	}
}
