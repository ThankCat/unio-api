package providerendpoint

import "github.com/go-chi/chi/v5"

// Deps 是 ProviderEndpoint 模块的路由依赖。
type Deps struct {
	Service ProviderEndpointService
	Breaker BreakerRuntime // 可空：Redis 缺失时 ops/runtime 与复位不可用
}

// Register 注册 ProviderEndpoint 模块路由（P4 §8.5）。
func Register(r chi.Router, d Deps) {
	if d.Service == nil {
		return
	}
	h := &handler{service: d.Service, breaker: d.Breaker}
	// 静态 /ops 段必须在 /{id} 之前注册（chi 静态优先，但保持清晰）。
	r.Get("/provider-endpoints/{id}/ops/runtime", h.runtime)
	r.Delete("/provider-endpoints/{id}/ops/circuit-breaker", h.resetBreaker)
	r.Get("/provider-endpoints", h.list)
	r.Post("/provider-endpoints", h.create)
	r.Get("/provider-endpoints/{id}", h.get)
	r.Patch("/provider-endpoints/{id}", h.update)
	r.Post("/provider-endpoints/{id}/status", h.updateStatus)
	r.Post("/provider-endpoints/{id}/base-url", h.updateBaseURL)
	r.Post("/provider-endpoints/{id}/routing", h.updateRouting)
}
