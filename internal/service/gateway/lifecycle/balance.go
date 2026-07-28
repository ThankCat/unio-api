package lifecycle

import (
	"context"
	"math"
	"sort"

	"github.com/ThankCat/unio-gateway/internal/core/routing"
)

const (
	defaultTTFTTargetMs = int64(2000)
	defaultTTFTWeight   = 0.35
)

// CapacitySignal 是一个 channel-global 容量维度的只读事实。Limit<=0 表示显式不限。
type CapacitySignal struct {
	Used  int64
	Limit int64
	Known bool
}

// ChannelCapacity 是 balanced scorer 使用的并发和 TPM 快照。
type ChannelCapacity struct {
	Concurrency  CapacitySignal
	TPM          CapacitySignal
	ErrorRate    float64
	ErrorSamples int64
	TTFTEWMAMs   float64
	TTFTSamples  int64
	HalfOpen     bool
	RuntimeKnown bool
}

// ChannelCapacitySnapshotReader 读取候选渠道的全局容量；读取不能产生预占或推进状态机。
type ChannelCapacitySnapshotReader func(context.Context, routing.ChatRouteCandidate) (ChannelCapacity, error)

// BalanceScore 保存一次候选评分的完整组成，供调度、trace 和运行时后台共用。
type BalanceScore struct {
	AlgorithmVersion                        string
	ProviderID                              int64
	CandidateOriginRevision                 int64
	RuntimeOriginRevision                   int64
	OriginRevisionCurrent                   bool
	CandidateProviderStatusRevision         int64
	RuntimeProviderStatusRevision           int64
	ProviderStatusRevisionCurrent           bool
	CandidateChannelConfigRevision          int64
	RuntimeChannelConfigRevision            *int64
	ChannelConfigRevisionCurrent            bool
	CandidateChannelAdmissionLimitsRevision int64
	RuntimeChannelAdmissionLimitsRevision   int64
	ChannelAdmissionLimitsRevisionCurrent   bool
	RouteRateLimitsRevision                 int64
	ChannelRateLimitsRevision               int64
	GlobalConcurrencyRevision               int64
	CircuitBreakerRevision                  int64
	ConcurrencyRemaining                    *float64
	TPMRemaining                            *float64
	CapacityScore                           float64
	HealthScore                             float64
	EconomicScore                           float64
	PriorityScore                           float64
	FinalScore                              float64
	EconomicWeightPct                       int
	HealthWeightPct                         int
	CapacityWeightPct                       int
	PriorityWeightPct                       int
	Priority                                int32
	ErrorRate                               float64
	ErrorSamples                            int64
	TTFTEWMAMs                              float64
	TTFTSamples                             int64
	TTFTSampleSource                        string
	LatencyPenalty                          float64
	RoutingFactor                           float64
	CostRatio                               float64
	CostWeight                              float64
	CostFactor                              float64
	RoutingBalanceRevision                  int64
	Weight                                  float64
	Pressure                                float64
	CapacityUnknown                         bool
	CapacityReadFailed                      bool
	RuntimeControlState                     string
	RuntimeRevisionCurrent                  bool
	ProviderBreakerState                    string
	ChannelBreakerState                     string
	CooldownRemainingMs                     int64
	ModelPermissionPaused                   bool
	ModelPermissionRecheckState             string
	BreakerStoreAdmission                   string
	HalfOpen                                bool
}

type scoredCandidate struct {
	route routing.ChatRouteCandidate
	score BalanceScore
}

