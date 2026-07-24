package breakerstore

import (
	"strconv"
	"strings"
)

// keyBuilder 生成版本化 namespace key（§5.1），避免与 P3 进程内状态或未来结构冲突。
type keyBuilder struct {
	base   string // "<ns>:"
	prefix string // "<ns>:breaker:v2:"
}

func newKeyBuilder(namespace string) keyBuilder {
	namespace = strings.Trim(namespace, ":")
	if namespace == "" {
		namespace = "unio"
	}
	return keyBuilder{base: namespace + ":", prefix: namespace + ":breaker:v2:"}
}

func (k keyBuilder) channel(id int64) string {
	return k.prefix + "channel:" + strconv.FormatInt(id, 10)
}

func (k keyBuilder) origin(id int64) string {
	return k.prefix + "origin:" + strconv.FormatInt(id, 10)
}

func (k keyBuilder) permit(permitID string) string {
	return k.prefix + "permit:" + permitID
}

// originEvidence 保存 Origin HTTP 500/timeout 跨渠道、跨模型证据的短 TTL 集合（§5.1）。
// category 只能是 http_500 | first_token_timeout | body_read_timeout。
func (k keyBuilder) originEvidenceChannels(id int64, category string) string {
	return k.prefix + "origin-evidence:" + strconv.FormatInt(id, 10) + ":" + category + ":channels"
}

func (k keyBuilder) originEvidenceModels(id int64, category string) string {
	return k.prefix + "origin-evidence:" + strconv.FormatInt(id, 10) + ":" + category + ":models"
}

// channel429Cooldown 是所有 Gateway 共享的 Channel 429 冷却 key（§2.4.1、§5.1）；
// 使用独立 namespace，不复用 breaker 窗口，Reset breaker 不清它。
func (k keyBuilder) channel429Cooldown(channelID int64) string {
	// 保持与 §5.1 一致的 cooldown:v2 命名（独立于 breaker:v2 前缀）。
	return trimBreaker(k.prefix) + "cooldown:v2:channel-429:" + strconv.FormatInt(channelID, 10)
}

// channelModelPermission 是按 (channel_id, model_id) 保存的 403 权限暂停 key（§2.4.2、§5.1）。
func (k keyBuilder) channelModelPermission(channelID, modelID int64) string {
	return trimBreaker(k.prefix) + "permission:v1:channel-model:" + strconv.FormatInt(channelID, 10) + ":" + strconv.FormatInt(modelID, 10)
}

// permissionRecheckQueue 保存需要自动复检的 Channel-Model permission key。
// member 使用完整 permission key，天然按绑定去重；score 是 Redis TIME 计算的下次可领取时间。
func (k keyBuilder) permissionRecheckQueue() string {
	return trimBreaker(k.prefix) + "permission:v1:recheck-queue"
}

// trimBreaker 从 "<ns>:breaker:v2:" 还原出 "<ns>:"，供 cooldown/permission 使用各自的版本化前缀。
func trimBreaker(prefix string) string {
	return strings.TrimSuffix(prefix, "breaker:v2:")
}

// ---- admission control（§5.1）----

func (k keyBuilder) admissionRouteRate() string {
	return k.base + "admission:v1:route-rate-limits"
}

func (k keyBuilder) admissionChannelRate() string {
	return k.base + "admission:v1:channel-rate-limits"
}

func (k keyBuilder) admissionGlobalConcurrency() string {
	return k.base + "admission:v1:global-concurrency"
}

func (k keyBuilder) admissionChannel(channelID int64) string {
	return k.base + "admission:v1:channel:" + strconv.FormatInt(channelID, 10)
}

func (k keyBuilder) admissionRequest(requestAdmissionID string) string {
	return k.base + "admission:v1:request:" + requestAdmissionID
}

func (k keyBuilder) admissionOp(token string) string {
	return k.base + "admission:v1:op:" + token
}

// route-user 稳定窗口桶 / 并发 active set（不带 revision，§5.2）。
func (k keyBuilder) requestRPMBucket(routeID, userID, minuteBucket int64) string {
	return k.base + "admission:v1:ru-rpm:" + i(routeID) + ":" + i(userID) + ":" + i(minuteBucket)
}

func (k keyBuilder) requestRPDBucket(routeID, userID, dayBucket int64) string {
	return k.base + "admission:v1:ru-rpd:" + i(routeID) + ":" + i(userID) + ":" + i(dayBucket)
}

func (k keyBuilder) requestTPMBucket(routeID, userID, minuteBucket int64) string {
	return k.base + "admission:v1:ru-tpm:" + i(routeID) + ":" + i(userID) + ":" + i(minuteBucket)
}

func (k keyBuilder) requestConcurrency(routeID, userID int64) string {
	return k.base + "admission:v1:ru-conc:" + i(routeID) + ":" + i(userID)
}

// Channel 稳定窗口桶（RPM/RPD/TPM）——不带 revision。
func (k keyBuilder) channelRPMBucket(channelID, minuteBucket int64) string {
	return k.base + "admission:v1:ch-rpm:" + i(channelID) + ":" + i(minuteBucket)
}

func (k keyBuilder) channelRPMBucketPrefix(channelID int64) string {
	return k.base + "admission:v1:ch-rpm:" + i(channelID) + ":"
}

func (k keyBuilder) channelRPDBucket(channelID, dayBucket int64) string {
	return k.base + "admission:v1:ch-rpd:" + i(channelID) + ":" + i(dayBucket)
}

func (k keyBuilder) channelRPDBucketPrefix(channelID int64) string {
	return k.base + "admission:v1:ch-rpd:" + i(channelID) + ":"
}

func (k keyBuilder) channelTPMBucket(channelID, minuteBucket int64) string {
	return k.base + "admission:v1:ch-tpm:" + i(channelID) + ":" + i(minuteBucket)
}

func (k keyBuilder) channelTPMBucketPrefix(channelID int64) string {
	return k.base + "admission:v1:ch-tpm:" + i(channelID) + ":"
}

// ---- runtime-control（circuit_breaker / routing_balance）与完整性 marker（§5.1）----

func (k keyBuilder) runtimeControlSetting(settingKey string) string {
	return k.base + "runtime-control:v1:setting:" + settingKey
}

func (k keyBuilder) runtimeControlOp(token string) string {
	return k.base + "runtime-control:v1:op:" + token
}

func (k keyBuilder) stateIntegrityMarker() string {
	return k.base + "runtime-control:v1:state-integrity-marker"
}

// runtimeInfrastructureFault is a persistent, namespace-wide admission fence shared by every
// Gateway. It is removed only after a full runtime-control reconciliation and readiness proof.
func (k keyBuilder) runtimeInfrastructureFault() string {
	return k.base + "runtime-control:v1:infrastructure-fault"
}

// runtimeReconciliationProof stores the Redis server run_id observed by the last successful full
// reconciliation. Admission scripts compare it with INFO server before making any resource write.
func (k keyBuilder) runtimeReconciliationProof() string {
	return k.base + "runtime-control:v1:reconciliation-proof"
}

func i(v int64) string { return strconv.FormatInt(v, 10) }
