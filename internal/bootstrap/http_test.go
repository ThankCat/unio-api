package bootstrap

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	gatewayanthropic "github.com/ThankCat/unio-api/internal/app/gatewayapi/anthropic/messages"
	gatewayapi "github.com/ThankCat/unio-api/internal/app/gatewayapi/openai/chatcompletions"
	"github.com/ThankCat/unio-api/internal/platform/config"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
)

type fakeHTTPChatCompletionService struct{}

func (s fakeHTTPChatCompletionService) CreateChatCompletion(ctx context.Context, req gatewayapi.ChatCompletionRequest) (*gatewayapi.ChatCompletionResponse, error) {
	return &gatewayapi.ChatCompletionResponse{}, nil
}

func (s fakeHTTPChatCompletionService) StreamChatCompletion(ctx context.Context, req gatewayapi.ChatCompletionRequest, emit func(gatewayapi.ChatCompletionStreamResponse) error) error {
	return nil
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
		nil,
		config.Config{
			Redis: config.RedisConfig{KeyNamespace: "unio:test"},
			RateLimit: config.RateLimitConfig{
				DefaultLimit:  60,
				DefaultWindow: time.Minute,
				FailurePolicy: "fail_closed",
			},
		},
		fakeHTTPChatCompletionService{},
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