func orderBalancedCandidates(
	ctx context.Context,
	in []routing.ChatRouteCandidate,
	mode string,
	capacity ChannelCapacitySnapshotReader,
	config BalanceConfig,
) ([]routing.ChatRouteCandidate, map[int64]BalanceScore, bool) {
	entries := make([]scoredCandidate, 0, len(in))
	scores := make(map[int64]BalanceScore, len(in))
	if mode != "balanced" {
		out := append([]routing.ChatRouteCandidate(nil), in...)
		for _, candidate := range out {
			snapshot := ChannelCapacity{}
			if capacity != nil {
				if value, err := capacity(ctx, candidate); err == nil {
					snapshot = value
				}
			}
			score := scoreCapacity(snapshot, config)
			score = ApplyObjectiveFactors(score, candidate.CostRatio, candidate.Priority, config)
			scores[candidate.Channel.ID] = score
		}
		return out, scores, false
	}

	allZero := len(in) > 0
	for _, candidate := range in {
		snapshot := ChannelCapacity{}
		readFailed := false
		if capacity != nil {
			var err error
			snapshot, err = capacity(ctx, candidate)
			if err != nil {
				readFailed = true
				snapshot = ChannelCapacity{}
			}
		}
		score := scoreCapacity(snapshot, config)
		score = ApplyObjectiveFactors(score, candidate.CostRatio, candidate.Priority, config)
		score.CapacityReadFailed = readFailed
		if score.CapacityScore > 0 {
			allZero = false
		}
		entries = append(entries, scoredCandidate{route: candidate, score: score})
	}

	closed := make([]scoredCandidate, 0, len(entries))
	halfOpen := make([]scoredCandidate, 0)
	for _, entry := range entries {
		if entry.score.HalfOpen {
			halfOpen = append(halfOpen, entry)
			continue
		}
		closed = append(closed, entry)
	}

	sort.SliceStable(closed, func(i, j int) bool {
		if closed[i].score.FinalScore != closed[j].score.FinalScore {
			return closed[i].score.FinalScore > closed[j].score.FinalScore
		}
		if closed[i].route.Priority != closed[j].route.Priority {
			return closed[i].route.Priority < closed[j].route.Priority
		}
		return closed[i].route.Channel.ID < closed[j].route.Channel.ID
	})
	// half-open stays behind ordinary candidates and preserves the SQL Priority/ID order for probes.
	entries = append(closed, halfOpen...)

	out := make([]routing.ChatRouteCandidate, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.route)
		scores[entry.route.Channel.ID] = entry.score
	}
	return out, scores, allZero
}

func scoreCapacity(snapshot ChannelCapacity, config BalanceConfig) BalanceScore {
	config = normalizedBalanceConfig(config)
	concurrencyRemaining, concurrencyPressure := remainingRatio(snapshot.Concurrency)
	tpmRemaining, tpmPressure := remainingRatio(snapshot.TPM)

	capacity := 1.0
	switch {
	case concurrencyRemaining != nil && tpmRemaining != nil:
		capacity = math.Min(*concurrencyRemaining, *tpmRemaining)
	case concurrencyRemaining != nil:
		capacity = *concurrencyRemaining
	case tpmRemaining != nil:
		capacity = *tpmRemaining
	}
	errorRate := snapshot.ErrorRate
	errorRate = clamp01(errorRate)
	latencyPenalty := 0.0
	if snapshot.TTFTSamples > 0 {
		latencyPenalty = snapshot.TTFTEWMAMs / (snapshot.TTFTEWMAMs + float64(config.TTFTTargetMs))
		latencyPenalty = clamp01(latencyPenalty)
	}
	health := (1 - errorRate) * (1 - config.TTFTWeight*latencyPenalty)
	if snapshot.ErrorSamples == 0 && snapshot.TTFTSamples == 0 {
		health = 1
	}
	if snapshot.HalfOpen {
		health = 0
	}
	return BalanceScore{
		AlgorithmVersion:       "objective_v1",
		ConcurrencyRemaining:   concurrencyRemaining,
		TPMRemaining:           tpmRemaining,
		CapacityScore:          capacity * 100,
		HealthScore:            health * 100,
		ErrorRate:              errorRate,
		ErrorSamples:           snapshot.ErrorSamples,
		TTFTEWMAMs:             snapshot.TTFTEWMAMs,
		TTFTSamples:            snapshot.TTFTSamples,
		TTFTSampleSource:       "stream_only",
		LatencyPenalty:         latencyPenalty,
		RoutingFactor:          health,
		CostFactor:             1,
		RoutingBalanceRevision: config.Revision,
		Weight:                 0,
		Pressure:               combinedPressure(concurrencyRemaining != nil, concurrencyPressure, tpmRemaining != nil, tpmPressure),
		CapacityUnknown:        concurrencyRemaining == nil && tpmRemaining == nil,
		HalfOpen:               snapshot.HalfOpen,
	}
}

