package bootstrap

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"

	gatewayanthropic "github.com/ThankCat/unio-gateway/internal/app/gatewayapi/anthropic/messages"
	gatewayapi "github.com/ThankCat/unio-gateway/internal/app/gatewayapi/openai/chatcompletions"
	gatewayresponses "github.com/ThankCat/unio-gateway/internal/app/gatewayapi/openai/responses"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/lifecycle"
)

type fakeHTTPChatCompletionService struct{}

func (s fakeHTTPChatCompletionService) CreateChatCompletion(ctx context.Context, req gatewayapi.ChatCompletionRequest) (*lifecycle.NonStreamResult[*gatewayapi.ChatCompletionResponse], error) {
	return lifecycle.NewNonStreamResult(&gatewayapi.ChatCompletionResponse{}, lifecycle.NewDeliveryFinalizer(func() {}, func() {})), nil
}

func (s fakeHTTPChatCompletionService) StreamChatCompletion(ctx context.Context, req gatewayapi.ChatCompletionRequest, emit func(gatewayapi.ChatCompletionStreamResponse) error) error {
	return nil
}

type fakeHTTPResponsesService struct{}

func (s fakeHTTPResponsesService) CreateResponse(ctx context.Context, req gatewayresponses.ResponsesRequest) (*lifecycle.NonStreamResult[*gatewayresponses.ResponsesResponse], error) {
	return lifecycle.NewNonStreamResult(&gatewayresponses.ResponsesResponse{Object: "response"}, lifecycle.NewDeliveryFinalizer(func() {}, func() {})), nil
}

func (s fakeHTTPResponsesService) StreamResponse(ctx context.Context, req gatewayresponses.ResponsesRequest, emit func(gatewayresponses.ResponsesStreamEvent) error) error {
	return nil
}

func (s fakeHTTPResponsesService) CompactHistory(ctx context.Context, req gatewayresponses.ResponsesRequest) (*lifecycle.NonStreamResult[*gatewayresponses.CompactHistoryResponse], error) {
	return lifecycle.NewNonStreamResult(&gatewayresponses.CompactHistoryResponse{}, lifecycle.NewDeliveryFinalizer(func() {}, func() {})), nil
}

func (s fakeHTTPResponsesService) CountInputTokens(ctx context.Context, req gatewayresponses.ResponsesRequest) (*gatewayresponses.InputTokenCountResponse, error) {
	return &gatewayresponses.InputTokenCountResponse{Object: "response.input_tokens"}, nil
}

type fakeHTTPMessagesService struct{}

func (s fakeHTTPMessagesService) CreateMessage(ctx context.Context, req gatewayanthropic.MessageRequest) (*lifecycle.NonStreamResult[*gatewayanthropic.MessageResponse], error) {
	return lifecycle.NewNonStreamResult(&gatewayanthropic.MessageResponse{Type: "message", Role: "assistant"}, lifecycle.NewDeliveryFinalizer(func() {}, func() {})), nil
}

func (s fakeHTTPMessagesService) StreamMessage(ctx context.Context, req gatewayanthropic.MessageRequest, emit func(gatewayanthropic.StreamFrame) error) error {
	return nil
}

func TestNewHTTPHandlerBuildsHealthRoute(t *testing.T) {
	logger := zap.NewNop()
	handler := NewHTTPHandler(
		logger,
		&sqlc.Queries{},
		nil,
		fakeHTTPChatCompletionService{},
		fakeHTTPResponsesService{},
		fakeHTTPMessagesService{},
		nil,
		nil,
	)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %q", http.StatusOK, rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "{\"status\":\"ok\"}\n" {
		t.Fatalf("unexpected health response body %q", rec.Body.String())
	}
}
