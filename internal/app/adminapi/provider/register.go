package provider

import "github.com/go-chi/chi/v5"

// Deps 是服务商模块的路由依赖。
type Deps struct {
	Service        ProviderService
	OpsService     ProviderOpsService
	BalanceService ProviderBalanceService
	Breaker        BreakerRuntime
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
	if d.BalanceService != nil {
		pbh := &providerBalanceHandler{service: d.BalanceService}
		r.Post("/providers/{id}/balance-adjustments", pbh.adjust)
		r.Get("/providers/{id}/ledger-entries", pbh.ledgerEntries)
		r.Get("/providers/{id}/cost-risks", pbh.costRisks)
		r.Get("/providers/{id}/cost-risks/summary", pbh.costRiskSummary)
	}

	if d.Service != nil {
		ph := &providersHandler{service: d.Service}
		rh := &runtimeHandler{service: d.Service, breaker: d.Breaker}
		r.Get("/providers", ph.list)
		r.Post("/providers", ph.create)
		r.Get("/providers/{id}/ops/runtime", rh.runtime)
		r.Delete("/providers/{id}/ops/circuit-breaker", rh.reset)
		r.Post("/providers/{id}/archive", ph.archive)
		r.Post("/providers/{id}/restore", ph.restore)
		r.Post("/providers/{id}/status", ph.updateStatus)
		r.Patch("/providers/{id}/origin", ph.updateOrigin)
		r.Get("/providers/{id}", ph.get)
		r.Patch("/providers/{id}", ph.update)
		// DELETE 物理删除录错的脏数据：名下有渠道或已被请求/账务引用时返回 409，提示改用停用。
		r.Delete("/providers/{id}", ph.delete)
	}
}
