package models

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

func ptrInt64(v int64) *int64 { return &v }

// routerTestRateLimiter 是 models handler 集成测试使用的「线路+用户」级限流器替身。
type routerTestRateLimiter struct {
	routeID  int64
	userID   int64
	decision ratelimit.Decision
	err      error
}

// AllowRouteUserRequest 记录收到的 (线路,用户)，并返回测试预设的限流判断结果。
func (l *routerTestRateLimiter) AllowRouteUserRequest(_ context.Context, routeID, userID int64, _ ratelimit.Limits) (ratelimit.Decision, error) {
	l.routeID = routeID
	l.userID = userID
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

// routerTestModelCatalogService 是 models handler 测试使用的模型目录 service 替身。
type routerTestModelCatalogService struct {
	called               bool
	projectID            int64
	requiredCapabilities []string
	models               []modelcatalog.Model
	err                  error
}

// ListAvailableModels 记录收到的 project id 与 capability 过滤，并返回测试预设的模型列表。
func (s *routerTestModelCatalogService) ListAvailableModels(ctx context.Context, projectID int64, requiredCapabilities []string) ([]modelcatalog.Model, error) {
	s.called = true
	s.projectID = projectID
	s.requiredCapabilities = requiredCapabilities
	return s.models, s.err
}

// newTestRouter 创建仅包含 /v1/models 的测试 router，挂载与生产一致的鉴权与限流中间件。
//
// 它不引入 gatewayapi 根包，避免子包 → gatewayapi → 子包的测试编译环；
// 顶层 httpmw（request id/metrics/logger）与跨 operation 路由在
// gatewayapi router_test.go 中单独验证。
//
// 第 4 个参数（modelCatalogServices 变长形式）保留 chat 包同名 helper 的调用习惯，
// 即 newTestRouter(auth, _ignored_, limiter, modelCatalogService)，让 models 包测试和
// chat 包测试的调用形态保持一致。chatService 形参当前只用于占位，不真正影响 models endpoint。
func newTestRouter(authenticator middleware.APIKeyAuthenticator, _ any, limiter middleware.KeyRateLimiter, modelCatalogServices ...ModelCatalogService) http.Handler {
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
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		}))

		r.Get("/models", NewModelsHandler(modelCatalogService))
	})

	return r
}
