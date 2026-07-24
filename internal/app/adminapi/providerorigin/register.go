package providerorigin

import "github.com/go-chi/chi/v5"

// Deps 是 ProviderOrigin 模块的路由依赖。
type Deps struct {
	Service ProviderOriginService
	Breaker BreakerRuntime // 可空：Redis 缺失时 ops/runtime 与复位不可用
}

// Register 注册 ProviderOrigin 模块路由（P4 §8.5）。
func Register(r chi.Router, d Deps) {
	if d.Service == nil {
		return
	}
	h := &handler{service: d.Service, breaker: d.Breaker}
	// 静态 /ops 段必须在 /{id} 之前注册（chi 静态优先，但保持清晰）。
	r.Get("/provider-origins/{id}/ops/runtime", h.runtime)
	r.Delete("/provider-origins/{id}/ops/circuit-breaker", h.resetBreaker)
	r.Get("/provider-origins", h.list)
	r.Post("/provider-origins", h.create)
	r.Get("/provider-origins/{id}", h.get)
	r.Patch("/provider-origins/{id}", h.update)
	r.Post("/provider-origins/{id}/status", h.updateStatus)
	r.Post("/provider-origins/{id}/base-url", h.updateBaseURL)
	r.Post("/provider-origins/{id}/routing", h.updateRouting)
}
