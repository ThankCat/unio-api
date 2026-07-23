package system

import (
	"github.com/go-chi/chi/v5"

	"github.com/ThankCat/unio-gateway/internal/platform/config"
)

// Deps 是系统设置模块的路由依赖（结算补偿任务只读视图 + 运行时配置 + 进程级配置只读面板）。
type Deps struct {
	RecoveryJobService        RecoveryJobQueryService
	ProviderSettingsService   ProviderSettingsService
	RuntimeDiagnosticsService RuntimeDiagnosticsService

	// 进程级 env 生效阈值（脱敏）快照，恒有效，故 /system/config 无条件注册。
	GatewayConfig config.GatewayConfig
	WorkerConfig  config.WorkerConfig
	HTTPConfig    config.HTTPConfig
}

// Register 注册系统设置模块路由。
func Register(r chi.Router, d Deps) {
	if d.RuntimeDiagnosticsService != nil {
		h := &runtimeDiagnosticsHandler{service: d.RuntimeDiagnosticsService}
		r.Get("/system/runtime-diagnostics", h.get)
	}

	// M8 系统/任务/健康：结算补偿任务只读视图（列表脱敏内部详情，详情按 ?include_internal 回显）。
	if d.RecoveryJobService != nil {
		rjh := &recoveryJobsHandler{service: d.RecoveryJobService}
		r.Get("/system/settlement-recovery-jobs", rjh.list)
		r.Get("/system/settlement-recovery-jobs/{id}", rjh.get)
	}

	// 运行时配置（可编辑、免重启生效）：通用 List/PUT 从注册表驱动面板；beta 专用端点为便捷 typed 入口。
	if d.ProviderSettingsService != nil {
		psh := &providerSettingsHandler{service: d.ProviderSettingsService}
		r.Get("/settings", psh.listSettings)
		r.Put("/settings/{key}", psh.putSetting)
		r.Get("/provider-settings/anthropic/beta-policy", psh.getAnthropicBeta)
		r.Put("/provider-settings/anthropic/beta-policy", psh.putAnthropicBeta)
	}

	// 系统配置只读面板：进程级 env 生效阈值（脱敏），让运营在前端看到所有不可运行期改的阈值。
	systemConfig := &systemConfigHandler{
		gateway: d.GatewayConfig,
		worker:  d.WorkerConfig,
		http:    d.HTTPConfig,
	}
	r.Get("/system/config", systemConfig.get)
}
