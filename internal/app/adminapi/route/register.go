package route

import "github.com/go-chi/chi/v5"

// Deps 是线路模块的路由依赖。
type Deps struct {
	Service    RouteService
	OpsService RouteOpsService
}

// Register 注册线路模块路由。静态 /routes/ops 必须在 /routes/{id} 之前注册。
func Register(r chi.Router, d Deps) {
	// §3.5 线路路由作战台：静态 /routes/ops 必须在 /routes/{id} 之前注册。
	if d.OpsService != nil {
		roh := &routeOpsHandler{service: d.OpsService}
		r.Get("/routes/ops", roh.table)
		r.Get("/routes/{id}/ops/detail", roh.detail)
		r.Get("/routes/{id}/ops/reachable-models", roh.reachableModels)
		r.Get("/routes/{id}/ops/channel-pool", roh.channelPool)
		r.Get("/routes/{id}/ops/bindings", roh.bindings)
		r.Get("/routes/{id}/ops/performance", roh.performance)
		r.Get("/routes/{id}/ops/models", roh.models)
		r.Get("/routes/{id}/ops/requests", roh.requests)
	}

	if d.Service != nil {
		rh := &routesHandler{service: d.Service}
		// 线路（渠道商品）CRUD + 渠道池设置。
		r.Get("/routes", rh.list)
		r.Post("/routes", rh.create)
		r.Post("/routes/{id}/archive", rh.archive)
		r.Post("/routes/{id}/restore", rh.restore)
		r.Get("/routes/{id}", rh.get)
		r.Patch("/routes/{id}", rh.update)
		r.Delete("/routes/{id}", rh.delete)
	}
}
