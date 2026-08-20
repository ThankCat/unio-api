package requests

import (
	"context"

	"github.com/go-chi/chi/v5"

	"github.com/ThankCat/unio-gateway/internal/app/consoleapi/transport"
	consoleservice "github.com/ThankCat/unio-gateway/internal/service/console"
	consolerequests "github.com/ThankCat/unio-gateway/internal/service/console/requests"
)

// Service 定义 HTTP 适配层依赖的客户请求日志查询能力。
type Service interface {
	List(context.Context, consolerequests.ListParams) ([]consolerequests.Item, int64, *consoleservice.Error)
	Summary(context.Context, consolerequests.SummaryParams) (consolerequests.Summary, *consoleservice.Error)
	Filters(context.Context, int64) (consolerequests.Filters, *consoleservice.Error)
}

var _ Service = (*consolerequests.Service)(nil)

// Deps 包含客户请求日志 HTTP 适配层的依赖。
type Deps struct {
	Service     Service
	ErrorWriter transport.ErrorWriter
}

// Register 将客户请求日志路由挂载到 /requests。
func Register(r chi.Router, deps Deps) {
	h := &handler{
		service:     deps.Service,
		errorWriter: deps.ErrorWriter,
	}
	r.Route("/requests", func(r chi.Router) {
		r.Get("/", h.list)
		r.Get("/summary", h.summary)
		r.Get("/filters", h.filters)
	})
}

type handler struct {
	service     Service
	errorWriter transport.ErrorWriter
}
