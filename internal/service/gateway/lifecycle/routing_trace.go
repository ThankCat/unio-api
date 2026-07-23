package lifecycle

import (
	"context"
	"encoding/json"
	"hash/fnv"
	"math"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/core/requestlog"
	"github.com/ThankCat/unio-gateway/internal/core/routingdiagnostic"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

const routingTraceAlgorithmVersion = "balanced_v3_cost"

// RoutingTraceStore 是 trace recorder 唯一需要的持久化能力。
type RoutingTraceStore interface {
	UpsertRoutingDecisionTrace(context.Context, sqlc.UpsertRoutingDecisionTraceParams) error
}

type routingTraceDiagnosticStore interface {
	RouteRuntimePool(context.Context, sqlc.RouteRuntimePoolParams) ([]sqlc.RouteRuntimePoolRow, error)
}

// RoutingTraceRecorder 按稳定采样策略持久化不含敏感数据的路由决策。
type RoutingTraceRecorder struct {
	store      RoutingTraceStore
	logger     *zap.Logger
	metrics    routingTraceMetricsRecorder
	sampleRate atomic.Uint32
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
	recorder := &RoutingTraceRecorder{store: store, logger: logger}
	recorder.sampleRate.Store(500) // 万分之 500 = 5%。
	return recorder
}

// SetSampleRate hot-updates normal-decision sampling. Abnormal decisions remain fully persisted.
func (r *RoutingTraceRecorder) SetSampleRate(rate float64) {
	if r == nil {
		return
	}
	if math.IsNaN(rate) || rate < 0 {
		rate = 0
	}
	if rate > 1 {
		rate = 1
	}
	r.sampleRate.Store(uint32(math.Round(rate * 10000)))
}

// RoutingDecisionTraceInput 是一次计划或 fallback 后的 trace 输入。
type RoutingDecisionTraceInput struct {
	Request          requestlog.RequestRecord
	RouteID          int64
	Mode             string
	PoolSize         int
	Plan             CandidatePlan
	StickyChannelID  int64
	ForceReasons     []string
	FallbackChain    []TransportAttempt
	FallbackOccurred bool
	MarginGuard      bool
}

type traceCandidateScore struct {
	EndpointID                              int64    `json:"endpoint_id"`
	ChannelID                               int64    `json:"channel_id"`
	RouteIndex                              int      `json:"route_index"`
	Eligible                                bool     `json:"eligible"`
	ExcludedReason                          string   `json:"excluded_reason,omitempty"`
	CandidateEndpointBaseURLRevision        int64    `json:"candidate_endpoint_base_url_revision"`
	RuntimeEndpointBaseURLRevision          int64    `json:"runtime_endpoint_base_url_revision"`
	EndpointBaseURLRevisionCurrent          bool     `json:"endpoint_base_url_revision_current"`
	CandidateEndpointStatusRevision         int64    `json:"candidate_endpoint_status_revision"`
	RuntimeEndpointStatusRevision           int64    `json:"runtime_endpoint_status_revision"`
	EndpointStatusRevisionCurrent           bool     `json:"endpoint_status_revision_current"`
	CandidateChannelConfigRevision          int64    `json:"candidate_channel_config_revision"`
	RuntimeChannelConfigRevision            *int64   `json:"runtime_channel_config_revision"`
	ChannelConfigRevisionCurrent            bool     `json:"channel_config_revision_current"`
	CandidateChannelAdmissionLimitsRevision int64    `json:"candidate_channel_admission_limits_revision"`
	RuntimeChannelAdmissionLimitsRevision   int64    `json:"runtime_channel_admission_limits_revision"`
	ChannelAdmissionLimitsRevisionCurrent   bool     `json:"channel_admission_limits_revision_current"`
	RouteRateLimitsRevision                 int64    `json:"route_rate_limits_revision"`
	ChannelRateLimitsRevision               int64    `json:"channel_rate_limits_revision"`
	GlobalConcurrencyRevision               int64    `json:"global_concurrency_revision"`
	CircuitBreakerRevision                  int64    `json:"circuit_breaker_revision"`
	RuntimeControlState                     string   `json:"runtime_control_state"`
	RuntimeRevisionCurrent                  bool     `json:"runtime_revision_current"`
	EndpointBreakerState                    string   `json:"endpoint_breaker_state,omitempty"`
	ChannelBreakerState                     string   `json:"channel_breaker_state,omitempty"`
	BreakerStoreAdmission                   string   `json:"breaker_store_admission"`
	ConcurrencyRemaining                    *float64 `json:"concurrency_remaining"`
	TPMRemaining                            *float64 `json:"tpm_remaining"`
	CapacityScore                           float64  `json:"capacity_score"`
	ErrorRate                               float64  `json:"error_rate"`
	ErrorSamples                            int64    `json:"error_samples"`
	TTFTEWMAMs                              float64  `json:"ttft_ewma_ms"`
	TTFTSamples                             int64    `json:"ttft_samples"`
	TTFTSampleSource                        string   `json:"ttft_sample_source"`
	LatencyPenalty                          float64  `json:"latency_penalty"`
	RoutingFactor                           float64  `json:"routing_factor"`
	CostRatio                               float64  `json:"cost_ratio"`
	CostWeight                              float64  `json:"cost_weight"`
	CostFactor                              float64  `json:"cost_factor"`
	RoutingBalanceRevision                  int64    `json:"routing_balance_revision"`
	Weight                                  float64  `json:"final_weight"`
	Pressure                                float64  `json:"pressure"`
	CapacityUnknown                         bool     `json:"capacity_unknown"`
	CapacityReadFailed                      bool     `json:"capacity_read_failed"`
	CooldownRemainingMs                     int64    `json:"cooldown_remaining_ms"`
	ModelPermissionPaused                   bool     `json:"model_permission_paused"`
	ModelPermissionRecheckState             string   `json:"model_permission_recheck_state"`
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
	stickyInvalid := in.StickyChannelID != 0 && !in.Plan.StickyPinned
	if in.Plan.AllCapacityZero {
		reasons = append(reasons, "all_capacity_zero")
	}
	if stickyInvalid {
		reasons = append(reasons, "sticky_invalid")
	}
	if in.MarginGuard {
		reasons = append(reasons, "negative_margin")
	}
	abnormal := len(reasons) > 0
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
	sampled := routingTraceSampled(in.Request.RequestID, r.sampleRate.Load())
	if !abnormal && !sampled {
		r.recordWriteMetric("sampled_out")
		return
	}

	planCandidates := make(map[int64]Candidate, len(in.Plan.Candidates))
	planExcluded := make(map[int64]CandidateExclusion, len(in.Plan.Excluded))
	for _, candidate := range in.Plan.Candidates {
		planCandidates[candidate.Route.Channel.ID] = candidate
	}
	for _, excluded := range in.Plan.Excluded {
		planExcluded[excluded.ChannelID] = excluded
	}
	scores := make([]traceCandidateScore, 0, len(in.Plan.Candidates)+len(in.Plan.Excluded))
	order := decisionOrder
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
				if candidate.Balance.EndpointID == 0 {
					candidate.Balance.EndpointID = row.ProviderEndpointID
					candidate.Balance.CandidateEndpointBaseURLRevision = row.ProviderEndpointBaseUrlRevision
					candidate.Balance.CandidateEndpointStatusRevision = row.ProviderEndpointStatusRevision
					candidate.Balance.CandidateChannelConfigRevision = row.ChannelConfigRevision
					candidate.Balance.CandidateChannelAdmissionLimitsRevision = row.ChannelAdmissionLimitsRevision
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
	scoreJSON, err := json.Marshal(scores)
	if err != nil {
		r.logger.Error("marshal routing trace scores", zap.Error(err))
		r.recordWriteMetric("failed")
		return
	}
	fallbackJSON := []byte("[]")
	if len(in.FallbackChain) > 0 {
		fallbackJSON, err = json.Marshal(in.FallbackChain)
		if err != nil {
			r.logger.Error("marshal routing trace fallback", zap.Error(err))
			r.recordWriteMetric("failed")
			return
		}
	}
	stickyID := pgtype.Int8{}
	if in.StickyChannelID > 0 {
		stickyID = pgtype.Int8{Int64: in.StickyChannelID, Valid: true}
	}
	if err := r.store.UpsertRoutingDecisionTrace(ctx, sqlc.UpsertRoutingDecisionTraceParams{
		RequestRecordID:      in.Request.ID,
		RouteID:              in.RouteID,
		Mode:                 in.Mode,
		RequestedModelID:     in.Request.RequestedModelID,
		Protocol:             string(in.Request.IngressProtocol),
		Operation:            string(in.Request.Operation),
		PoolSize:             int32(poolSize),
		CandidateCount:       int32(len(in.Plan.Candidates)),
		StickyChannelID:      stickyID,
		StickyPinned:         in.Plan.StickyPinned,
		StickyInvalid:        stickyInvalid,
		AllCapacityZero:      in.Plan.AllCapacityZero,
		MarginGuardTriggered: in.MarginGuard,
		Abnormal:             abnormal,
		AbnormalReasons:      reasons,
		CandidateScores:      scoreJSON,
		SelectedOrder:        order,
		FallbackChain:        fallbackJSON,
		AlgorithmVersion:     routingTraceAlgorithmVersion,
		Sampled:              sampled,
	}); err != nil {
		r.recordWriteMetric("failed")
		r.logger.Error("write routing decision trace", zap.Int64("request_record_id", in.Request.ID), zap.Error(err))
		return
	}
	r.recordWriteMetric("success")
}

func (r *RoutingTraceRecorder) recordWriteMetric(result string) {
	if r != nil && r.metrics != nil {
		r.metrics.IncRoutingTraceWrite(result)
	}
}

func traceScore(candidate Candidate, channelID int64, routeIndex int, eligible bool, excludedReason string) traceCandidateScore {
	return traceCandidateScore{
		EndpointID: candidate.Balance.EndpointID,
		ChannelID:  channelID, RouteIndex: routeIndex, Eligible: eligible, ExcludedReason: excludedReason,
		CandidateEndpointBaseURLRevision:        candidate.Balance.CandidateEndpointBaseURLRevision,
		RuntimeEndpointBaseURLRevision:          candidate.Balance.RuntimeEndpointBaseURLRevision,
		EndpointBaseURLRevisionCurrent:          candidate.Balance.EndpointBaseURLRevisionCurrent,
		CandidateEndpointStatusRevision:         candidate.Balance.CandidateEndpointStatusRevision,
		RuntimeEndpointStatusRevision:           candidate.Balance.RuntimeEndpointStatusRevision,
		EndpointStatusRevisionCurrent:           candidate.Balance.EndpointStatusRevisionCurrent,
		CandidateChannelConfigRevision:          candidate.Balance.CandidateChannelConfigRevision,
		RuntimeChannelConfigRevision:            candidate.Balance.RuntimeChannelConfigRevision,
		ChannelConfigRevisionCurrent:            candidate.Balance.ChannelConfigRevisionCurrent,
		CandidateChannelAdmissionLimitsRevision: candidate.Balance.CandidateChannelAdmissionLimitsRevision,
		RuntimeChannelAdmissionLimitsRevision:   candidate.Balance.RuntimeChannelAdmissionLimitsRevision,
		ChannelAdmissionLimitsRevisionCurrent:   candidate.Balance.ChannelAdmissionLimitsRevisionCurrent,
		RouteRateLimitsRevision:                 candidate.Balance.RouteRateLimitsRevision,
		ChannelRateLimitsRevision:               candidate.Balance.ChannelRateLimitsRevision,
		GlobalConcurrencyRevision:               candidate.Balance.GlobalConcurrencyRevision,
		CircuitBreakerRevision:                  candidate.Balance.CircuitBreakerRevision,
		RuntimeControlState:                     candidate.Balance.RuntimeControlState,
		RuntimeRevisionCurrent:                  candidate.Balance.RuntimeRevisionCurrent,
		EndpointBreakerState:                    candidate.Balance.EndpointBreakerState,
		ChannelBreakerState:                     candidate.Balance.ChannelBreakerState,
		BreakerStoreAdmission:                   candidate.Balance.BreakerStoreAdmission,
		ConcurrencyRemaining:                    candidate.Balance.ConcurrencyRemaining,
		TPMRemaining:                            candidate.Balance.TPMRemaining,
		CapacityScore:                           candidate.Balance.CapacityScore, ErrorRate: candidate.Balance.ErrorRate,
		ErrorSamples: candidate.Balance.ErrorSamples,
		TTFTEWMAMs:   candidate.Balance.TTFTEWMAMs, TTFTSamples: candidate.Balance.TTFTSamples,
		TTFTSampleSource: candidate.Balance.TTFTSampleSource, LatencyPenalty: candidate.Balance.LatencyPenalty,
		RoutingFactor: candidate.Balance.RoutingFactor,
		CostRatio:     candidate.Balance.CostRatio, CostWeight: candidate.Balance.CostWeight,
		CostFactor: candidate.Balance.CostFactor, RoutingBalanceRevision: candidate.Balance.RoutingBalanceRevision,
		Weight: candidate.Balance.Weight, Pressure: candidate.Balance.Pressure,
		CapacityUnknown: candidate.Balance.CapacityUnknown, CapacityReadFailed: candidate.Balance.CapacityReadFailed,
		CooldownRemainingMs:         candidate.Balance.CooldownRemainingMs,
		ModelPermissionPaused:       candidate.Balance.ModelPermissionPaused,
		ModelPermissionRecheckState: candidate.Balance.ModelPermissionRecheckState,
	}
}

func poolFactsFromRow(row sqlc.RouteRuntimePoolRow) routingdiagnostic.PoolFacts {
	return routingdiagnostic.PoolFacts{
		RouteStatus: row.RouteStatus, ChannelStatus: row.ChannelStatus, ProviderStatus: row.ProviderStatus,
		CredentialValid: row.CredentialValid, HasCredential: row.HasCredential, HasBaseURL: row.HasBaseUrl,
		Protocol: row.Protocol, ModelExists: row.ModelExists, ModelStatus: row.ModelStatus,
		BindingStatus: row.BindingStatus, HasModelPrice: row.HasModelPrice, HasChannelCost: row.HasChannelCost,
	}
}

func routingTraceSampled(requestID string, rate uint32) bool {
	if rate == 0 || requestID == "" {
		return false
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(requestID))
	return h.Sum32()%10000 < rate
}
