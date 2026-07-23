// Package breakerstore 实现 P4 Redis 全局熔断（ROUTING_P4_GLOBAL_BREAKER_PROVIDER_PLAN §2.3-§2.6、§5）。
//
// 它把 Channel 与 Endpoint 熔断事实统一到 Redis：进程内不再保留熔断状态，多 Gateway 共享同一事实。
// 状态迁移由 Redis Lua 原子执行，使用 Redis TIME，不信任 Gateway 本机时钟；先校验后写、全有或
// 全无、first-terminal-wins。Redis/BreakerStore 基础设施故障统一 fail-closed（P4-D08）。
//
// 本包当前实现 P4 熔断的核心状态机与 AttemptPermit 生命周期（Channel/Endpoint 双触发熔断、
// half-open 双探测恢复、退避、仅流式 TTFT EWMA、Channel 在途并发租约）。入口 request-admission、
// admission-control 四维限额、Endpoint BaseURL/status 围栏、runtime-control 发布与完整性 epoch
// 恢复属于同一 BreakerStore 契约的其余能力族，按计划 §5.3 分阶段接入。
package breakerstore

import (
	"math"
	"time"
)

// Scope 是熔断作用域：Channel 或 Endpoint。二者共用同一状态机框架（§2.5）。
type Scope string

const (
	ScopeChannel  Scope = "channel"
	ScopeEndpoint Scope = "endpoint"
)

// BreakerState 是熔断状态机对外暴露的稳定字符串。
type BreakerState string

const (
	StateClosed   BreakerState = "closed"
	StateOpen     BreakerState = "open"
	StateHalfOpen BreakerState = "half_open"
)

// RequestMode 是本次 attempt 的流式模式，固化进服务端 permit record；只有 stream 才可能更新 TTFT。
type RequestMode string

const (
	ModeStream    RequestMode = "stream"
	ModeNonStream RequestMode = "non_stream"
)

func (m RequestMode) valid() bool {
	return m == ModeStream || m == ModeNonStream
}

// UpstreamOperation 是稳定的上游 operation 枚举（固化进 permit，用于审计与 TPM 口径）。
type UpstreamOperation string

const (
	OpChatCompletions  UpstreamOperation = "chat_completions"
	OpResponses        UpstreamOperation = "responses"
	OpResponsesCompact UpstreamOperation = "responses_compact"
	OpMessages         UpstreamOperation = "messages"
)

func (o UpstreamOperation) valid() bool {
	switch o {
	case OpChatCompletions, OpResponses, OpResponsesCompact, OpMessages:
		return true
	default:
		return false
	}
}

// Outcome 是 Finish 提交的真实上游结果分类（已由调用方完成稳定 attribution/eligibility，§2.5.8）。
//
// 只有 attributable 到该作用域的 eligible 结果才进入 breaker 分子/分母；平台/Store/DB/adapter 本地
// 错误、客户取消、401/403/429、400/404/405/422 都不进入（由调用方映射为 OutcomeIgnored）。
type Outcome string

const (
	// OutcomeEligibleSuccess 归因到该作用域的真实上游成功；清空连续失败计数。
	OutcomeEligibleSuccess Outcome = "eligible_success"
	// OutcomeEligibleFailure 归因到该作用域的真实上游失败；进入分子并累加连续失败。
	OutcomeEligibleFailure Outcome = "eligible_failure"
	// OutcomeIgnored 非该作用域责任的结果；既不增加失败，也不冒充成功，也不清连续失败。
	OutcomeIgnored Outcome = "ignored"
)

func (o Outcome) valid() bool {
	switch o {
	case OutcomeEligibleSuccess, OutcomeEligibleFailure, OutcomeIgnored:
		return true
	default:
		return false
	}
}

// EndpointEvidenceCategory 是需要跨 Channel、跨模型短窗证据后才可扩大到 Endpoint 的错误分类。
// 空值表示本次 Finish 没有条件归因证据；不同分类使用彼此隔离的 Redis 集合，不能拼样本。
type EndpointEvidenceCategory string

const (
	EndpointEvidenceNone              EndpointEvidenceCategory = ""
	EndpointEvidenceHTTP500           EndpointEvidenceCategory = "http_500"
	EndpointEvidenceFirstTokenTimeout EndpointEvidenceCategory = "first_token_timeout"
	EndpointEvidenceBodyReadTimeout   EndpointEvidenceCategory = "body_read_timeout"
)

