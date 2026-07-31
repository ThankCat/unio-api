package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/core/requestlog"
	"github.com/ThankCat/unio-gateway/internal/core/routingdiagnostic"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/observability/metrics"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

const routingTraceAlgorithmVersion = "objective_v1"

// routingTraceSchemaVersion 是结构化 trace_payload 的稳定 schema 版本（§13.1）。
// 0 保留给改造前的 legacy_sampled 行——它们没有结构化 payload，不能伪装成完整 trace。
const routingTraceSchemaVersion = 1

// TraceStatus 是 trace 的生命周期状态（§13.1）。
type TraceStatus string

const (
	// TraceStatusPartial 请求已进入路由规划但尚未收口。进程异常时保留 partial 是有意义的：
	// 它区分「请求尚未收口」和「根本没有记录」。
	TraceStatusPartial TraceStatus = "partial"
	// TraceStatusComplete 请求生命周期已结束，trace 已幂等收口。
	TraceStatusComplete TraceStatus = "complete"
)

// RoutingTraceStore 是 trace recorder 唯一需要的持久化能力。
type RoutingTraceStore interface {
	UpsertRoutingDecisionTrace(context.Context, sqlc.UpsertRoutingDecisionTraceParams) error
}

type routingTraceDiagnosticStore interface {
	RouteRuntimePool(context.Context, sqlc.RouteRuntimePoolParams) ([]sqlc.RouteRuntimePoolRow, error)
}

// RoutingTraceRecorder 为每个进入路由规划的请求持久化一条不含敏感数据的完整路由过程。
//
// 不再对普通成功请求做采样（§13.1）：路由问题恰恰最需要在「看起来正常」的请求上被解释。
// trace 与请求记录同生命周期（ON DELETE CASCADE），不单独裁剪。
type RoutingTraceRecorder struct {
	store   RoutingTraceStore
	logger  *zap.Logger
	metrics routingTraceMetricsRecorder
}

func (r *RoutingTraceRecorder) SetMetrics(metrics routingTraceMetricsRecorder) {
	if r != nil {
		r.metrics = metrics
	}
}

