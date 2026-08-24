package usage

import (
	"context"

	"github.com/go-chi/chi/v5"

	"github.com/ThankCat/unio-gateway/internal/app/consoleapi/transport"
	consoleservice "github.com/ThankCat/unio-gateway/internal/service/console"
	consoleusage "github.com/ThankCat/unio-gateway/internal/service/console/usage"
)

// Service 定义 HTTP 适配层依赖的用量统计查询能力。
type Service interface {
	Overview(context.Context, consoleusage.OverviewParams) (consoleusage.Overview, *consoleservice.Error)
	Trend(context.Context, consoleusage.TrendParams) (consoleusage.Trend, *consoleservice.Error)
	Groups(context.Context, consoleusage.GroupParams) ([]consoleusage.GroupItem, *consoleservice.Error)
	Filters(context.Context, int64) (consoleusage.UsageFilters, *consoleservice.Error)
}

var _ Service = (*consoleusage.Service)(nil)

// Deps 包含用量统计 HTTP 适配层的依赖。
type Deps struct {
	Service     Service
	ErrorWriter transport.ErrorWriter
}

// Register 将用量统计路由挂载到 /usage。
func Register(r chi.Router, deps Deps) {
	h := &handler{
		service:     deps.Service,
		errorWriter: deps.ErrorWriter,
	}
	r.Route("/usage", func(r chi.Router) {
		r.Get("/overview", h.overview)
		r.Get("/trend", h.trend)
		r.Get("/groups", h.groups)
		r.Get("/filters", h.filters)
	})
}

type handler struct {
	service     Service
	errorWriter transport.ErrorWriter
}
