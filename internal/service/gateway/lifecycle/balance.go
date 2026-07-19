package lifecycle

import (
	"context"
	"math"
	"sort"

	"github.com/ThankCat/unio-gateway/internal/core/routing"
)

const minimumHealthFactor = 0.05

// CapacitySignal 是一个 channel-global 容量维度的只读事实。Limit<=0 表示显式不限。
type CapacitySignal struct {
	Used  int64
	Limit int64
	Known bool
}

// ChannelCapacity 是 balanced scorer 使用的并发和 TPM 快照。
type ChannelCapacity struct {
	Concurrency CapacitySignal
	TPM         CapacitySignal
}

// ChannelCapacitySnapshotReader 读取候选渠道的全局容量；读取不能产生预占或推进状态机。
type ChannelCapacitySnapshotReader func(context.Context, routing.ChatRouteCandidate) (ChannelCapacity, error)

// BalanceScore 保存一次候选评分的完整组成，供调度、trace 和运行时后台共用。
type BalanceScore struct {
	ConcurrencyRemaining *float64
	TPMRemaining         *float64
	CapacityScore        float64
	HealthFactor         float64
	Weight               float64
	Pressure             float64
	CapacityUnknown      bool
	CapacityReadFailed   bool
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
	health func(string) float64,
	random func() float64,
	config BalanceConfig,
) ([]routing.ChatRouteCandidate, map[int64]BalanceScore, bool, bool) {
	entries := make([]scoredCandidate, 0, len(in))
	scores := make(map[int64]BalanceScore, len(in))
	if mode != "balanced" {
		out := append([]routing.ChatRouteCandidate(nil), in...)
		for _, candidate := range out {
			score := BalanceScore{CapacityScore: 1, HealthFactor: 1, Weight: 1, CapacityUnknown: true}
			scores[candidate.Channel.ID] = score
		}
		return out, scores, false, false
	}

	degraded := false
	allUnknown := true
	allZero := len(in) > 0
	for _, candidate := range in {
		snapshot := ChannelCapacity{}
		readFailed := false
		if !config.Enabled || !config.WeightByRemaining {
			snapshot = ChannelCapacity{
				Concurrency: CapacitySignal{Limit: 0, Known: true},
				TPM:         CapacitySignal{Limit: 0, Known: true},
			}
		} else if capacity != nil {
			var err error
			snapshot, err = capacity(ctx, candidate)
			if err != nil {
				degraded, readFailed = true, true
				snapshot = ChannelCapacity{}
			}
		}
		healthValue := healthScore(health, candidate.Channel.ID)
		if !config.Enabled {
			healthValue = 0
		}
		score := scoreCapacity(snapshot, healthValue)
		score.CapacityReadFailed = readFailed
		if !score.CapacityUnknown {
			allUnknown = false
		}
		if score.CapacityScore > 0 {
			allZero = false
		}
		entries = append(entries, scoredCandidate{route: candidate, score: score})
	}

	// 所有容量维度均未知时严格退化为池内均匀随机，不让健康或 SQL priority 伪装成容量事实。
	if allUnknown {
		allZero = false
		for i := range entries {
			entries[i].score.CapacityScore = 1
			entries[i].score.HealthFactor = 1
			entries[i].score.Weight = 1
		}
	}

	if allZero {
		// 所有候选容量为零时，最低压力候选排首进入现有队首短等；其余仍保留在线路内 fallback。
		sort.SliceStable(entries, func(i, j int) bool { return entries[i].score.Pressure < entries[j].score.Pressure })
	} else {
		entries = weightedWithoutReplacement(entries, random)
	}

	out := make([]routing.ChatRouteCandidate, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.route)
		scores[entry.route.Channel.ID] = entry.score
	}
	return out, scores, degraded, allZero
}

func scoreCapacity(snapshot ChannelCapacity, healthScore float64) BalanceScore {
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
	healthFactor := math.Max(minimumHealthFactor, 1-clamp01(healthScore))
	return BalanceScore{
		ConcurrencyRemaining: concurrencyRemaining,
		TPMRemaining:         tpmRemaining,
		CapacityScore:        capacity,
		HealthFactor:         healthFactor,
		Weight:               capacity * healthFactor,
		Pressure:             combinedPressure(concurrencyRemaining != nil, concurrencyPressure, tpmRemaining != nil, tpmPressure),
		CapacityUnknown:      concurrencyRemaining == nil && tpmRemaining == nil,
	}
}

// ScoreBalanceCandidate exposes the scheduler's scorer for read-only admin diagnostics.
func ScoreBalanceCandidate(snapshot ChannelCapacity, healthScore float64) BalanceScore {
	return scoreCapacity(snapshot, healthScore)
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

func healthScore(health func(string) float64, channelID int64) float64 {
	if health == nil {
		return 0
	}
	return health(MetricsID(channelID))
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
