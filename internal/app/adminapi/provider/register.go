package provider

import "github.com/go-chi/chi/v5"

// Deps 是服务商模块的路由依赖。
type Deps struct {
	Service    ProviderService
	OpsService ProviderOpsService
}

// Register 注册服务商模块路由（CRUD + §3.2 服务商聚合视图）。
func Register(r chi.Router, d Deps) {
	// §3.2 服务商聚合视图：静态 /providers/ops 必须在 /providers/{id} 之前注册。
	if d.OpsService != nil {
		poh := &providerOpsHandler{service: d.OpsService}
		r.Get("/providers/ops", poh.table)
		r.Get("/providers/{id}/ops/detail", poh.detail)
		r.Get("/providers/{id}/ops/channel-catalog", poh.channelCatalog)
		r.Get("/providers/{id}/ops/model-catalog", poh.modelCatalog)
		r.Get("/providers/{id}/ops/route-catalog", poh.routeCatalog)
		r.Get("/providers/{id}/ops/channels", poh.channels)
		r.Get("/providers/{id}/ops/performance", poh.performance)
		r.Get("/providers/{id}/ops/errors", poh.errors)
	}

	if d.Service != nil {
		ph := &providersHandler{service: d.Service}
		r.Get("/providers", ph.list)
		r.Post("/providers", ph.create)
		r.Post("/providers/{id}/archive", ph.archive)
		r.Post("/providers/{id}/restore", ph.restore)
		r.Patch("/providers/{id}", ph.update)
		// DELETE 物理删除录错的脏数据：名下有渠道或已被请求/账务引用时返回 409，提示改用停用。
		r.Delete("/providers/{id}", ph.delete)
	}
}