func (c EndpointEvidenceCategory) valid() bool {
	switch c {
	case EndpointEvidenceNone,
		EndpointEvidenceHTTP500,
		EndpointEvidenceFirstTokenTimeout,
		EndpointEvidenceBodyReadTimeout:
		return true
	default:
		return false
	}
}

// Disposition 是 Finish/Abort 对某作用域 breaker/TTFT 应用与否的结果（写入 request_attempts）。
type Disposition string

const (
	DispositionApplied          Disposition = "applied"
	DispositionStaleRevision    Disposition = "stale_revision"
	DispositionStaleStatusRev   Disposition = "stale_status_revision"
	DispositionStaleConfigRev   Disposition = "stale_config_revision"
	DispositionStaleGeneration  Disposition = "stale_generation"
	DispositionRuntimeStateLost Disposition = "runtime_state_lost"
	DispositionStaleIntegrity   Disposition = "stale_integrity_epoch"
	DispositionRuntimeSyncReq   Disposition = "runtime_sync_required"
	DispositionExpired          Disposition = "expired"
	DispositionUnknownPermit    Disposition = "unknown_permit"
	DispositionTerminalConflict Disposition = "terminal_conflict"
	DispositionResultUnknown    Disposition = "result_unknown"
	DispositionNotApplicable    Disposition = "not_applicable"
)

// AdmissionMode 是 AcquireAttempt 的显式准入结果，只允许 permit|denied（§5.5.4）。
type AdmissionMode string

const (
	AdmissionPermit AdmissionMode = "permit"
	AdmissionDenied AdmissionMode = "denied"
)

// DeniedReason 是业务拒绝的稳定原因；基础设施故障使用 ReasonBreakerStoreUnavailable 并终止整次 fallback。
type DeniedReason string

const (
	ReasonOpen                    DeniedReason = "open"
	ReasonHalfOpenBusy            DeniedReason = "half_open_busy"
	ReasonConcurrencyLimited      DeniedReason = "concurrency_limited"
	ReasonRateLimited             DeniedReason = "rate_limited"
	ReasonModelPermissionPaused   DeniedReason = "model_permission_paused"
	ReasonStaleRevision           DeniedReason = "stale_revision"
	ReasonStaleStatusRevision     DeniedReason = "stale_status_revision"
	ReasonStaleConfigRevision     DeniedReason = "stale_config_revision"
	ReasonRuntimeSyncRequired     DeniedReason = "runtime_sync_required"
	ReasonRuntimeSyncPending      DeniedReason = "runtime_sync_pending"
	ReasonStaleSettingRevision    DeniedReason = "stale_setting_revision"
	ReasonRuntimeStateLost        DeniedReason = "runtime_state_lost"
	ReasonStaleIntegrityEpoch     DeniedReason = "stale_integrity_epoch"
	ReasonUnknownRequestAdmission DeniedReason = "unknown_request_admission"
	ReasonBreakerStoreUnavailable DeniedReason = "breaker_store_unavailable"
)

// Config 是 P4 gateway.circuit_breaker 的运行参数（§4.8 目标形状）。
type Config struct {
	Enabled bool

	WindowMs             int64
	MinRequests          int
	FailureRatio         float64
	ConsecutiveFailures  int
	ConsecutiveWindowMs  int64
	HalfOpenSuccesses    int
	AttemptPermitTTLMs   int64
	AttemptRenewMs       int64
	AttemptTerminalTTLMs int64
	OpenDurationsMs      []int64

	EndpointAmbiguousDistinctChannels int
	EndpointAmbiguousDistinctModels   int
}

// DefaultConfig 返回 §4.8 的目标默认配置。
func DefaultConfig() Config {
	return Config{
		Enabled:                           true,
		WindowMs:                          30000,
		MinRequests:                       20,
		FailureRatio:                      0.5,
		ConsecutiveFailures:               3,
		ConsecutiveWindowMs:               10000,
		HalfOpenSuccesses:                 2,
		AttemptPermitTTLMs:                30000,
		AttemptRenewMs:                    10000,
		AttemptTerminalTTLMs:              300000,
		OpenDurationsMs:                   []int64{15000, 30000, 60000, 120000, 300000},
		EndpointAmbiguousDistinctChannels: 2,
		EndpointAmbiguousDistinctModels:   2,
	}
}

