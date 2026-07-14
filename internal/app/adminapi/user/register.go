package user

import "github.com/go-chi/chi/v5"

// Deps 是用户/客户中心模块的路由依赖（用户只读 + API Key + 手工调额 + §3.7 客户中心聚合）。
type Deps struct {
	Service           UserService
	APIKeyService     APIKeyService
	AdjustmentService AdjustmentService
	OpsService        CustomerOpsService
}

// Register 注册用户模块路由。静态 /users/ops 置于 /users/{id} 之前。
func Register(r chi.Router, d Deps) {
	// §3.7 客户中心只读运维聚合：静态 ops 路径在 {id} 之前注册。
	if d.OpsService != nil {
		cuh := &customerOpsHandler{service: d.OpsService}
		r.Get("/users/ops", cuh.usersTable)
		r.Get("/users/{id}/ops/detail", cuh.userDetail)
		r.Get("/users/{id}/api-keys/ops/summary", cuh.apiKeysSummary)
		r.Get("/users/{id}/api-keys/ops", cuh.apiKeysTable)
	}

	// M7 客户管理：用户（只读列表/详情）、手工调额。
	if d.Service != nil {
		uh := &usersHandler{service: d.Service}
		r.Get("/users/{id}", uh.get)

		// 手工调额是用户的子资源：充值/扣款一律走账本留痕。
		if d.AdjustmentService != nil {
			ah := &adjustmentsHandler{service: d.AdjustmentService}
			r.Post("/users/{id}/balance-adjustments", ah.create)
		}
	}

	if d.APIKeyService != nil {
		akh := &apiKeysHandler{service: d.APIKeyService}
		// 创建挂在用户下；单把操作用扁平 /api-keys/{id} 定位。
		r.Post("/users/{id}/api-keys", akh.create)
		// PATCH 调启停/费用上限；DELETE 物理删除无调用历史的 Key（有历史→409 提示改用吊销）。
		r.Patch("/api-keys/{id}", akh.update)
		r.Delete("/api-keys/{id}", akh.delete)
		// 吊销是保留行与审计的软失效（不可逆），走子资源 POST，与硬删除区分。
		r.Post("/api-keys/{id}/revoke", akh.revoke)
	}
}
