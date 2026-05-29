package bootstrap

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ThankCat/unio-api/internal/app/gatewayapi"
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