// ScoreBalanceCandidateWithConfig is the shared read-only scorer for Gateway routing and Admin.
// Callers must pass the active routing-balance payload returned by SnapshotMany.
func ScoreBalanceCandidateWithConfig(snapshot ChannelCapacity, config BalanceConfig) BalanceScore {
	return scoreCapacity(snapshot, config)
}

// ApplyObjectiveFactors completes the shared objective score for Gateway and Admin.
func ApplyObjectiveFactors(score BalanceScore, costRatio float64, priority int32, config BalanceConfig) BalanceScore {
	config = normalizedBalanceConfig(config)
	score.CostRatio = costRatio
	score.EconomicScore = (1 - clamp01(costRatio)) * 100
	score.Priority = priority
	score.PriorityScore = clamp01(float64(100-priority)/100) * 100
	score.EconomicWeightPct = config.EconomicWeightPct
	score.HealthWeightPct = config.HealthWeightPct
	score.CapacityWeightPct = config.CapacityWeightPct
	score.PriorityWeightPct = config.PriorityWeightPct
	score.FinalScore = (score.EconomicScore*float64(config.EconomicWeightPct) +
		score.HealthScore*float64(config.HealthWeightPct) +
		score.CapacityScore*float64(config.CapacityWeightPct) +
		score.PriorityScore*float64(config.PriorityWeightPct)) / 100
	if score.HalfOpen {
		score.FinalScore = 0
	}
	// Legacy fields remain populated for old Admin clients while algorithm_version selects new semantics.
	score.CostWeight = float64(config.EconomicWeightPct) / 100
	score.CostFactor = score.EconomicScore / 100
	score.Weight = score.FinalScore
	return score
}

// ApplyCostFactor is retained as a source-compatible wrapper for older internal tests.
func ApplyCostFactor(score BalanceScore, costRatio float64, config BalanceConfig) BalanceScore {
	return ApplyObjectiveFactors(score, costRatio, 0, config)
}

func recordNeutralCostFactor(score BalanceScore, costRatio float64, config BalanceConfig) BalanceScore {
	return ApplyObjectiveFactors(score, costRatio, 0, config)
}

func normalizedBalanceConfig(config BalanceConfig) BalanceConfig {
	if config.TTFTTargetMs <= 0 {
		config.TTFTTargetMs = defaultTTFTTargetMs
	}
	if config.TTFTWeight < 0 || config.TTFTWeight > 1 {
		config.TTFTWeight = defaultTTFTWeight
	}
	if config.EconomicWeightPct < 0 || config.HealthWeightPct < 0 || config.CapacityWeightPct < 0 || config.PriorityWeightPct < 0 ||
		config.EconomicWeightPct+config.HealthWeightPct+config.CapacityWeightPct+config.PriorityWeightPct != 100 {
		config.EconomicWeightPct = 45
		config.HealthWeightPct = 25
		config.CapacityWeightPct = 20
		config.PriorityWeightPct = 10
	}
	return config
}

func combinedPressure(concurrencyKnown bool, concurrencyPressure float64, tpmKnown bool, tpmPressure float64) float64 {
	switch {
	case concurrencyKnown && tpmKnown:
		return (concurrencyPressure + tpmPressure) / 2
	case concurrencyKnown:
		return concurrencyPressure
	case tpmKnown:
		return tpmPressure
	default:
		return 0
	}
}

func remainingRatio(signal CapacitySignal) (*float64, float64) {
	if !signal.Known {
		return nil, 0
	}
	if signal.Limit <= 0 {
		value := 1.0
		return &value, 0
	}
	used := max(signal.Used, 0)
	pressure := clamp01(float64(used) / float64(signal.Limit))
	remaining := 1 - pressure
	return &remaining, pressure
}

func clamp01(value float64) float64 {
	if math.IsNaN(value) || value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}
