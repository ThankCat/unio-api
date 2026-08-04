// Package breakerstore 实现 Redis 全局熔断与原子准入控制。
//
// 它把 Provider 与 Channel 熔断事实统一到 Redis：进程内不再保留熔断状态，多 Gateway 共享同一事实。
// 状态迁移由 Redis Lua 原子执行，使用 Redis TIME，不信任 Gateway 本机时钟；先校验后写、全有或
// 全无、first-terminal-wins。Redis/BreakerStore 基础设施故障统一 fail-closed。
//
// 本包实现熔断核心状态机与 AttemptPermit 生命周期（Provider/Channel 双触发熔断、
// half-open 双探测恢复、退避、仅流式 TTFT EWMA、Channel 在途并发租约）。入口 request-admission、
// admission-control 四维限额、Provider origin/status 围栏、runtime-control 发布与完整性 epoch
// 恢复属于同一 BreakerStore 契约的其余能力族。
package breakerstore

// Scope 是熔断作用域：Provider 或 Channel。二者共用同一状态机框架（§2.5）。
type Scope string

const (
	ScopeChannel  Scope = "channel"
	ScopeProvider Scope = "provider"
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

// UpstreamEndpoint 是稳定的上游 operation 枚举（固化进 permit，用于审计与 TPM 口径）。
type UpstreamEndpoint string

const (
	EndpointChatCompletions  UpstreamEndpoint = "chat_completions"
	EndpointResponses        UpstreamEndpoint = "responses"
	EndpointResponsesCompact UpstreamEndpoint = "responses_compact"
	EndpointMessages         UpstreamEndpoint = "messages"
)

func (o UpstreamEndpoint) valid() bool {
	switch o {
	case EndpointChatCompletions, EndpointResponses, EndpointResponsesCompact, EndpointMessages:
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

// ProviderEvidenceCategory 是需要跨 Channel、跨模型短窗证据后才可扩大到 Provider 的错误分类。
// 空值表示本次 Finish 没有条件归因证据；不同分类使用彼此隔离的 Redis 集合，不能拼样本。
type ProviderEvidenceCategory string

const (
	ProviderEvidenceNone              ProviderEvidenceCategory = ""
	ProviderEvidenceHTTP500           ProviderEvidenceCategory = "http_500"
	ProviderEvidenceFirstTokenTimeout ProviderEvidenceCategory = "first_token_timeout"
	ProviderEvidenceBodyReadTimeout   ProviderEvidenceCategory = "body_read_timeout"
)

func (c ProviderEvidenceCategory) valid() bool {
	switch c {
	case ProviderEvidenceNone,
		ProviderEvidenceHTTP500,
		ProviderEvidenceFirstTokenTimeout,
		ProviderEvidenceBodyReadTimeout:
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
	ReasonOpen         DeniedReason = "open"
	ReasonHalfOpenBusy DeniedReason = "half_open_busy"
	// ReasonConcurrencyFull 是唯一允许进入全池短等的拒绝原因（§9.3）。
	ReasonConcurrencyFull DeniedReason = "concurrency_full"
	// ReasonCooldown 表示渠道处于上游真实 429 冷却；绝不允许伪装成并发满进入等待（§6.3/§9.3）。
	ReasonCooldown                DeniedReason = "cooldown"
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

// Config 是 gateway.circuit_breaker 的运行参数。
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

	ProviderAmbiguousDistinctChannels int
	ProviderAmbiguousDistinctModels   int
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
		ProviderAmbiguousDistinctChannels: 2,
		ProviderAmbiguousDistinctModels:   2,
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

	ProviderID            int64
	ChannelID             int64
	RouteID               int64
	RouteChannelRPDBucket string

	OriginRevision         int64
	ProviderStatusRevision int64
	ChannelConfigRevision  int64

	ModelID          int64
	UpstreamEndpoint UpstreamEndpoint
	RequestMode      RequestMode

	ProviderStateGeneration int64
	ChannelStateGeneration  int64
	ProviderHalfOpenProbe   bool
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
	// CooldownRemainingMs 仅在 Reason=cooldown 时为正，供全池均冷却时给出准确 Retry-After（§9.5）。
	CooldownRemainingMs int64
}

// FinishOutcome 是 Finish 提交的真实结果：分别对 Provider / Channel 给出 attribution。
type FinishOutcome struct {
	ProviderOutcome Outcome
	ChannelOutcome  Outcome

	RequestWriteState       RequestWriteState
	ResponseHeadersReceived bool
	FirstTokenEligible      bool

	// ProviderEvidence 表示本次 Channel failure 需要满足短窗 distinct Channel + model 门槛后，
	// 才能在同一个 Redis Finish 中原子升级为 Provider eligible_failure。
	ProviderEvidence ProviderEvidenceCategory

	// ActualTotalTokens 是完整、可靠且包含 cache read/write 的实际总量；nil 时保留输入估算。
	ActualTotalTokens *int64
}

type RequestWriteState string

const (
	RequestWriteNotStarted RequestWriteState = "not_started"
	RequestWriteCompleted  RequestWriteState = "completed"
	RequestWriteUncertain  RequestWriteState = "uncertain"
)

func (s RequestWriteState) valid() bool {
	return s == RequestWriteNotStarted || s == RequestWriteCompleted || s == RequestWriteUncertain
}

// FinishResult 汇报两个作用域各自的 applied/stale disposition（写入 request_attempts）。
type FinishResult struct {
	ProviderDisposition Disposition
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
	LastTransitionAtMs       int64
	LastFailureCategory      string
	ControlPresent           bool   // Provider only
	EffectiveStatus          string // Provider only
	OriginRevision           int64  // Provider current / Channel bound Provider origin revision
	StatusRevision           int64  // Provider current / Channel bound Provider status revision
	PendingOriginRevision    int64  // Provider only: pending origin revision, 0 when absent
	PendingStatusRevision    int64  // Provider only: pending status revision, 0 when absent
	OriginRevisionState      string // Provider only: active|pending
	StatusRevisionState      string // Provider only: active|pending
	StateGeneration          int64
	OriginFenceGeneration    int64 // Provider only
	StatusFenceGeneration    int64 // Provider only
	ProviderID               int64 // Channel only
	ChannelConfigRevision    int64 // Channel only
	HalfOpenBusy             bool
	HalfOpenLeaseRemainingMs int64
}

// SnapshotCandidateInput 是批量路由快照所需的稳定候选身份。
// SnapshotMany 用它判断 Channel state 是否仍属于同一 Provider 与配置代际。
type SnapshotCandidateInput struct {
	ProviderID              int64
	ChannelID               int64
	OriginRevision          int64
	ProviderStatusRevision  int64
	ChannelConfigRevision   int64
	ChannelCapacityRevision int64
}

// SnapshotManyInput 固化一次客户请求在 PostgreSQL 强一致读取到的完整运行态版本。
// SnapshotMany 只接受这些 expected revisions，不接受调用方计算后的限额或评分参数。
type SnapshotManyInput struct {
	IntegrityEpoch            string
	IntegrityRevision         int64
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
	CandidateSnapshotProviderDisabled      CandidateSnapshotStatus = "provider_disabled"
)

// CapacityUsage 是 Redis stable resource 的同一时点只读事实。Limit=0 表示不限。
type CapacityUsage struct {
	Used  int64
	Limit int64
}

// RoutingBalanceSnapshot 是本次 SnapshotMany 的 active routing-balance 线性化点（objective_v1 五项评分，§7）。
type RoutingBalanceSnapshot struct {
	Revision             int64
	CostWeightPct        int
	ConcurrencyWeightPct int
	TTFTWeightPct        int
	ErrorRateWeightPct   int
	PriorityWeightPct    int
	// TTFT / 错误率各自的滚动窗口与线性惩罚参数（§7.4/§7.5）。
	TTFTWindowMs                 int64
	TTFTPenaltyUnitMs            int64
	TTFTPenaltyPointsPerUnit     float64
	ErrorWindowMs                int64
	ErrorPenaltyPointsPerPercent float64
}

// CandidateSnapshot 是同一 Redis Lua 时点读取的 Provider/Channel 运行态，保持输入顺序。
type CandidateSnapshot struct {
	Candidate                   SnapshotCandidateInput
	Status                      CandidateSnapshotStatus
	Provider                    ScopeSnapshot
	Channel                     ScopeSnapshot
	Concurrency                 CapacityUsage
	CooldownRemainingMs         int64
	ModelPermissionPaused       bool
	ModelPermissionRecheckState string
}

// SnapshotManyResult 保持候选输入顺序，并只返回一次共享 routing-balance payload。
type SnapshotManyResult struct {
	Candidates                []CandidateSnapshot
	IntegrityRevision         int64
	RouteRateRevision         int64
	GlobalConcurrencyRevision int64
	CircuitBreakerRevision    int64
	RoutingBalance            RoutingBalanceSnapshot
}