// AttemptPermit 是一次真实上游调用前取得的不可伪造、不可复用准入凭据（§2.2）。
//
// 服务端记录为权威；调用方不得自行声明 resource token。permit_id/request_admission_id 不进入公开
// API 或 Prometheus label；routing trace 只保存安全摘要。
type AttemptPermit struct {
	PermitID           string
	RequestAdmissionID string
	IntegrityEpoch     string
	IntegrityRevision  int64

	EndpointID int64
	ChannelID  int64

	EndpointBaseURLRevision int64
	EndpointStatusRevision  int64
	ChannelConfigRevision   int64

	ModelID           int64
	UpstreamOperation UpstreamOperation
	RequestMode       RequestMode

	EndpointStateGeneration int64
	ChannelStateGeneration  int64
	EndpointHalfOpenProbe   bool
	ChannelHalfOpenProbe    bool

	PermitTTLMs   int64
	RenewMs       int64
	TerminalTTLMs int64

	AcquiredAtMs int64
	LeaseUntilMs int64
}

// AttemptAdmission 是 AcquireAttempt 的返回：permit 模式携带 Permit，denied 模式携带 Reason。
type AttemptAdmission struct {
	Mode   AdmissionMode
	Permit *AttemptPermit
	Reason DeniedReason
}

// FinishOutcome 是 Finish 提交的真实结果：分别对 Endpoint / Channel 给出 attribution，
// 以及可选的流式 FirstToken 样本（仅 stream permit 且样本有效时更新 TTFT EWMA）。
type FinishOutcome struct {
	EndpointOutcome Outcome
	ChannelOutcome  Outcome

	// EndpointEvidence 表示本次 Channel failure 需要满足短窗 distinct Channel + model 门槛后，
	// 才能在同一个 Redis Finish 中原子升级为 Endpoint eligible_failure。
	EndpointEvidence EndpointEvidenceCategory

	// FirstTokenMs 仅在 stream permit 且观测到有效 FirstToken 时为非 nil；非流式必须为 nil。
	FirstTokenMs *int64

	// ChannelTPMActual 为权威 usage 的真实 Channel TPM（cache-aware actual）；非 nil 时按 actual-estimate
	// 对账原始桶，nil 时释放 TPM 预占（§2.12.8）。仅在启用 admission control 的 permit 生效。
	ChannelTPMActual *int64
}

// FinishResult 汇报两个作用域各自的 applied/stale disposition（写入 request_attempts）。
type FinishResult struct {
	EndpointDisposition Disposition
	ChannelDisposition  Disposition
}

// ScopeSnapshot 是某作用域当前只读运行态（供 Admin 与 balanced 评分读取，不推进状态机）。
type ScopeSnapshot struct {
	Scope                    Scope
	ID                       int64
	Exists                   bool
	State                    BreakerState
	OpenRemainingMs          int64
	OpenLevel                int
	WindowStartedAtMs        int64
	EligibleSuccesses        int64
	EligibleFailures         int64
	ConsecutiveFailures      int64
	ErrorRate                float64
	SampleCount              int64
	TTFTEWMAMs               float64 // Channel only
	TTFTSamples              int64   // Channel only
	LastTransitionAtMs       int64
	LastFailureCategory      string
	ControlPresent           bool   // Endpoint only
	EffectiveStatus          string // Endpoint only
	BaseURLRevision          int64  // Endpoint current / Channel bound Endpoint revision
	StatusRevision           int64  // Endpoint current / Channel bound Endpoint revision
	PendingBaseURLRevision   int64  // Endpoint only: pending BaseURL revision, 0 when absent
	PendingStatusRevision    int64  // Endpoint only: pending effective-status revision, 0 when absent
	BaseURLRevisionState     string // Endpoint only: active|pending
	StatusRevisionState      string // Endpoint only: active|pending
	StateGeneration          int64
	BaseURLFenceGeneration   int64 // Endpoint only
	StatusFenceGeneration    int64 // Endpoint only
	ProviderEndpointID       int64 // Channel only
	ChannelConfigRevision    int64 // Channel only
	HalfOpenBusy             bool
	HalfOpenLeaseRemainingMs int64
}

// SnapshotCandidateInput 是批量路由快照所需的稳定候选身份。
// SnapshotMany 用它判断 Channel state 是否仍属于同一 Endpoint 与配置代际。
type SnapshotCandidateInput struct {
	EndpointID               int64
	ChannelID                int64
	EndpointBaseURLRevision  int64
	EndpointStatusRevision   int64
	ChannelConfigRevision    int64
	ChannelAdmissionRevision int64
}

// SnapshotManyInput 固化一次客户请求在 PostgreSQL 强一致读取到的完整运行态版本。
// SnapshotMany 只接受这些 expected revisions，不接受调用方计算后的限额或评分参数。
type SnapshotManyInput struct {
	IntegrityEpoch            string
	IntegrityRevision         int64
	ChannelRateRevision       int64
	GlobalConcurrencyRevision int64
	CircuitBreakerRevision    int64
	RoutingBalanceRevision    int64
	ModelID                   int64
	Candidates                []SnapshotCandidateInput
}

