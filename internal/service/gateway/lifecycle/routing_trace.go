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

const routingTraceAlgorithmVersion = "balanced_v1"

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
	Request         requestlog.RequestRecord
	RouteID         int64
	Mode            string
	PoolSize        int
	Plan            CandidatePlan
	StickyChannelID int64
	ForceReasons    []string
	FallbackChain   any
	Attempts        int
	MarginGuard     bool
}

type traceCandidateScore struct {
	ChannelID            int64    `json:"channel_id"`
	RouteIndex           int      `json:"route_index"`
	Eligible             bool     `json:"eligible"`
	ExcludedReason       string   `json:"excluded_reason,omitempty"`
	ConcurrencyRemaining *float64 `json:"concurrency_remaining"`
	TPMRemaining         *float64 `json:"tpm_remaining"`
	CapacityScore        float64  `json:"capacity_score"`
	HealthFactor         float64  `json:"health_factor"`
	Weight               float64  `json:"weight"`
	Pressure             float64  `json:"pressure"`
	CapacityUnknown      bool     `json:"capacity_unknown"`
	CapacityReadFailed   bool     `json:"capacity_read_failed"`
}

func (r *RoutingTraceRecorder) Record(ctx context.Context, in RoutingDecisionTraceInput) {
	if r == nil || r.store == nil || in.RouteID <= 0 || in.Request.ID <= 0 {
		return
	}
	// pgx encodes a nil []string as SQL NULL, while abnormal_reasons is NOT NULL.
	reasons := append(make([]string, 0, len(in.ForceReasons)+4), in.ForceReasons...)
	if in.Attempts > 1 {
		reasons = append(reasons, "fallback")
	}
	stickyInvalid := in.StickyChannelID != 0 && !in.Plan.StickyPinned
	if in.Plan.CapacityDegraded {
		reasons = append(reasons, "capacity_degraded")
	}
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
					if reason == "" {
						reason = excluded.Reason
					}
				} else if reason == "" {
					reason = "not_in_candidate_plan"
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
			scores = append(scores, traceScore(Candidate{}, excluded.ChannelID, excluded.RouteIndex, false, excluded.Reason))
		}
	}
	scoreJSON, err := json.Marshal(scores)
	if err != nil {
		r.logger.Error("marshal routing trace scores", zap.Error(err))
		r.recordWriteMetric("failed")
		return
	}
	fallbackJSON := []byte("[]")
	if in.FallbackChain == nil && in.Attempts > 0 {
		attempted := in.Attempts
		if attempted > len(order) {
			attempted = len(order)
		}
		in.FallbackChain = order[:attempted]
	}
	if in.FallbackChain != nil {
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
		CapacityDegraded:     in.Plan.CapacityDegraded,
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
		ChannelID: channelID, RouteIndex: routeIndex, Eligible: eligible, ExcludedReason: excludedReason,
		ConcurrencyRemaining: candidate.Balance.ConcurrencyRemaining,
		TPMRemaining:         candidate.Balance.TPMRemaining,
		CapacityScore:        candidate.Balance.CapacityScore, HealthFactor: candidate.Balance.HealthFactor,
		Weight: candidate.Balance.Weight, Pressure: candidate.Balance.Pressure,
		CapacityUnknown: candidate.Balance.CapacityUnknown, CapacityReadFailed: candidate.Balance.CapacityReadFailed,
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
