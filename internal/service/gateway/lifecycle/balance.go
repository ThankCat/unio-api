package lifecycle

import (
	"context"
	"math"
	"sort"

	"github.com/ThankCat/unio-gateway/internal/core/routing"
)

// ObjectiveAlgorithmVersion 是 Gateway 与 Admin 共享的唯一评分算法版本标识。
const ObjectiveAlgorithmVersion = "objective_v1"

// 五项评分的默认参数（配置缺失或非法时的回退，与 appsettings 默认一致，§14.6）。
const (
	defaultCostWeightPct                = 25
	defaultConcurrencyWeightPct         = 20
	defaultTTFTWeightPct                = 25
	defaultErrorRateWeightPct           = 20
	defaultPriorityWeightPct            = 10
	defaultTTFTPenaltyUnitMs            = int64(1000)
	defaultTTFTPenaltyPointsPerUnit     = 2.5
	defaultErrorPenaltyPointsPerPercent = 2.5
)

// CapacitySignal 是一个 channel-global 容量维度的只读事实。Limit<=0 表示显式不限。
type CapacitySignal struct {
	Used  int64
	Limit int64
	Known bool
}

// ChannelScoreInputs 是五项评分对某个渠道的全部运行态输入。
//
// 并发来自 Redis 原子快照；TTFT 与错误率来自 30 分钟分钟桶聚合（§12），
// 与 breaker 自身的 open/close 计数解耦：breaker 只负责资格，不再参与评分。
type ChannelScoreInputs struct {
	Concurrency CapacitySignal

	// 30 分钟窗口聚合（分钟对齐）。Count 为 0 表示无样本，对应指标分为 100。
	TTFTSumMs         int64
	TTFTCount         int64
	ErrorAttemptCount int64
	ErrorCount        int64

	HalfOpen     bool
	RuntimeKnown bool
}

// ChannelScoreSnapshotReader 读取候选渠道的评分输入；读取不能产生预占或推进状态机。
type ChannelScoreSnapshotReader func(context.Context, routing.ChatRouteCandidate) (ChannelScoreInputs, error)

// BalanceScore 保存一次候选评分的完整组成，供调度、trace 和 Admin 共用。
type BalanceScore struct {
	AlgorithmVersion                 string
	ProviderID                       int64
	CandidateOriginRevision          int64
	RuntimeOriginRevision            int64
	OriginRevisionCurrent            bool
	CandidateProviderStatusRevision  int64
	RuntimeProviderStatusRevision    int64
	ProviderStatusRevisionCurrent    bool
	CandidateChannelConfigRevision   int64
	RuntimeChannelConfigRevision     *int64
	ChannelConfigRevisionCurrent     bool
	CandidateChannelCapacityRevision int64
	RuntimeChannelCapacityRevision   int64
	ChannelCapacityRevisionCurrent   bool
	RouteRateLimitsRevision          int64
	GlobalConcurrencyRevision        int64
	CircuitBreakerRevision           int64
	RoutingBalanceRevision           int64

	// 五项指标分（各自 [0,100]）与权重、贡献。
	CostScore        float64
	ConcurrencyScore float64
	TTFTScore        float64
	ErrorScore       float64
	PriorityScore    float64

	CostWeightPct        int
	ConcurrencyWeightPct int
	TTFTWeightPct        int
	ErrorRateWeightPct   int
	PriorityWeightPct    int

	// FinalScore 是五项加权总分，限制在 [0,100]。
	FinalScore float64

	// 指标输入事实（供 Admin/trace 解释评分，不参与二次计算）。
	CostRatio            float64
	Priority             int32
	ConcurrencyRemaining *float64
	AvgTTFTMs            float64
	TTFTSampleCount      int64
	ErrorRatePct         float64
	ErrorSampleCount     int64

	CapacityUnknown    bool
	CapacityReadFailed bool

	RuntimeControlState         string
	RuntimeRevisionCurrent      bool
	ProviderBreakerState        string
	ChannelBreakerState         string
	CooldownRemainingMs         int64
	ModelPermissionPaused       bool
	ModelPermissionRecheckState string
	BreakerStoreAdmission       string
	HalfOpen                    bool
}

