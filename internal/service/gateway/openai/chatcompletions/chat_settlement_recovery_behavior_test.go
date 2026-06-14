package chatcompletions

import (
	"errors"
	"testing"

	gatewayapi "github.com/ThankCat/unio-api/internal/app/gatewayapi/openai/chatcompletions"
	chatcompletionsadapter "github.com/ThankCat/unio-api/internal/core/adapter/openai/chatcompletions"
	"github.com/ThankCat/unio-api/internal/service/gateway/lifecycle"
)

func TestChatCompletionServiceCreateChatCompletionReturnsResponseWhenRecoveryScheduled(t *testing.T) {
	settlementCause := errors.New("commit failed after usage")
	settlement := &fakeChatSettlementExecutor{err: lifecycle.ChatSettlementRecoveryScheduledError(1, settlementCause)}
	authorizer := &fakeChatAuthorizer{authorization: lifecycle.ChatAuthorization{ReservationID: 7702}}
	requestLog := newFakeRequestLogService()
	service := newChatCompletionServiceForTestWithAuthorizer(
		&fakeChatRouter{plan: routePlan(routeCandidate("openai", 123, "gpt-4.1"))},
		&fakeAdapterRegistry{
			chatAdapters: map[string]chatcompletionsadapter.ChatAdapter{
				"openai": &fakeChatAdapter{chatResp: chatResponse("adapter response")},
			},
		},
		nil,
		requestLog,
		settlement,
		authorizer,
	)

	got, err := service.CreateChatCompletion(contextWithPrincipal(42), chatRequest())
	if err != nil {
		t.Fatalf("expected response after recovery scheduled, got err: %v", err)
	}
	if got == nil || got.Choices[0].Message.ContentString() != "adapter response" {
		t.Fatalf("expected adapter response to be returned, got %#v", got)
	}
	if len(settlement.params) != 1 {
		t.Fatalf("expected one settlement attempt, got %d", len(settlement.params))
	}
	if len(requestLog.markRequestFailedArgs) != 0 {
		t.Fatalf("expected request not to be marked failed after recovery scheduled, got %d", len(requestLog.markRequestFailedArgs))
	}
	if len(authorizer.releaseParams) != 0 || len(authorizer.releaseBillingExceptionParams) != 0 {
		t.Fatalf("expected authorization not to be released after reliable usage, got release=%d exception=%d", len(authorizer.releaseParams), len(authorizer.releaseBillingExceptionParams))
	}
}

func TestChatCompletionServiceStreamReturnsNilWhenRecoveryScheduledAfterFinalUsage(t *testing.T) {
	settlementCause := errors.New("stream settlement commit failed")
	settlement := &fakeChatSettlementExecutor{err: lifecycle.ChatSettlementRecoveryScheduledError(1, settlementCause)}
	authorizer := &fakeChatAuthorizer{authorization: lifecycle.ChatAuthorization{ReservationID: 8831}}
	requestLog := newFakeRequestLogService()
	service := newChatCompletionServiceForTestWithAuthorizer(
		&fakeChatRouter{plan: routePlan(routeCandidate("openai", 123, "gpt-4.1"))},
		&fakeAdapterRegistry{
			streamChatAdapters: map[string]chatcompletionsadapter.StreamChatAdapter{
				"openai": &fakeChatAdapter{streamResp: []chatcompletionsadapter.ChatStreamChunk{
					{ID: "chatcmpl_mock", Model: "gpt-4.1", Role: "assistant", Content: "stream content"},
					streamUsageChunk("gpt-4.1"),
				}},
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
	if err != nil {
		t.Fatalf("expected nil stream error after recovery scheduled, got %v", err)
	}
	if len(settlement.params) != 1 {
		t.Fatalf("expected one settlement attempt, got %d", len(settlement.params))
	}
	if len(requestLog.markRequestFailedArgs) != 0 {
		t.Fatalf("expected request not to be marked failed after recovery scheduled, got %d", len(requestLog.markRequestFailedArgs))
	}
	if len(authorizer.releaseParams) != 0 || len(authorizer.releaseBillingExceptionParams) != 0 {
		t.Fatalf("expected authorization not to be released after reliable usage, got release=%d exception=%d", len(authorizer.releaseParams), len(authorizer.releaseBillingExceptionParams))
	}
}

func TestChatCompletionServiceStreamKeepsTailErrorWhenRecoveryScheduled(t *testing.T) {
	upstreamErr := errors.New("tail read failed")
	settlementCause := errors.New("settlement failed after tail usage")
	settlement := &fakeChatSettlementExecutor{err: lifecycle.ChatSettlementRecoveryScheduledError(1, settlementCause)}
	requestLog := newFakeRequestLogService()
	service := newChatCompletionServiceForTestWithAuthorizer(
		&fakeChatRouter{plan: routePlan(routeCandidate("openai", 123, "gpt-4.1"))},
		&fakeAdapterRegistry{
			streamChatAdapters: map[string]chatcompletionsadapter.StreamChatAdapter{
				"openai": &fakeChatAdapter{
					streamResp: []chatcompletionsadapter.ChatStreamChunk{
						{ID: "chatcmpl_mock", Model: "gpt-4.1", Role: "assistant", Content: "billable stream content"},
						streamUsageChunk("gpt-4.1"),
					},
					streamErr: upstreamErr,
				},
			},
		},
		nil,
		requestLog,
		settlement,
		&fakeChatAuthorizer{authorization: lifecycle.ChatAuthorization{ReservationID: 8841}},
	)

	err := service.StreamChatCompletion(contextWithPrincipal(42), chatRequest(), func(chunk gatewayapi.ChatCompletionStreamResponse) error {
		return nil
	})
	if !errors.Is(err, upstreamErr) {
		t.Fatalf("expected original stream tail error, got %v", err)
	}
	if len(settlement.params) != 1 {
		t.Fatalf("expected one settlement attempt, got %d", len(settlement.params))
	}
	if len(requestLog.markRequestFailedArgs) != 0 {
		t.Fatalf("expected request not to be marked failed after recovery scheduled, got %d", len(requestLog.markRequestFailedArgs))
	}
}