// CandidateSnapshotStatus 描述只读快照相对 PostgreSQL 候选身份的稳定判定。
type CandidateSnapshotStatus string

const (
	CandidateSnapshotCurrent               CandidateSnapshotStatus = "current"
	CandidateSnapshotNoSample              CandidateSnapshotStatus = "no_sample"
	CandidateSnapshotStaleRevision         CandidateSnapshotStatus = "stale_revision"
	CandidateSnapshotStaleStatusRevision   CandidateSnapshotStatus = "stale_status_revision"
	CandidateSnapshotStaleConfigRevision   CandidateSnapshotStatus = "stale_config_revision"
	CandidateSnapshotRuntimeSyncRequired   CandidateSnapshotStatus = "runtime_sync_required"
	CandidateSnapshotRuntimeSyncPending    CandidateSnapshotStatus = "runtime_sync_pending"
	CandidateSnapshotOpen                  CandidateSnapshotStatus = "open"
	CandidateSnapshotHalfOpen              CandidateSnapshotStatus = "half_open"
	CandidateSnapshotHalfOpenBusy          CandidateSnapshotStatus = "half_open_busy"
	CandidateSnapshotRateLimited           CandidateSnapshotStatus = "rate_limited"
	CandidateSnapshotModelPermissionPaused CandidateSnapshotStatus = "model_permission_paused"
	CandidateSnapshotEndpointDisabled      CandidateSnapshotStatus = "endpoint_disabled"
)

// CapacityUsage 是 Redis stable resource 的同一时点只读事实。Limit=0 表示不限。
type CapacityUsage struct {
	Used  int64
	Limit int64
}

// RoutingBalanceSnapshot 是本次 SnapshotMany 的 active routing-balance 线性化点。
type RoutingBalanceSnapshot struct {
	Revision             int64
	TTFTTargetMs         int64
	TTFTWeight           float64
	CostWeight           float64
	MinimumRoutingFactor float64
}

// CandidateSnapshot 是同一 Redis Lua 时点读取的 Endpoint/Channel 运行态，保持输入顺序。
type CandidateSnapshot struct {
	Candidate                   SnapshotCandidateInput
	Status                      CandidateSnapshotStatus
	Endpoint                    ScopeSnapshot
	Channel                     ScopeSnapshot
	Concurrency                 CapacityUsage
	RPM                         CapacityUsage
	RPD                         CapacityUsage
	TPM                         CapacityUsage
	CooldownRemainingMs         int64
	ModelPermissionPaused       bool
	ModelPermissionRecheckState string
}

// SnapshotManyResult 保持候选输入顺序，并只返回一次共享 routing-balance payload。
type SnapshotManyResult struct {
	Candidates                []CandidateSnapshot
	IntegrityRevision         int64
	RouteRateRevision         int64
	ChannelRateRevision       int64
	GlobalConcurrencyRevision int64
	CircuitBreakerRevision    int64
	RoutingBalance            RoutingBalanceSnapshot
}

// valid 校验 breaker 配置，确保任何非法数值都在调用 Redis 前失败。
func (c Config) valid() bool {
	if c.WindowMs <= 0 || c.MinRequests < 2 || math.IsNaN(c.FailureRatio) || math.IsInf(c.FailureRatio, 0) || c.FailureRatio <= 0 || c.FailureRatio > 1 {
		return false
	}
	if c.ConsecutiveFailures < 1 || c.ConsecutiveWindowMs <= 0 || c.HalfOpenSuccesses < 2 {
		return false
	}
	if c.AttemptPermitTTLMs <= 0 || c.AttemptRenewMs <= 0 || c.AttemptTerminalTTLMs < c.AttemptPermitTTLMs {
		return false
	}
	if c.AttemptRenewMs > c.AttemptPermitTTLMs/3 {
		return false
	}
	if len(c.OpenDurationsMs) == 0 {
		return false
	}
	for i, duration := range c.OpenDurationsMs {
		if duration <= 0 || (i > 0 && duration < c.OpenDurationsMs[i-1]) {
			return false
		}
	}
	if c.EndpointAmbiguousDistinctChannels < 2 || c.EndpointAmbiguousDistinctModels < 2 {
		return false
	}
	return true
}

// renewInterval 返回续租间隔（供调用方的 renewer 使用）。
func (p AttemptPermit) renewInterval() time.Duration {
	return time.Duration(p.RenewMs) * time.Millisecond
}