type scoredCandidate struct {
	route routing.ChatRouteCandidate
	score BalanceScore
}

func orderBalancedCandidates(
	ctx context.Context,
	in []routing.ChatRouteCandidate,
	mode string,
	inputs ChannelScoreSnapshotReader,
	config BalanceConfig,
) ([]routing.ChatRouteCandidate, map[int64]BalanceScore, bool) {
	entries := make([]scoredCandidate, 0, len(in))
	scores := make(map[int64]BalanceScore, len(in))
	if mode != "balanced" {
		out := append([]routing.ChatRouteCandidate(nil), in...)
		for _, candidate := range out {
			snapshot := ChannelScoreInputs{}
			if inputs != nil {
				if value, err := inputs(ctx, candidate); err == nil {
					snapshot = value
				}
			}
			scores[candidate.Channel.ID] = ScoreChannel(snapshot, candidate.CostRatio, candidate.Priority, config)
		}
		return out, scores, false
	}

	allZero := len(in) > 0
	for _, candidate := range in {
		snapshot := ChannelScoreInputs{}
		readFailed := false
		if inputs != nil {
			var err error
			snapshot, err = inputs(ctx, candidate)
			if err != nil {
				readFailed = true
				snapshot = ChannelScoreInputs{}
			}
		}
		score := ScoreChannel(snapshot, candidate.CostRatio, candidate.Priority, config)
		score.CapacityReadFailed = readFailed
		if score.ConcurrencyScore > 0 {
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

	// 稳定顺序（§7.7）：总分降序 → Priority 升序 → Channel ID 升序。不得加入随机抖动。
	sort.SliceStable(closed, func(i, j int) bool {
		if closed[i].score.FinalScore != closed[j].score.FinalScore {
			return closed[i].score.FinalScore > closed[j].score.FinalScore
		}
		if closed[i].route.Priority != closed[j].route.Priority {
			return closed[i].route.Priority < closed[j].route.Priority
		}
		return closed[i].route.Channel.ID < closed[j].route.Channel.ID
	})
	// half-open 只由熔断探测许可控制，不与普通流量竞争，固定排在普通候选之后（§3.3）。
	entries = append(closed, halfOpen...)

	out := make([]routing.ChatRouteCandidate, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.route)
		scores[entry.route.Channel.ID] = entry.score
	}
	return out, scores, allZero
}

// ScoreChannel 是 Gateway 路由与 Admin 展示共用的只读五项评分纯函数（§7）。
//
//	final = cost*w_cost + concurrency*w_conc + ttft*w_ttft + error*w_err + priority*w_prio
//
// 每项指标分限制在 [0,100]，总分限制在 [0,100]。
func ScoreChannel(
	in ChannelScoreInputs,
	costRatio float64,
	priority int32,
	config BalanceConfig,
) BalanceScore {
	config = normalizedBalanceConfig(config)

	concurrencyRemaining := remainingRatio(in.Concurrency)
	concurrencyScore := 100.0
	if concurrencyRemaining != nil {
		concurrencyScore = clamp01(*concurrencyRemaining) * 100
	}

	// 成本分（§7.2）：cost_ratio 为七个归一化计价分项中最大的渠道成本/客户售价比。
	costScore := (1 - clamp01(costRatio)) * 100

	// TTFT 分（§7.4）：30 分钟算术均值，每 penalty_unit 扣 points；无样本得 100。
	avgTTFTMs := 0.0
	ttftScore := 100.0
	if in.TTFTCount > 0 {
		avgTTFTMs = float64(in.TTFTSumMs) / float64(in.TTFTCount)
		units := avgTTFTMs / float64(config.TTFTPenaltyUnitMs)
		ttftScore = clampScore(100 - units*config.TTFTPenaltyPointsPerUnit)
	}

	// 错误率分（§7.5）：每 1% 错误率扣 points；无样本得 100。
	errorRatePct := 0.0
	errorScore := 100.0
	if in.ErrorAttemptCount > 0 {
		errorRatePct = float64(in.ErrorCount) / float64(in.ErrorAttemptCount) * 100
		errorScore = clampScore(100 - errorRatePct*config.ErrorPenaltyPointsPerPercent)
	}

	// Priority 分（§7.6）：数值越小越优先，0 得 100 分。
	priorityScore := clamp01(float64(100-priority)/100) * 100

	final := (costScore*float64(config.CostWeightPct) +
		concurrencyScore*float64(config.ConcurrencyWeightPct) +
		ttftScore*float64(config.TTFTWeightPct) +
		errorScore*float64(config.ErrorRateWeightPct) +
		priorityScore*float64(config.PriorityWeightPct)) / 100

	score := BalanceScore{
		AlgorithmVersion:       ObjectiveAlgorithmVersion,
		CostScore:              costScore,
		ConcurrencyScore:       concurrencyScore,
		TTFTScore:              ttftScore,
		ErrorScore:             errorScore,
		PriorityScore:          priorityScore,
		CostWeightPct:          config.CostWeightPct,
		ConcurrencyWeightPct:   config.ConcurrencyWeightPct,
		TTFTWeightPct:          config.TTFTWeightPct,
		ErrorRateWeightPct:     config.ErrorRateWeightPct,
		PriorityWeightPct:      config.PriorityWeightPct,
		FinalScore:             clampScore(final),
		CostRatio:              costRatio,
		Priority:               priority,
		ConcurrencyRemaining:   concurrencyRemaining,
		AvgTTFTMs:              avgTTFTMs,
		TTFTSampleCount:        in.TTFTCount,
		ErrorRatePct:           errorRatePct,
		ErrorSampleCount:       in.ErrorAttemptCount,
		RoutingBalanceRevision: config.Revision,
		CapacityUnknown:        concurrencyRemaining == nil,
		HalfOpen:               in.HalfOpen,
	}
	// half-open 候选不参与普通排序竞争，总分归零并固定排在普通候选之后。
	if in.HalfOpen {
		score.FinalScore = 0
	}
	return score
}

func normalizedBalanceConfig(config BalanceConfig) BalanceConfig {
	if config.CostWeightPct < 0 || config.ConcurrencyWeightPct < 0 || config.TTFTWeightPct < 0 ||
		config.ErrorRateWeightPct < 0 || config.PriorityWeightPct < 0 ||
		config.CostWeightPct+config.ConcurrencyWeightPct+config.TTFTWeightPct+
			config.ErrorRateWeightPct+config.PriorityWeightPct != 100 {
		config.CostWeightPct = defaultCostWeightPct
		config.ConcurrencyWeightPct = defaultConcurrencyWeightPct
		config.TTFTWeightPct = defaultTTFTWeightPct
		config.ErrorRateWeightPct = defaultErrorRateWeightPct
		config.PriorityWeightPct = defaultPriorityWeightPct
	}
	if config.TTFTPenaltyUnitMs <= 0 {
		config.TTFTPenaltyUnitMs = defaultTTFTPenaltyUnitMs
	}
	if config.TTFTPenaltyPointsPerUnit <= 0 {
		config.TTFTPenaltyPointsPerUnit = defaultTTFTPenaltyPointsPerUnit
	}
	if config.ErrorPenaltyPointsPerPercent <= 0 {
		config.ErrorPenaltyPointsPerPercent = defaultErrorPenaltyPointsPerPercent
	}
	return config
}

// remainingRatio 返回剩余比例（§7.3）。未知返回 nil；Limit<=0 表示不限，得满分。
func remainingRatio(signal CapacitySignal) *float64 {
	if !signal.Known {
		return nil
	}
	if signal.Limit <= 0 {
		value := 1.0
		return &value
	}
	used := max(signal.Used, 0)
	remaining := 1 - clamp01(float64(used)/float64(signal.Limit))
	return &remaining
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

func clampScore(value float64) float64 {
	if math.IsNaN(value) || value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}
