package models

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/ThankCat/unio-gateway/internal/app/gatewayapi/middleware"
	"github.com/ThankCat/unio-gateway/internal/core/modelcatalog"
)

func ptrInt64(v int64) *int64 { return &v }

// routerTestModelCatalogService 是 models handler 测试使用的模型目录 service 替身。
type routerTestModelCatalogService struct {
	called               bool
	projectID            int64
	routeID              int64
	requiredCapabilities []string
	models               []modelcatalog.Model
	err                  error
}

// ListAvailableModels 记录收到的 project id 与 capability 过滤，并返回测试预设的模型列表。
func (s *routerTestModelCatalogService) ListAvailableModels(ctx context.Context, projectID, routeID int64, requiredCapabilities []string) ([]modelcatalog.Model, error) {
	s.called = true
	s.projectID = projectID
	s.routeID = routeID
	s.requiredCapabilities = requiredCapabilities
	return s.models, s.err
}

// newTestRouter 创建仅包含 /v1/models 的测试 router，挂载生产鉴权。
//
// 它不引入 gatewayapi 根包，避免子包 → gatewayapi → 子包的测试编译环；
// 顶层 httpmw（request id/metrics/logger）与跨 endpoint 路由在
// gatewayapi router_test.go 中单独验证。
//
// 第 4 个参数（modelCatalogServices 变长形式）保留 chat 包同名 helper 的调用习惯，
// 即 newTestRouter(auth, _ignored_, _admissionPlaceholder, modelCatalogService)，让 models 包测试和
// chat 包测试的调用形态保持一致。chatService 形参当前只用于占位，不真正影响 models origin。
func newTestRouter(authenticator middleware.APIKeyAuthenticator, _ any, _ any, modelCatalogServices ...ModelCatalogService) http.Handler {
	modelCatalogService := ModelCatalogService(&routerTestModelCatalogService{})
	if len(modelCatalogServices) > 0 && modelCatalogServices[0] != nil {
		modelCatalogService = modelCatalogServices[0]
	}

	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		r.Use(middleware.APIKeyAuth(authenticator))
		r.Get("/models", NewModelsHandler(modelCatalogService))
	})

	return r
}
