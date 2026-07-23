package channel

import (
	"github.com/go-chi/chi/v5"
)

// Deps 是渠道模块的路由依赖（渠道 CRUD/运维/检测/绑定/成本价/成本倍率/充值倍率）。
type Deps struct {
	Service               ChannelService
	OpsService            ChannelOpsService
	TestService           ChannelTestService
	ModelService          ChannelModelService
	PriceService          ChannelPriceService
	CostMultiplierService ChannelCostMultiplierService
	RechargeFactorService ChannelRechargeFactorService
	// Breaker 可选：P4 Redis 全局 breaker 只读运行态与复位（§8.4/§8.5）；nil 时 ops/runtime 与复位返回 503。
	Breaker BreakerRuntime
}

// Register 注册渠道模块路由。静态 /channels/ops* 与 /channels/adapter-keys 均置于 /channels/{id} 之前。
func Register(r chi.Router, d Deps) {
	// §3.3 渠道作战台只读运维聚合：静态 /channels/ops* 必须在 /channels/{id} 之前注册。
	if d.OpsService != nil {
		coh := &channelOpsHandler{service: d.OpsService}
		r.Get("/channels/ops", coh.table)
		r.Get("/channels/{id}/ops/detail", coh.detail)
		r.Get("/channels/{id}/ops/performance", coh.performance)
		r.Get("/channels/{id}/ops/errors", coh.errors)
		r.Get("/channels/{id}/ops/models", coh.models)
		r.Get("/channels/{id}/ops/routes", coh.routes)
	}

	// P4 §8.5：Channel breaker 只读运行态 + 显式复位（Redis 全局 breaker）。静态 /ops 段置于 /{id} 之前。
	{
		cbh := &channelBreakerHandler{service: d.Service, breaker: d.Breaker}
		r.Get("/channels/{id}/ops/runtime", cbh.runtime)
		r.Delete("/channels/{id}/ops/circuit-breaker", cbh.reset)
	}

	if d.Service != nil {
		ch := &channelsHandler{service: d.Service}
		// adapter_key 可选枚举（供前端下拉）：静态路径，置于 /channels/{id} 之前避免被通配吞掉。
		r.Get("/channels/adapter-keys", ch.adapterKeys)
		r.Get("/channels", ch.list)
		r.Post("/channels", ch.create)
		r.Get("/channels/{id}", ch.get)
		r.Patch("/channels/{id}", ch.update)
		r.Delete("/channels/{id}", ch.delete)
		r.Post("/channels/{id}/archive", ch.archive)
		r.Post("/channels/{id}/restore", ch.restore)
		// credential PUT 原子保存并同步返回五态验证结果，不回显 credential。
		r.Put("/channels/{id}/credential", ch.rotateCredential)
	}

	// 渠道主动检测（一键测渠道）：向真实上游发一个最小请求验证连通/凭据/模型，只报告不摘除。
	if d.TestService != nil {
		cth := &channelTestHandler{service: d.TestService}
		r.Post("/channels/{id}/test", cth.test)
		r.Get("/channels/{id}/test-logs", cth.testLogs)
	}

	if d.ModelService != nil {
		cmh := &channelModelsHandler{service: d.ModelService}
		// channel↔model 绑定是 channel 的子资源，用 {modelId} 定位 Unio 模型。
		r.Get("/channels/{id}/models", cmh.list)
		r.Post("/channels/{id}/models", cmh.create)
		r.Patch("/channels/{id}/models/{modelId}", cmh.update)
		r.Delete("/channels/{id}/models/{modelId}", cmh.delete)
	}

	if d.PriceService != nil {
		cph := &channelPricesHandler{service: d.PriceService}
		// 渠道-模型成本价（绝对覆盖）挂在 channel 下；价格不可删，PATCH 调窗口/启停用价格 id 定位。
		r.Get("/channels/{id}/prices", cph.list)
		r.Post("/channels/{id}/models/{modelId}/prices", cph.create)
		r.Patch("/channel-prices/{id}", cph.update)
	}

	// DEC-027：渠道价格倍率挂在 channel 下（默认 + 逐模型覆盖）；倍率不可改，PATCH 调窗口/启停用 id 定位。
	if d.CostMultiplierService != nil {
		ccmh := &channelCostMultipliersHandler{service: d.CostMultiplierService}
		r.Get("/channels/{id}/cost-multipliers", ccmh.list)
		r.Post("/channels/{id}/cost-multipliers", ccmh.create)
		r.Patch("/channel-cost-multipliers/{id}", ccmh.update)
	}

	// DEC-027：渠道充值倍率挂在 channel 下（账户级）；数值不可改，PATCH 调窗口/启停用 id 定位。
	if d.RechargeFactorService != nil {
		crfh := &channelRechargeFactorsHandler{service: d.RechargeFactorService}
		r.Get("/channels/{id}/recharge-factors", crfh.list)
		r.Post("/channels/{id}/recharge-factors", crfh.create)
		r.Patch("/channel-recharge-factors/{id}", crfh.update)
	}
}
