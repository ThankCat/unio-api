package system

import (
	"net/http"
	"strconv"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi/adminhttp"

	"github.com/ThankCat/unio-gateway/internal/platform/config"
)

// systemConfigHandler 暴露进程级（env 生效）网关配置的只读视图。
//
// 设计意图（上线前全量修复 P0 前端）：凡运行期不可改的 env 阈值/兜底都要在前端「网关配置(只读)」面板可见，
// 杜绝后台静默。此处只回显非敏感运维阈值（兜底 token、补偿、HTTP 超时），绝不回显任何
// 凭据/密钥/DSN（DATABASE_URL、REDIS_PASSWORD、CREDENTIAL_MASTER_KEY、ADMIN_API_TOKEN 等）。
//
// 线路/渠道默认限流、渠道熔断、流式 idle 超时、渠道 429 冷却、凭据 401 阈值、默认渠道超时已迁移为
// 运行时配置（app_settings），从本只读面板移除，改在「运行时配置」可编辑面板管理（/settings）。
//
// 注意：admin-server 与 gateway-server 是独立进程，此处反映的是 admin 进程启动时读到的 env。
// 共享同一份 .env 时与 gateway 生效值一致；否则仅作近似参考（DTO note 已说明）。
type systemConfigHandler struct {
	gateway config.GatewayConfig
	worker  config.WorkerConfig
	http    config.HTTPConfig
}

// systemConfigEntryDTO 是一条配置项：人类可读标签 + 当前值 + 对应 env 变量名。
type systemConfigEntryDTO struct {
	Label string `json:"label"`
	Value string `json:"value"`
	Env   string `json:"env"`
}

// systemConfigGroupDTO 是一组同主题配置项。
type systemConfigGroupDTO struct {
	Title   string                 `json:"title"`
	Entries []systemConfigEntryDTO `json:"entries"`
}

// systemConfigDTO 是只读网关配置面板的完整响应体。
type systemConfigDTO struct {
	Note   string                 `json:"note"`
	Groups []systemConfigGroupDTO `json:"groups"`
}

// get 返回脱敏后的进程级网关配置分组。
func (h *systemConfigHandler) get(w http.ResponseWriter, _ *http.Request) {
	dto := systemConfigDTO{
		Note: "进程级 env 生效值（admin 进程启动时读取，脱敏）。与 gateway 共享同一 .env 时一致；已隐藏所有凭据/密钥/连接串。",
		Groups: []systemConfigGroupDTO{
			{
				Title: "授权与冻结",
				Entries: []systemConfigEntryDTO{
					{
						Label: "输出 token 冻结兜底上限",
						Value: strconv.FormatInt(h.gateway.MaxOutputTokensFallback, 10),
						Env:   "AUTHORIZATION_MAX_OUTPUT_TOKENS_FALLBACK",
					},
				},
			},
			{
				Title: "上游响应与流式",
				Entries: []systemConfigEntryDTO{
					{
						Label: "非流式响应体上限(字节)",
						Value: strconv.FormatInt(h.gateway.MaxUpstreamResponseBytes, 10),
						Env:   "GATEWAY_MAX_UPSTREAM_RESPONSE_MB",
					},
				},
			},
			{
				Title: "结算补偿 worker",
				Entries: []systemConfigEntryDTO{
					{Label: "启动超时", Value: h.worker.StartupTimeout.String(), Env: "WORKER_STARTUP_TIMEOUT"},
					{Label: "空闲轮询间隔", Value: h.worker.RunnerIdleInterval.String(), Env: "WORKER_RUNNER_IDLE_INTERVAL"},
					{Label: "补偿锁 TTL", Value: h.worker.SettlementRecoveryLockTTL.String(), Env: "WORKER_SETTLEMENT_RECOVERY_LOCK_TTL"},
					{Label: "补偿首跑延迟", Value: h.worker.SettlementRecoveryInitialDelay.String(), Env: "WORKER_SETTLEMENT_RECOVERY_INITIAL_DELAY"},
					{Label: "补偿结算超时", Value: h.worker.SettlementRecoverySettleTimeout.String(), Env: "WORKER_SETTLEMENT_RECOVERY_SETTLE_TIMEOUT"},
					{Label: "补偿最大重试次数", Value: strconv.FormatInt(int64(h.worker.SettlementRecoveryMaxAttempts), 10), Env: "WORKER_SETTLEMENT_RECOVERY_MAX_ATTEMPTS"},
					{Label: "补偿退避上限", Value: h.worker.SettlementRecoveryBackoffCap.String(), Env: "WORKER_SETTLEMENT_RECOVERY_BACKOFF_CAP"},
					{Label: "补偿单轮批量", Value: strconv.FormatInt(int64(h.worker.SettlementRecoveryBatchSize), 10), Env: "WORKER_SETTLEMENT_RECOVERY_BATCH_SIZE"},
				},
			},
			{
				Title: "孤儿预授权清扫 worker",
				Entries: []systemConfigEntryDTO{
					{Label: "判定年龄阈值", Value: h.worker.OrphanReservationSweepAgeThreshold.String(), Env: "WORKER_ORPHAN_RESERVATION_SWEEP_AGE_THRESHOLD"},
					{Label: "单轮扫描批量", Value: strconv.FormatInt(int64(h.worker.OrphanReservationSweepBatchSize), 10), Env: "WORKER_ORPHAN_RESERVATION_SWEEP_BATCH_SIZE"},
				},
			},
			{
				Title: "HTTP 服务",
				Entries: []systemConfigEntryDTO{
					{Label: "读超时", Value: h.http.ReadTimeout.String(), Env: "HTTP_READ_TIMEOUT"},
					{Label: "写超时", Value: h.http.WriteTimeout.String(), Env: "HTTP_WRITE_TIMEOUT"},
					{Label: "空闲超时", Value: h.http.IdleTimeout.String(), Env: "HTTP_IDLE_TIMEOUT"},
					{Label: "优雅关闭超时", Value: h.http.ShutdownTimeout.String(), Env: "HTTP_SHUTDOWN_TIMEOUT"},
					{Label: "JSON 体上限(字节)", Value: strconv.FormatInt(h.http.MaxJSONBodyBytes, 10), Env: "HTTP_MAX_JSON_BODY_MB"},
				},
			},
		},
	}

	adminhttp.WriteData(w, http.StatusOK, dto)
}
