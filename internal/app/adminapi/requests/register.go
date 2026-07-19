package requests

import "github.com/go-chi/chi/v5"

// Deps 是请求记录模块的路由依赖（M6 只读查询台）。
type Deps struct {
	Service             RequestQueryService
	RoutingTraceService RoutingTraceService
}

// Register 注册请求记录模块路由。
func Register(r chi.Router, d Deps) {
	if d.RoutingTraceService != nil {
		rth := &routingTraceHandler{service: d.RoutingTraceService}
		r.Get("/requests/{requestId}/routing-decision", rth.get)
	}
	if d.Service != nil {
		rqh := &requestsHandler{service: d.Service}
		r.Get("/requests", rqh.list)
		// 详情按对外 request_id 定位；?include_internal=true 才回显内部错误详情。
		r.Get("/requests/{requestId}", rqh.get)
	}
}