func NewRoutingTraceRecorder(store RoutingTraceStore, logger *zap.Logger) *RoutingTraceRecorder {
	if store == nil {
		return nil
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &RoutingTraceRecorder{store: store, logger: logger}
}

// RoutingDecisionTraceInput 是一次计划或 fallback 后的 trace 输入。
type RoutingDecisionTraceInput struct {
	Request         requestlog.RequestRecord
	RouteID         int64
	Mode            string
	PoolSize        int
	Plan            CandidatePlan
	StickyChannelID int64
	// Sticky 是本请求对绑定采取的动作快照（§10.12）。零值表示未启用 sticky。
	Sticky           StickyAudit
	ForceReasons     []string
	FallbackChain    []TransportAttempt
	FallbackOccurred bool
	MarginGuard      bool

	// Status 缺省为 partial：只有请求生命周期结束时的收口写入才置为 complete（§13.1）。
	Status TraceStatus

	// 以下是收口阶段才有的执行事实（§13.2/§13.3）。partial 写入时为零值。
	ActualScanOrder     []int64
	AttemptedChannelIDs []int64
	AcquireResults      []AcquireOutcome
	SelectedChannelID   int64
	FallbackCount       int
	FinalResult         string
	CapacityWaitMs      *int64
	CapacityWaitResult  string
}

// tracePayload 是 §13.3 要求的完整结构化路由过程。
// 它只保存稳定枚举与数值事实：绝不写 API key、原始 session key、credential、
// 用户 prompt、响应正文或未脱敏的上游错误正文（§13.4）。
type tracePayload struct {
	SchemaVersion    int                   `json:"schema_version"`
	AlgorithmVersion string                `json:"algorithm_version"`
	Mode             string                `json:"mode"`
	Candidates       []traceCandidateScore `json:"candidates"`
	BaselineOrder    []int64               `json:"baseline_order"`
	ActualScanOrder  []int64               `json:"actual_scan_order"`
	AcquireResults   []AcquireOutcome      `json:"acquire_results"`
	Attempts         []TransportAttempt    `json:"attempts"`
	AttemptedChannel []int64               `json:"attempted_channel_ids"`
	Sticky           tracePayloadSticky    `json:"sticky"`
	CapacityWait     tracePayloadWait      `json:"capacity_wait"`
	ScoreConfig      tracePayloadScoreCfg  `json:"score_config"`
	AbnormalReasons  []string              `json:"abnormal_reasons"`
	FinalResult      string                `json:"final_result,omitempty"`
}

type tracePayloadSticky struct {
	KeyPresent         bool   `json:"key_present"`
	BeforeChannelID    int64  `json:"before_channel_id,omitempty"`
	BeforeVersion      int64  `json:"before_version,omitempty"`
	Action             string `json:"action,omitempty"`
	Reason             string `json:"reason,omitempty"`
	AfterChannelID     int64  `json:"after_channel_id,omitempty"`
	AfterVersion       int64  `json:"after_version,omitempty"`
	Pinned             bool   `json:"pinned"`
	PinnedNonPreferred bool   `json:"pinned_non_preferred"`
}

type tracePayloadWait struct {
	Result  string `json:"result,omitempty"`
	Waited  *int64 `json:"waited_ms,omitempty"`
	Entered bool   `json:"entered"`
}

// tracePayloadScoreCfg 冻结本次决策实际生效的评分权重，
// 这样即使之后改了系统设置，历史 trace 仍能被正确解释。
type tracePayloadScoreCfg struct {
	Revision             int64 `json:"routing_balance_revision"`
	CostWeightPct        int   `json:"cost_weight_pct"`
	ConcurrencyWeightPct int   `json:"concurrency_weight_pct"`
	TTFTWeightPct        int   `json:"ttft_weight_pct"`
	ErrorRateWeightPct   int   `json:"error_rate_weight_pct"`
	PriorityWeightPct    int   `json:"priority_weight_pct"`
}

type traceCandidateScore struct {
	ProviderID                       int64    `json:"provider_id"`
	ChannelID                        int64    `json:"channel_id"`
	RouteIndex                       int      `json:"route_index"`
	Eligible                         bool     `json:"eligible"`
	ExcludedReason                   string   `json:"excluded_reason,omitempty"`
	CandidateOriginRevision          int64    `json:"candidate_origin_revision"`
	RuntimeOriginRevision            int64    `json:"runtime_origin_revision"`
	OriginRevisionCurrent            bool     `json:"origin_revision_current"`
	CandidateProviderStatusRevision  int64    `json:"candidate_provider_status_revision"`
	RuntimeProviderStatusRevision    int64    `json:"runtime_provider_status_revision"`
	ProviderStatusRevisionCurrent    bool     `json:"provider_status_revision_current"`
	CandidateChannelConfigRevision   int64    `json:"candidate_channel_config_revision"`
	RuntimeChannelConfigRevision     *int64   `json:"runtime_channel_config_revision"`
	ChannelConfigRevisionCurrent     bool     `json:"channel_config_revision_current"`
	CandidateChannelCapacityRevision int64    `json:"candidate_channel_capacity_revision"`
	RuntimeChannelCapacityRevision   int64    `json:"runtime_channel_capacity_revision"`
	ChannelCapacityRevisionCurrent   bool     `json:"channel_capacity_revision_current"`
	RouteRateLimitsRevision          int64    `json:"route_rate_limits_revision"`
	GlobalConcurrencyRevision        int64    `json:"global_concurrency_revision"`
	CircuitBreakerRevision           int64    `json:"circuit_breaker_revision"`
	RuntimeControlState              string   `json:"runtime_control_state"`
	RuntimeRevisionCurrent           bool     `json:"runtime_revision_current"`
	ProviderBreakerState             string   `json:"provider_breaker_state,omitempty"`
	ChannelBreakerState              string   `json:"channel_breaker_state,omitempty"`
	BreakerStoreAdmission            string   `json:"breaker_store_admission"`
	AlgorithmVersion                 string   `json:"algorithm_version"`
	ConcurrencyRemaining             *float64 `json:"concurrency_remaining"`
	CostScore                        float64  `json:"cost_score"`
	ConcurrencyScore                 float64  `json:"concurrency_score"`
	TTFTScore                        float64  `json:"ttft_score"`
	ErrorScore                       float64  `json:"error_score"`
	PriorityScore                    float64  `json:"priority_score"`
	FinalScore                       float64  `json:"final_score"`
	CostWeightPct                    int      `json:"cost_weight_pct"`
	ConcurrencyWeightPct             int      `json:"concurrency_weight_pct"`
	TTFTWeightPct                    int      `json:"ttft_weight_pct"`
	ErrorRateWeightPct               int      `json:"error_rate_weight_pct"`
	PriorityWeightPct                int      `json:"priority_weight_pct"`
	CostRatio                        float64  `json:"cost_ratio"`
	Priority                         int32    `json:"priority"`
	AvgTTFTMs                        float64  `json:"avg_ttft_ms"`
	TTFTSampleCount                  int64    `json:"ttft_sample_count"`
	ErrorRatePct                     float64  `json:"error_rate_pct"`
	ErrorSampleCount                 int64    `json:"error_sample_count"`
	RoutingBalanceRevision           int64    `json:"routing_balance_revision"`
	CapacityUnknown                  bool     `json:"capacity_unknown"`
	CapacityReadFailed               bool     `json:"capacity_read_failed"`
	CooldownRemainingMs              int64    `json:"cooldown_remaining_ms"`
	ModelPermissionPaused            bool     `json:"model_permission_paused"`
	ModelPermissionRecheckState      string   `json:"model_permission_recheck_state"`
}

func (r *RoutingTraceRecorder) Record(ctx context.Context, in RoutingDecisionTraceInput) {
	if r == nil || r.store == nil || in.RouteID <= 0 || in.Request.ID <= 0 {
		return
	}
	// pgx encodes a nil []string as SQL NULL, while abnormal_reasons is NOT NULL.
	reasons := append(make([]string, 0, len(in.ForceReasons)+4), in.ForceReasons...)
	if in.FallbackOccurred {
		reasons = append(reasons, "fallback")
	}
	stickyTemporaryBypass := false
	if in.StickyChannelID != 0 && !in.Plan.StickyPinned {
		_, stickyTemporaryBypass = in.Plan.stickyTemporaryBypassReason(in.StickyChannelID)
	}
	stickyInvalid := in.StickyChannelID != 0 && !in.Plan.StickyPinned && !stickyTemporaryBypass
	if in.Plan.AllCapacityZero {
		reasons = append(reasons, "all_capacity_zero")
	}
	if stickyInvalid {
		reasons = append(reasons, "sticky_invalid")
	}
	if stickyTemporaryBypass {
		reasons = append(reasons, "sticky_cooldown_bypass")
	}
	if in.MarginGuard {
		reasons = append(reasons, "negative_margin")
	}
	decisionOrder := make([]int64, 0, len(in.Plan.Candidates))
	for _, candidate := range in.Plan.Candidates {
		decisionOrder = append(decisionOrder, candidate.Route.Channel.ID)
	}
	selectedChannelID := int64(0)
	if len(decisionOrder) > 0 {
		selectedChannelID = decisionOrder[0]
	}
	r.logger.Info("routing decision",
		zap.String("request_id", in.Request.RequestID),
		zap.Int64("route_id", in.RouteID),
		zap.String("mode", in.Mode),
		zap.Int64("channel_id", selectedChannelID),
		zap.Int("pool_size", in.PoolSize),
		zap.Int("candidate_count", len(in.Plan.Candidates)),
		zap.Int64s("selected_order", decisionOrder),
		zap.String("fallback_reason", strings.Join(reasons, ",")),
	)
	planCandidates := make(map[int64]Candidate, len(in.Plan.Candidates))
	planExcluded := make(map[int64]CandidateExclusion, len(in.Plan.Excluded))
	for _, candidate := range in.Plan.Candidates {
		planCandidates[candidate.Route.Channel.ID] = candidate
	}
	for _, excluded := range in.Plan.Excluded {
		planExcluded[excluded.ChannelID] = excluded
	}
	scores := make([]traceCandidateScore, 0, len(in.Plan.Candidates)+len(in.Plan.Excluded))
	poolSize := in.PoolSize
	if diagnosticStore, ok := r.store.(routingTraceDiagnosticStore); ok {
		poolRows, poolErr := diagnosticStore.RouteRuntimePool(ctx, sqlc.RouteRuntimePoolParams{
			RouteID: in.RouteID, ModelID: in.Request.RequestedModelID,
			AtTime: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
		})
		if poolErr != nil {
			r.logger.Warn("read routing trace pool diagnostics", zap.Int64("route_id", in.RouteID), zap.Error(poolErr))
		} else {
			poolSize = len(poolRows)
			if in.Mode == "" && len(poolRows) > 0 {
				in.Mode = poolRows[0].Mode
			}
			for index, row := range poolRows {
				reason := routingdiagnostic.ExcludedReason(poolFactsFromRow(row), routingdiagnostic.Filter{
					ModelID: in.Request.RequestedModelID, Protocol: string(in.Request.IngressProtocol),
				})
				candidate, selected := planCandidates[row.ChannelID]
				routeIndex := index
				if selected {
					routeIndex = candidate.RouteIndex
				} else if excluded, exists := planExcluded[row.ChannelID]; exists {
					routeIndex = excluded.RouteIndex
					candidate = Candidate{Route: excluded.Route, Balance: excluded.Balance}
					if reason == "" {
						reason = excluded.Reason
					}
				} else if reason == "" {
					reason = "not_in_candidate_plan"
				}
				if candidate.Balance.ProviderID == 0 {
					candidate.Balance.ProviderID = row.ProviderID
					candidate.Balance.CandidateOriginRevision = row.ProviderOriginRevision
					candidate.Balance.CandidateProviderStatusRevision = row.ProviderStatusRevision
					candidate.Balance.CandidateChannelConfigRevision = row.ChannelConfigRevision
					candidate.Balance.CandidateChannelCapacityRevision = row.ChannelCapacityRevision
				}
				scores = append(scores, traceScore(candidate, row.ChannelID, routeIndex, selected && reason == "", reason))
			}
		}
	}
	if len(scores) == 0 {
		for _, candidate := range in.Plan.Candidates {
			scores = append(scores, traceScore(candidate, candidate.Route.Channel.ID, candidate.RouteIndex, true, ""))
		}
		for _, excluded := range in.Plan.Excluded {
			scores = append(scores, traceScore(Candidate{Route: excluded.Route, Balance: excluded.Balance}, excluded.ChannelID, excluded.RouteIndex, false, excluded.Reason))
		}
	}
	audit := in.Sticky

	status := in.Status
	if status == "" {
		status = TraceStatusPartial
	}
	eligibleCount := 0
	for _, score := range scores {
		if score.Eligible {
			eligibleCount++
		}
	}
	payload := tracePayload{
		SchemaVersion:    routingTraceSchemaVersion,
		AlgorithmVersion: routingTraceAlgorithmVersion,
		Mode:             in.Mode,
		Candidates:       scores,
		BaselineOrder:    nonNilInt64s(in.Plan.BaselineOrder),
		ActualScanOrder:  nonNilInt64s(in.ActualScanOrder),
		AcquireResults:   nonNilAcquireOutcomes(in.AcquireResults),
		Attempts:         nonNilTransportAttempts(in.FallbackChain),
		AttemptedChannel: nonNilInt64s(in.AttemptedChannelIDs),
		Sticky: tracePayloadSticky{
			KeyPresent: audit.KeyPresent, BeforeChannelID: audit.BeforeChannelID,
			BeforeVersion: audit.BeforeVersion, Action: string(audit.Action), Reason: audit.Reason,
			AfterChannelID: audit.AfterChannelID, AfterVersion: audit.AfterVersion,
			Pinned: in.Plan.StickyPinned, PinnedNonPreferred: in.Plan.StickyPinnedNonPreferred,
		},
		CapacityWait: tracePayloadWait{
			Result: in.CapacityWaitResult, Waited: in.CapacityWaitMs,
			Entered: in.CapacityWaitMs != nil,
		},
		ScoreConfig:     traceScoreConfigOf(in.Plan),
		AbnormalReasons: reasons,
		FinalResult:     in.FinalResult,
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		r.logger.Error("marshal routing trace payload", zap.Error(err))
		r.recordWriteMetric("failed")
		return
	}

	if err := r.store.UpsertRoutingDecisionTrace(ctx, sqlc.UpsertRoutingDecisionTraceParams{
		RequestRecordID:       in.Request.ID,
		RouteID:               in.RouteID,
		Mode:                  in.Mode,
		RequestedModelID:      in.Request.RequestedModelID,
		Protocol:              string(in.Request.IngressProtocol),
		Endpoint:              string(in.Request.Endpoint),
		PoolSize:              int32(poolSize),
		StickyKeyPresent:      audit.KeyPresent,
		StickyBeforeChannelID: optionalTraceInt64(audit.BeforeChannelID),
		StickyBeforeVersion:   optionalTraceInt64(audit.BeforeVersion),
		StickyAction:          optionalTraceText(string(audit.Action)),
		StickyReason:          optionalTraceText(audit.Reason),
		StickyAfterChannelID:  optionalTraceInt64(audit.AfterChannelID),
		StickyAfterVersion:    optionalTraceInt64(audit.AfterVersion),
		AlgorithmVersion:      routingTraceAlgorithmVersion,
		TraceStatus:           string(status),
		SchemaVersion:         routingTraceSchemaVersion,
		EligibleCount:         int32(eligibleCount),
		BaselineOrder:         nonNilInt64s(in.Plan.BaselineOrder),
		ActualScanOrder:       nonNilInt64s(in.ActualScanOrder),
		AttemptedChannelIds:   nonNilInt64s(in.AttemptedChannelIDs),
		SelectedChannelID:     optionalTraceInt64(in.SelectedChannelID),
		FallbackCount:         int32(in.FallbackCount),
		FinalResult:           optionalTraceText(in.FinalResult),
		CapacityWaitMs:        optionalTraceInt32(in.CapacityWaitMs),
		CapacityWaitResult:    optionalTraceText(in.CapacityWaitResult),
		TracePayload:          payloadJSON,
	}); err != nil {
		r.recordWriteMetric("failed")
		r.logger.Error("write routing decision trace",
			zap.Int64("request_record_id", in.Request.ID),
			zap.String("trace_status", string(status)),
			zap.Error(err),
		)
		return
	}
	r.recordWriteMetric("success")
}

// nonNilInt64s 保证 NOT NULL 数组列拿到空数组而不是 SQL NULL（pgx 把 nil slice 编码成 NULL）。
func nonNilInt64s(in []int64) []int64 {
	if in == nil {
		return []int64{}
	}
	return in
}

func nonNilAcquireOutcomes(in []AcquireOutcome) []AcquireOutcome {
	if in == nil {
		return []AcquireOutcome{}
	}
	return in
}

func nonNilTransportAttempts(in []TransportAttempt) []TransportAttempt {
	if in == nil {
		return []TransportAttempt{}
	}
	return in
}

// traceScoreConfigOf 从任一候选身上取本次决策生效的评分配置（同一请求内所有候选共享同一配置）。
func traceScoreConfigOf(plan CandidatePlan) tracePayloadScoreCfg {
	for _, candidate := range plan.Candidates {
		return tracePayloadScoreCfg{
			Revision:             candidate.Balance.RoutingBalanceRevision,
			CostWeightPct:        candidate.Balance.CostWeightPct,
			ConcurrencyWeightPct: candidate.Balance.ConcurrencyWeightPct,
			TTFTWeightPct:        candidate.Balance.TTFTWeightPct,
			ErrorRateWeightPct:   candidate.Balance.ErrorRateWeightPct,
			PriorityWeightPct:    candidate.Balance.PriorityWeightPct,
		}
	}
	return tracePayloadScoreCfg{}
}

// optionalTraceInt32 把 capacity_wait_ms 归一为 SQL NULL：没等过与等了 0ms 是不同事实。
func optionalTraceInt32(value *int64) pgtype.Int4 {
	if value == nil || *value < 0 {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: int32(*value), Valid: true}
}

func (r *RoutingTraceRecorder) recordWriteMetric(result string) {
	if r != nil && r.metrics != nil {
		r.metrics.IncRoutingTraceWrite(result)
	}
}

func traceScore(candidate Candidate, channelID int64, routeIndex int, eligible bool, excludedReason string) traceCandidateScore {
	return traceCandidateScore{
		ProviderID: candidate.Balance.ProviderID,
		ChannelID:  channelID, RouteIndex: routeIndex, Eligible: eligible, ExcludedReason: excludedReason,
		CandidateOriginRevision:          candidate.Balance.CandidateOriginRevision,
		RuntimeOriginRevision:            candidate.Balance.RuntimeOriginRevision,
		OriginRevisionCurrent:            candidate.Balance.OriginRevisionCurrent,
		CandidateProviderStatusRevision:  candidate.Balance.CandidateProviderStatusRevision,
		RuntimeProviderStatusRevision:    candidate.Balance.RuntimeProviderStatusRevision,
		ProviderStatusRevisionCurrent:    candidate.Balance.ProviderStatusRevisionCurrent,
		CandidateChannelConfigRevision:   candidate.Balance.CandidateChannelConfigRevision,
		RuntimeChannelConfigRevision:     candidate.Balance.RuntimeChannelConfigRevision,
		ChannelConfigRevisionCurrent:     candidate.Balance.ChannelConfigRevisionCurrent,
		CandidateChannelCapacityRevision: candidate.Balance.CandidateChannelCapacityRevision,
		RuntimeChannelCapacityRevision:   candidate.Balance.RuntimeChannelCapacityRevision,
		ChannelCapacityRevisionCurrent:   candidate.Balance.ChannelCapacityRevisionCurrent,
		RouteRateLimitsRevision:          candidate.Balance.RouteRateLimitsRevision,
		GlobalConcurrencyRevision:        candidate.Balance.GlobalConcurrencyRevision,
		CircuitBreakerRevision:           candidate.Balance.CircuitBreakerRevision,
		RuntimeControlState:              candidate.Balance.RuntimeControlState,
		RuntimeRevisionCurrent:           candidate.Balance.RuntimeRevisionCurrent,
		ProviderBreakerState:             candidate.Balance.ProviderBreakerState,
		ChannelBreakerState:              candidate.Balance.ChannelBreakerState,
		BreakerStoreAdmission:            candidate.Balance.BreakerStoreAdmission,
		AlgorithmVersion:                 candidate.Balance.AlgorithmVersion,
		ConcurrencyRemaining:             candidate.Balance.ConcurrencyRemaining,
		CostScore:                        candidate.Balance.CostScore,
		ConcurrencyScore:                 candidate.Balance.ConcurrencyScore,
		TTFTScore:                        candidate.Balance.TTFTScore,
		ErrorScore:                       candidate.Balance.ErrorScore,
		PriorityScore:                    candidate.Balance.PriorityScore,
		FinalScore:                       candidate.Balance.FinalScore,
		CostWeightPct:                    candidate.Balance.CostWeightPct,
		ConcurrencyWeightPct:             candidate.Balance.ConcurrencyWeightPct,
		TTFTWeightPct:                    candidate.Balance.TTFTWeightPct,
		ErrorRateWeightPct:               candidate.Balance.ErrorRateWeightPct,
		PriorityWeightPct:                candidate.Balance.PriorityWeightPct,
		CostRatio:                        candidate.Balance.CostRatio,
		Priority:                         candidate.Balance.Priority,
		AvgTTFTMs:                        candidate.Balance.AvgTTFTMs,
		TTFTSampleCount:                  candidate.Balance.TTFTSampleCount,
		ErrorRatePct:                     candidate.Balance.ErrorRatePct,
		ErrorSampleCount:                 candidate.Balance.ErrorSampleCount,
		RoutingBalanceRevision:           candidate.Balance.RoutingBalanceRevision,
		CapacityUnknown:                  candidate.Balance.CapacityUnknown,
		CapacityReadFailed:               candidate.Balance.CapacityReadFailed,
		CooldownRemainingMs:              candidate.Balance.CooldownRemainingMs,
		ModelPermissionPaused:            candidate.Balance.ModelPermissionPaused,
		ModelPermissionRecheckState:      candidate.Balance.ModelPermissionRecheckState,
	}
}

func poolFactsFromRow(row sqlc.RouteRuntimePoolRow) routingdiagnostic.PoolFacts {
	return routingdiagnostic.PoolFacts{
		RouteStatus: row.RouteStatus, ChannelStatus: row.ChannelStatus, ProviderStatus: row.ProviderStatus,
		CredentialValid: row.CredentialValid, HasCredential: row.HasCredential, HasBaseURL: row.HasOrigin,
		Protocol: row.Protocol, ModelExists: row.ModelExists, ModelStatus: row.ModelStatus,
		BindingStatus: row.BindingStatus, HasModelPrice: row.HasModelPrice, HasChannelCost: row.HasChannelCost,
	}
}

// optionalTraceInt64 把 0 归一为 SQL NULL：trace 里的 0 会被误读成「绑定到 channel 0」。
func optionalTraceInt64(value int64) pgtype.Int8 {
	if value <= 0 {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: value, Valid: true}
}

func optionalTraceText(value string) pgtype.Text {
	if value == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: value, Valid: true}
}

