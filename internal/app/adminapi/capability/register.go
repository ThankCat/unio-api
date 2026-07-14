package capability

import "github.com/go-chi/chi/v5"

// Deps 是能力模块的路由依赖（能力 key 字典 + 模型能力 + models.dev 同步 + adapter 画像）。
type Deps struct {
	Service     CapabilityService
	SyncService CapabilitySyncService
	SeedService CapabilitySeedService
}

// Register 注册能力模块路由。
func Register(r chi.Router, d Deps) {
	// M5 能力管理：模型能力（手工覆盖）CRUD + 能力 key 注册表。
	if d.Service != nil {
		cah := &capabilitiesHandler{service: d.Service}
		r.Get("/capability/keys", cah.listKeys)
		r.Post("/capability/keys", cah.createKey)
		r.Put("/capability/keys/{key}", cah.updateKey)
		r.Delete("/capability/keys/{key}", cah.deleteKey)
		// 模型能力挂在 model 下；写入用 PUT {key} 幂等 upsert，DELETE 撤销。
		r.Get("/models/{id}/capabilities", cah.listModelCapabilities)
		// 批量整表覆盖（一次保存多条，DEC-024 §6.2）；per-key PUT/DELETE 保留兼容。
		r.Put("/models/{id}/capabilities", cah.replaceModelCapabilities)
	}

	// M5 models.dev 同步：内联触发（dry-run 预览/实际应用）+ 最近任务展示。
	if d.SyncService != nil {
		csh := &capabilitySyncHandler{service: d.SyncService}
		r.Get("/capability/sync-jobs", csh.listJobs)
		r.Post("/capability/sync-jobs", csh.trigger)
	}

	// M5 adapter 画像：列出可物化画像 + 对指定模型物化（source=adapter_seed）。
	if d.SeedService != nil {
		cseh := &capabilitySeedHandler{service: d.SeedService}
		r.Get("/capability/adapter-profiles", cseh.listProfiles)
		r.Post("/capability/adapter-seed-jobs", cseh.materialize)
	}
}
