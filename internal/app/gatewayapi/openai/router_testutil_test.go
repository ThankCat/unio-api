package openai

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ThankCat/unio-api/internal/app/gatewayapi/middleware"
	"github.com/ThankCat/unio-api/internal/core/modelcatalog"
	"github.com/ThankCat/unio-api/internal/platform/ratelimit"
)

// routerTestRateLimiter 是 openai handler 集成测试使用的限流器替身，记录收到的限流参数。
type routerTestRateLimiter struct {
	subject  string
	limit    int64
	window   time.Duration
	decision ratelimit.Decision
	err      error
}

// Allow 记录收到的限流参数，并返回测试预设的限流判断结果。
func (l *routerTestRateLimiter) Allow(ctx context.Context, subject string, limit int64, window time.Duration) (ratelimit.Decision, error) {
	l.subject = subject
	l.limit = limit
	l.window = window
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

// routerTestModelCatalogService 是 openai handler 测试使用的模型目录 service 替身。
type routerTestModelCatalogService struct {
	called    bool
	projectID int64
	models    []modelcatalog.Model
	err       error
}

// ListAvailableModels 记录收到的 project id，并返回测试预设的模型列表。
func (s *routerTestModelCatalogService) ListAvailableModels(ctx context.Context, projectID int64) ([]modelcatalog.Model, error) {
	s.called = true
	s.projectID = projectID
	return s.models, s.err
}

// routerTestChatCompletionService 是 openai handler 测试使用的 chat completion service 替身。
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

// newTestRouter 创建仅包含 /v1 OpenAI 路由的测试 router，挂载与生产一致的鉴权与限流中间件。
//
// 它不引入 gatewayapi 根包，避免 openai → gatewayapi → openai 的测试编译环；
// 顶层 httpmw（request id/metrics/logger）在 gatewayapi router_test.go 中单独验证。
func newTestRouter(authenticator middleware.APIKeyAuthenticator, chatService ChatCompletionService, limiter middleware.RateLimiter, modelCatalogServices ...ModelCatalogService) http.Handler {
	if chatService == nil {
		chatService = &routerTestChatCompletionService{}
	}

	if limiter == nil {
		limiter = newAllowingRateLimiter()
	}

	modelCatalogService := ModelCatalogService(&routerTestModelCatalogService{})
	if len(modelCatalogServices) > 0 && modelCatalogServices[0] != nil {
		modelCatalogService = modelCatalogServices[0]
	}

	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		r.Use(middleware.APIKeyAuth(authenticator))
		r.Use(middleware.RateLimit(limiter, middleware.RateLimitOptions{
			Limit:  60,
			Window: time.Minute,
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		}))

		r.Get("/models", NewModelsHandler(modelCatalogService))
		r.Method(http.MethodPost, "/chat/completions", NewChatCompletionsHandler(chatService))
	})

	return r
}
