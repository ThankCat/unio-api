package chatcompletions

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ThankCat/unio-gateway/internal/app/gatewayapi/middleware"
	"github.com/ThankCat/unio-gateway/internal/platform/ratelimit"
)

// routerTestRateLimiter 是 chat completions handler 集成测试使用的「线路+用户」级限流器替身。
type routerTestRateLimiter struct {
	decision ratelimit.Decision
	err      error
}

// AllowRouteUserRequest 返回测试预设的限流判断结果。
func (l *routerTestRateLimiter) AllowRouteUserRequest(_ context.Context, _, _ int64, _ ratelimit.Limits) (ratelimit.Decision, error) {
	return l.decision, l.err
}

// newAllowingRateLimiter 创建默认允许请求通过的测试限流器。
func newAllowingRateLimiter() *routerTestRateLimiter {
	return &routerTestRateLimiter{
		decision: ratelimit.Decision{
			Allowed:   true,
			Limit:     60,
			Remaining: 59,
			ResetAt:   time.Date(2026, 5, 8, 10, 1, 0, 0, time.UTC),
		},
	}
}

// routerTestChatCompletionService 是 chat completions handler 测试使用的 service 替身。
type routerTestChatCompletionService struct{}

// CreateChatCompletion 返回固定响应，避免 handler 测试依赖 gateway/provider 组合。
func (s *routerTestChatCompletionService) CreateChatCompletion(ctx context.Context, req ChatCompletionRequest) (*ChatCompletionResponse, error) {
	return &ChatCompletionResponse{
		ID:      "chatcmpl_test",
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   req.Model,
		Choices: []ChatCompletionChoice{
			{
				Index: 0,
				Message: ChatMessage{
					Role:    "assistant",
					Content: jsonContent("mock response"),
				},
				FinishReason: "stop",
			},
		},
		Usage: ChatCompletionUsage{},
	}, nil
}

// StreamChatCompletion 发出固定流式响应，避免 handler 测试依赖 gateway/adapter 组合。
func (s *routerTestChatCompletionService) StreamChatCompletion(ctx context.Context, req ChatCompletionRequest, emit func(ChatCompletionStreamResponse) error) error {
	return emit(ChatCompletionStreamResponse{
		ID:      "chatcmpl_mock",
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   req.Model,
		Choices: []ChatCompletionStreamChoice{
			{
				Index: 0,
				Delta: ChatCompletionStreamDelta{
					Role:    "assistant",
					Content: "mock response",
				},
				FinishReason: nil,
			},
		},
	})
}

// newTestRouter 创建仅包含 /v1/chat/completions 的测试 router，挂载与生产一致的鉴权与限流中间件。
//
// 它不引入 gatewayapi 根包，避免子包 → gatewayapi → 子包的测试编译环；
// 顶层 httpmw（request id/metrics/logger）与跨 operation 路由（models）在
// gatewayapi router_test.go 和 models 子包中单独验证。
func newTestRouter(authenticator middleware.APIKeyAuthenticator, chatService ChatCompletionService, limiter middleware.KeyRateLimiter) http.Handler {
	if chatService == nil {
		chatService = &routerTestChatCompletionService{}
	}

	if limiter == nil {
		limiter = newAllowingRateLimiter()
	}

	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		r.Use(middleware.APIKeyAuth(authenticator))
		r.Use(middleware.RateLimit(limiter, middleware.RateLimitOptions{
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		}))

		r.Method(http.MethodPost, "/chat/completions", NewChatCompletionsHandler(chatService))
	})

	return r
}