// FinalResult 是 trace 的稳定终态枚举（§13.2）。它必须能一眼分清「客户放弃」、
// 「我们没容量」、「上游限流」和「上游真失败」——这四种在运维上的处置完全不同。
const (
	FinalResultSuccess            = "success"
	FinalResultClientCanceled     = "client_canceled"
	FinalResultCapacityExhausted  = "capacity_exhausted"
	FinalResultRateLimited        = "rate_limited"
	FinalResultNoAvailableChannel = "no_available_channel"
	FinalResultUpstreamFailed     = "upstream_failed"
	FinalResultGatewayError       = "gateway_error"
)

// finalResultOf 从终态与错误推导稳定终态码，只消费稳定失败码与 adapter 分类，不解析错误文本。
func finalResultOf(succeeded bool, err error) string {
	if succeeded && err == nil {
		return FinalResultSuccess
	}
	switch failure.CodeOf(err) {
	case failure.CodeRoutingChannelCapacityExhausted:
		return FinalResultCapacityExhausted
	case failure.CodeGatewayChannelRateLimited, failure.CodeRateLimitExceeded:
		return FinalResultRateLimited
	case failure.CodeRoutingNoAvailableChannel:
		return FinalResultNoAvailableChannel
	case failure.CodeGatewayBreakerStoreUnavailable,
		failure.CodeGatewayRuntimeSyncRequired,
		failure.CodeGatewayRuntimeStateLost:
		return FinalResultGatewayError
	}
	if errors.Is(err, context.Canceled) {
		return FinalResultClientCanceled
	}
	if category, ok := adapter.UpstreamCategoryOf(err); ok {
		if category == adapter.UpstreamErrorCanceled {
			return FinalResultClientCanceled
		}
		if category == adapter.UpstreamErrorRateLimit {
			return FinalResultRateLimited
		}
		return FinalResultUpstreamFailed
	}
	if err == nil {
		// 没有错误却也没成功：只可能是尚未收口的调用方 bug，明确标成 gateway 侧问题而不是猜。
		return FinalResultGatewayError
	}
	return FinalResultUpstreamFailed
}

