package lifecycle

import (
	"context"
	"math"
	"sort"

	"github.com/ThankCat/unio-gateway/internal/core/routing"
)

const (
	defaultTTFTTargetMs         = int64(2000)
	defaultTTFTWeight           = 0.35
	defaultMinimumRoutingFactor = 0.05
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
	TTFTEWMAMs   float64
	TTFTSamples  int64
	HalfOpen     bool
	RuntimeKnown bool
}

// ChannelCapacitySnapshotReader 读取候选渠道的全局容量；读取不能产生预占或推进状态机。
type ChannelCapacitySnapshotReader func(context.Context, routing.ChatRouteCandidate) (ChannelCapacity, error)

// BalanceScore 保存一次候选评分的完整组成，供调度、trace 和运行时后台共用。
type BalanceScore struct {
	OriginID                              int64
	CandidateOriginBaseURLRevision        int64
	RuntimeOriginBaseURLRevision          int64
	OriginBaseURLRevisionCurrent          bool
	CandidateOriginStatusRevision         int64
	RuntimeOriginStatusRevision           int64
	OriginStatusRevisionCurrent           bool
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
	OriginBreakerState                    string
	ChannelBreakerState                     string
	CooldownRemainingMs                     int64
	ModelPermissionPaused                   bool
	ModelPermissionRecheckState             string
	BreakerStoreAdmission                   string
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
	random func() float64,
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
			score = recordNeutralCostFactor(score, candidate.CostRatio, config)
			scores[candidate.Channel.ID] = score
		}
		return out, scores, false
	}

	allUnknown := true
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
		score = ApplyCostFactor(score, candidate.CostRatio, config)
		score.CapacityReadFailed = readFailed
		if !score.CapacityUnknown {
			allUnknown = false
		}
		if score.CapacityScore > 0 {
			allZero = false
		}
		entries = append(entries, scoredCandidate{route: candidate, score: score})
	}

	// 所有容量维度均未知时让容量和健康度退化为中性值，仍保留已冻结的成本因子。
	if allUnknown {
		allZero = false
		for i := range entries {
			entries[i].score.CapacityScore = 1
			entries[i].score.RoutingFactor = 1
			entries[i].score.Weight = entries[i].score.CostFactor
		}
	}

	closed := make([]scoredCandidate, 0, len(entries))
	halfOpen := make([]scoredCandidate, 0)
	for _, entry := range entries {
		if entry.score.Weight == 0 && entry.score.RoutingFactor == 0 {
			halfOpen = append(halfOpen, entry)
			continue
		}
		closed = append(closed, entry)
	}

	if allZero && len(closed) > 0 {
		// 所有候选容量为零时，最低压力候选排首进入现有队首短等；其余仍保留在线路内 fallback。
		sort.SliceStable(closed, func(i, j int) bool { return closed[i].score.Pressure < closed[j].score.Pressure })
	} else {
		closed = weightedWithoutReplacement(closed, random)
	}
	// half-open 只保序进入独立 probe fallback，不参与普通 weighted random。
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
	routingFactor := math.Max(config.MinimumRoutingFactor,
		(1-errorRate)*(1-config.TTFTWeight*latencyPenalty))
	weight := capacity * routingFactor
	if snapshot.HalfOpen {
		routingFactor = 0
		weight = 0
	}
	return BalanceScore{
		ConcurrencyRemaining:   concurrencyRemaining,
		TPMRemaining:           tpmRemaining,
		CapacityScore:          capacity,
		ErrorRate:              errorRate,
		TTFTEWMAMs:             snapshot.TTFTEWMAMs,
		TTFTSamples:            snapshot.TTFTSamples,
		TTFTSampleSource:       "stream_only",
		LatencyPenalty:         latencyPenalty,
		RoutingFactor:          routingFactor,
		CostFactor:             1,
		RoutingBalanceRevision: config.Revision,
		Weight:                 weight,
		Pressure:               combinedPressure(concurrencyRemaining != nil, concurrencyPressure, tpmRemaining != nil, tpmPressure),
		CapacityUnknown:        concurrencyRemaining == nil && tpmRemaining == nil,
	}
}

// ScoreBalanceCandidateWithConfig is the shared read-only scorer for Gateway routing and Admin.
// Callers must pass the active routing-balance payload returned by SnapshotMany.
func ScoreBalanceCandidateWithConfig(snapshot ChannelCapacity, config BalanceConfig) BalanceScore {
	return scoreCapacity(snapshot, config)
}

// ApplyCostFactor adds the cost-aware routing factor to an existing capacity and
// health score. Gateway and Admin share this helper so displayed and executed weights
// use exactly the same formula.
func ApplyCostFactor(score BalanceScore, costRatio float64, config BalanceConfig) BalanceScore {
	config = normalizedBalanceConfig(config)
	score.CostRatio = costRatio
	score.CostWeight = config.CostWeight
	score.CostFactor = math.Max(
		config.MinimumRoutingFactor,
		1-config.CostWeight*clamp01(costRatio),
	)
	score.Weight *= score.CostFactor
	return score
}

func recordNeutralCostFactor(score BalanceScore, costRatio float64, config BalanceConfig) BalanceScore {
	config = normalizedBalanceConfig(config)
	score.CostRatio = costRatio
	score.CostWeight = config.CostWeight
	score.CostFactor = 1
	return score
}

func normalizedBalanceConfig(config BalanceConfig) BalanceConfig {
	if config.TTFTTargetMs <= 0 {
		config.TTFTTargetMs = defaultTTFTTargetMs
	}
	if config.TTFTWeight < 0 || config.TTFTWeight > 1 {
		config.TTFTWeight = defaultTTFTWeight
	}
	if config.MinimumRoutingFactor <= 0 || config.MinimumRoutingFactor > 1 {
		config.MinimumRoutingFactor = defaultMinimumRoutingFactor
	}
	if math.IsNaN(config.CostWeight) || config.CostWeight < 0 || config.CostWeight > 1 {
		config.CostWeight = 0
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

func weightedWithoutReplacement(in []scoredCandidate, random func() float64) []scoredCandidate {
	if random == nil {
		random = func() float64 { return 0.5 }
	}
	positive := make([]scoredCandidate, 0, len(in))
	zero := make([]scoredCandidate, 0, len(in))
	for _, entry := range in {
		if entry.score.Weight > 0 {
			positive = append(positive, entry)
		} else {
			zero = append(zero, entry)
		}
	}

	out := make([]scoredCandidate, 0, len(in))
	for len(positive) > 0 {
		total := 0.0
		for _, entry := range positive {
			total += entry.score.Weight
		}
		draw := clampRandom(random()) * total
		selected := len(positive) - 1
		cumulative := 0.0
		for i, entry := range positive {
			cumulative += entry.score.Weight
			if draw < cumulative {
				selected = i
				break
			}
		}
		out = append(out, positive[selected])
		positive = append(positive[:selected], positive[selected+1:]...)
	}
	return append(out, zero...)
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

func clampRandom(value float64) float64 {
	if math.IsNaN(value) || value < 0 {
		return 0
	}
	if value >= 1 {
		return math.Nextafter(1, 0)
	}
	return value
}
