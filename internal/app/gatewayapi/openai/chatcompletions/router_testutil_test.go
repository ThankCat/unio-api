package chatcompletions

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ThankCat/unio-gateway/internal/app/gatewayapi/middleware"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/lifecycle"
)

// routerTestChatCompletionService 是 chat completions handler 测试使用的 service 替身。
type routerTestChatCompletionService struct{}

// CreateChatCompletion 返回固定响应，避免 handler 测试依赖 gateway/provider 组合。
func (s *routerTestChatCompletionService) CreateChatCompletion(ctx context.Context, req ChatCompletionRequest) (*lifecycle.NonStreamResult[*ChatCompletionResponse], error) {
	resp := &ChatCompletionResponse{
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
	}
	return lifecycle.NewNonStreamResult(resp, lifecycle.NewDeliveryFinalizer(func() {}, func() {})), nil
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

// newTestRouter 创建仅包含 /v1/chat/completions 的测试 router，挂载生产鉴权；
// request admission 的生命周期与协议映射由 middleware/request_admission_test.go 独立覆盖。
//
// 它不引入 gatewayapi 根包，避免子包 → gatewayapi → 子包的测试编译环；
// 顶层 httpmw（request id/metrics/logger）与跨 endpoint 路由（models）在
// gatewayapi router_test.go 和 models 子包中单独验证。
func newTestRouter(authenticator middleware.APIKeyAuthenticator, chatService ChatCompletionService, _ any) http.Handler {
	if chatService == nil {
		chatService = &routerTestChatCompletionService{}
	}

	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		r.Use(middleware.APIKeyAuth(authenticator))
		r.Method(http.MethodPost, "/chat/completions", NewChatCompletionsHandler(chatService))
	})

	return r
}
