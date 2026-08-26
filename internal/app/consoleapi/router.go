// Package consoleapi 组装 Unio Console 的公开 HTTP API。
package consoleapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	consoleapikeys "github.com/ThankCat/unio-gateway/internal/app/consoleapi/apikeys"
	consoleauth "github.com/ThankCat/unio-gateway/internal/app/consoleapi/auth"
	consolemeta "github.com/ThankCat/unio-gateway/internal/app/consoleapi/meta"
	consolemiddleware "github.com/ThankCat/unio-gateway/internal/app/consoleapi/middleware"
	consolerequests "github.com/ThankCat/unio-gateway/internal/app/consoleapi/requests"
	"github.com/ThankCat/unio-gateway/internal/app/consoleapi/transport"
	consoleusage "github.com/ThankCat/unio-gateway/internal/app/consoleapi/usage"
	"github.com/ThankCat/unio-gateway/internal/platform/config"
	"github.com/ThankCat/unio-gateway/internal/platform/httpmw"
	consoleservice "github.com/ThankCat/unio-gateway/internal/service/console"
)

// Deps 包含 Console HTTP 路由所需的基础设施依赖。
type Deps struct {
	Logger         *zap.Logger
	Config         config.ConsoleConfig
	AuthService    consoleauth.Service
	RequestService consolerequests.Service
	UsageService   consoleusage.Service
	APIKeyService  consoleapikeys.Service
}

// NewRouter 构建公开的 Console API 路由及其公共中间件。
func NewRouter(deps Deps) (http.Handler, error) {
	ipResolver, err := consolemiddleware.NewClientIPResolver(deps.Config.TrustedProxyCIDRs)
	if err != nil {
		return nil, err
	}

	r := chi.NewRouter()
	r.Use(consolemiddleware.CORS(deps.Config.AllowedOrigins))
	r.Use(httpmw.RequestID)
	r.Use(consolemiddleware.ClientIP(ipResolver))
	r.Use(httpmw.Tracing)
	r.Use(httpmw.Logger(deps.Logger))
	r.Use(consolemiddleware.Recoverer(deps.Logger))
	errorWriter := transport.NewErrorWriter(deps.Logger)

	r.NotFound(func(w http.ResponseWriter, req *http.Request) {
		errorWriter.Write(w, req, &consoleservice.Error{
			Code:    "not_found",
			Message: "The requested route was not found.",
			Status:  http.StatusNotFound,
		})
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, req *http.Request) {
		errorWriter.Write(w, req, &consoleservice.Error{
			Code:    "method_not_allowed",
			Message: "The HTTP method is not allowed for this route.",
			Status:  http.StatusMethodNotAllowed,
		})
	})
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_ = transport.WriteData(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	r.Route("/v1", func(r chi.Router) {
		consoleauth.Register(r, consoleauth.Deps{
			CookieDomain: deps.Config.CookieDomain,
			CookieSecure: deps.Config.CookieSecure,
			Service:      deps.AuthService,
			ErrorWriter:  errorWriter,
		})
		// 接入元信息只读进程配置，不查库也不需要身份：用户在登录页就该能看到往哪儿发请求。
		consolemeta.Register(r, consolemeta.Deps{
			GatewayPublicBaseURL: deps.Config.GatewayPublicBaseURL,
			DocsBaseURL:          deps.Config.DocsBaseURL,
		})
		if deps.RequestService != nil || deps.UsageService != nil || deps.APIKeyService != nil {
			r.Group(func(r chi.Router) {
				r.Use(consoleauth.RequireAuth(deps.AuthService, errorWriter))
				if deps.RequestService != nil {
					consolerequests.Register(r, consolerequests.Deps{
						Service:     deps.RequestService,
						ErrorWriter: errorWriter,
					})
				}
				if deps.UsageService != nil {
					consoleusage.Register(r, consoleusage.Deps{
						Service:     deps.UsageService,
						ErrorWriter: errorWriter,
					})
				}
				if deps.APIKeyService != nil {
					consoleapikeys.Register(r, consoleapikeys.Deps{
						Service:     deps.APIKeyService,
						ErrorWriter: errorWriter,
					})
				}
			})
		}
	})
	return r, nil
}
