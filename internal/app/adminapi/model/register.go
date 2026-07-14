package model

import "github.com/go-chi/chi/v5"

// Deps 是模型模块的路由依赖（模型 CRUD/运维/基准价/目录）。
type Deps struct {
	Service        ModelService
	OpsService     ModelOpsService
	PriceService   ModelPriceService
	CatalogService CatalogService
}

// Register 注册模型模块路由。静态 /models/ops 与 /models/from-catalog 均置于 /models/{id} 之前。
func Register(r chi.Router, d Deps) {
	// §3.4 模型商品控制台：静态 /models/ops 必须在 /models/{id} 之前注册。
	if d.OpsService != nil {
		moh := &modelOpsHandler{service: d.OpsService}
		r.Get("/models/ops", moh.table)
		r.Get("/models/{id}/ops/detail", moh.detail)
		r.Get("/models/{id}/ops/channels", moh.channels)
		r.Get("/models/{id}/ops/performance", moh.performance)
		r.Get("/models/{id}/ops/requests", moh.requests)
	}

	if d.Service != nil {
		mh := &modelsHandler{service: d.Service}
		r.Get("/models", mh.list)
		r.Post("/models", mh.create)
		r.Get("/models/{id}", mh.get)
		r.Patch("/models/{id}", mh.update)
		// DELETE 物理删除录错的脏数据，级联清理自身价格/绑定/能力；已被请求/账务引用时返回 409。
		r.Delete("/models/{id}", mh.delete)
	}

	if d.PriceService != nil {
		mph := &modelPricesHandler{service: d.PriceService}
		// DEC-026：模型基准售价挂在 model 下；金额不可改，PATCH 调窗口/启停用价格 id 定位。
		r.Get("/models/{id}/prices", mph.list)
		r.Post("/models/{id}/prices", mph.create)
		r.Patch("/model-prices/{id}", mph.update)
	}

	// 模型目录：浏览 models.dev 目录 + 从目录采纳/刷新/更新提醒（采纳/刷新/提醒回读完整模型）。
	if d.CatalogService != nil && d.Service != nil {
		ch := &catalogHandler{catalog: d.CatalogService, models: d.Service}
		r.Get("/model-catalog", ch.list)
		// canonical_id 含 '/'，用通配段承载（如 /model-catalog/openai/gpt-4o）。
		r.Get("/model-catalog/*", ch.get)
		r.Post("/models/from-catalog", ch.adopt)
	}
}
