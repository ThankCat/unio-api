package bootstrap

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	gatewayanthropic "github.com/ThankCat/unio-api/internal/app/gatewayapi/anthropic/messages"
	gatewayapi "github.com/ThankCat/unio-api/internal/app/gatewayapi/openai/chatcompletions"
	gatewayresponses "github.com/ThankCat/unio-api/internal/app/gatewayapi/openai/responses"
	"github.com/ThankCat/unio-api/internal/platform/ratelimit"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-api/internal/service/appsettings"
)

type fakeHTTPChatCompletionService struct{}

func (s fakeHTTPChatCompletionService) CreateChatCompletion(ctx context.Context, req gatewayapi.ChatCompletionRequest) (*gatewayapi.ChatCompletionResponse, error) {
	return &gatewayapi.ChatCompletionResponse{}, nil
}

func (s fakeHTTPChatCompletionService) StreamChatCompletion(ctx context.Context, req gatewayapi.ChatCompletionRequest, emit func(gatewayapi.ChatCompletionStreamResponse) error) error {
	return nil
}

type fakeHTTPResponsesService struct{}

func (s fakeHTTPResponsesService) CreateResponse(ctx context.Context, req gatewayresponses.ResponsesRequest) (*gatewayresponses.ResponsesResponse, error) {
	return &gatewayresponses.ResponsesResponse{Object: "response"}, nil
}

func (s fakeHTTPResponsesService) StreamResponse(ctx context.Context, req gatewayresponses.ResponsesRequest, emit func(gatewayresponses.ResponsesStreamEvent) error) error {
	return nil
}

func (s fakeHTTPResponsesService) CompactHistory(ctx context.Context, req gatewayresponses.ResponsesRequest) (*gatewayresponses.CompactHistoryResponse, error) {
	return &gatewayresponses.CompactHistoryResponse{}, nil
}

func (s fakeHTTPResponsesService) CountInputTokens(ctx context.Context, req gatewayresponses.ResponsesRequest) (*gatewayresponses.InputTokenCountResponse, error) {
	return &gatewayresponses.InputTokenCountResponse{Object: "response.input_tokens"}, nil
}

type fakeHTTPMessagesService struct{}

func (s fakeHTTPMessagesService) CreateMessage(ctx context.Context, req gatewayanthropic.MessageRequest) (*gatewayanthropic.MessageResponse, error) {
	return &gatewayanthropic.MessageResponse{Type: "message", Role: "assistant"}, nil
}

func (s fakeHTTPMessagesService) StreamMessage(ctx context.Context, req gatewayanthropic.MessageRequest, emit func(gatewayanthropic.StreamFrame) error) error {
	return nil
}

func TestNewHTTPHandlerBuildsHealthRoute(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := NewHTTPHandler(
		logger,
		&sqlc.Queries{},
		NewRateLimitGuard(nil, "unio:test", appsettings.DefaultRateLimitDefaultsSettings(), logger),
		ratelimit.NewConcurrencyLimiter(0, 0),
		fakeHTTPChatCompletionService{},
		fakeHTTPResponsesService{},
		fakeHTTPMessagesService{},
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