// CompleteRoutingTrace 在请求生命周期结束时把 partial trace 幂等升级为 complete（§13.1）。
//
// 它必须无条件调用（不只在 fallback 时）：否则「一次成功、零 fallback」的普通请求永远停在 partial，
// 而这类请求恰恰是排查「为什么选了这条渠道」最常打开的。
func (l *RequestLifecycle) CompleteRoutingTrace(
	ctx context.Context,
	in RoutingDecisionTraceInput,
	result RunResult,
	err error,
) {
	if l == nil || l.routingTraces == nil {
		return
	}
	in.Status = TraceStatusComplete
	in.ActualScanOrder = result.ActualScanOrder
	in.AttemptedChannelIDs = result.AttemptedChannelIDs
	in.AcquireResults = result.AcquireResults
	in.FallbackChain = result.TransportChain
	in.FallbackOccurred = result.RoutingFallback
	in.CapacityWaitMs = result.CapacityWaitMs
	in.CapacityWaitResult = result.CapacityWaitResult
	if count := len(result.TransportChain); count > 1 {
		in.FallbackCount = count - 1
	}
	succeeded := result.Outcome == metrics.ChatOutcomeSuccess
	in.FinalResult = finalResultOf(succeeded, err)
	if succeeded && len(result.TransportChain) > 0 {
		in.SelectedChannelID = result.TransportChain[len(result.TransportChain)-1].ChannelID
	}
	l.routingTraces.Record(context.WithoutCancel(ctx), in)
}
