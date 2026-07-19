package lifecycle

import (
	"context"
	"errors"
	"math/rand/v2"
	"sync/atomic"

	"github.com/ThankCat/unio-gateway/internal/core/billing"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

var (
	// ErrCandidateInputTokenEstimateInvalid 表示候选输入 token 估算结果不满足冻结边界。
	ErrCandidateInputTokenEstimateInvalid = errors.New("candidate input token estimate invalid")
)

// CandidateInputTokenEstimator 使用协议 service 持有的 typed DTO，估算某个 routing 候选的输入 token。
//
// lifecycle 不接触 OpenAI 或 Anthropic DTO。协议 service 在 closure 内按 candidate.AdapterKey
// 查找对应 tokenizer，并使用 candidate.UpstreamModel 构造协议族自己的 tokenizer 请求。
type CandidateInputTokenEstimator func(ctx context.Context, candidate routing.ChatRouteCandidate) (int64, error)

// CandidateAvailability 判断某个候选当前是否可进入 fallback plan。
//
// 它用于过滤已经熔断且仍在冷却中的 channel。实现必须是只读检查，不能提前占用 half-open
// 探测名额；真正尝试前仍由调用方执行一次带状态推进的 Allow。
type CandidateAvailability func(candidate routing.ChatRouteCandidate) bool

// CandidateCapabilityRegistry 定义共享 executor 查询 adapter capability 所需的最小边界。
type CandidateCapabilityRegistry interface {
	FilterCandidates(protocol string, candidates []routing.ChatRouteCandidate, capabilities ...AdapterCapability) []routing.ChatRouteCandidate
}

// CandidatePreparer 定义协议 service 在 authorization 前生成保守 fallback plan 所需的共享能力。
type CandidatePreparer interface {
	PrepareCandidates(ctx context.Context, params PrepareCandidatesParams) (CandidatePlan, error)
}

// PrepareCandidatesParams 表示生成一次 operation 候选计划所需参数。
type PrepareCandidatesParams struct {
	// Protocol 是 ingress 协议族；registry 只保留同协议代码能力。
	Protocol string

	// Candidates 是 SQL routing 已按数据库事实选出的同协议候选。
	Candidates []routing.ChatRouteCandidate

	// Capabilities 是本次 operation 在调用上游前必须具备的代码能力。
	Capabilities []AdapterCapability

	// Available 过滤当前处于熔断冷却期的 channel；nil 表示全部可用。
	Available CandidateAvailability

	// FailurePreferred 是失败软冷却的「偏好」判定（DEC-029）：返回 false 的候选不被剔除，
	// 而是 demote 到 fallback 顺序末尾——健康候选优先，软冷却候选仍作最后兜底。
	// 全部候选都在软冷却中时顺序不变，故唯一候选场景行为完全不受影响（唯一渠道保护）。
	// nil 表示不启用软偏好。
	FailurePreferred CandidateAvailability

	// EstimateInputTokens 对每个可用 fallback candidate 做 provider-specific 保守估算。
	EstimateInputTokens CandidateInputTokenEstimator

	// Mode 是线路策略（balanced/fixed）；fixed 保持唯一候选，balanced 按容量和健康度排序。
	// 排序叠加在能力过滤/熔断可用性之前，故最终 fallback 顺序即策略顺序。
	Mode string

	// ChannelHealthScore 给 balanced 提供渠道健康分（0 最佳、1 最差）；nil 时使用中性健康因子。
	ChannelHealthScore func(channelKey string) float64

	// ChannelCapacitySnapshot 只读返回 channel-global 并发/TPM 容量，不得预占配额。
	// nil 或读取失败时按未知信号退化，真正 admit 仍由 attempt runner 原子执行。
	ChannelCapacitySnapshot ChannelCapacitySnapshotReader

	// StickyChannelID 是会话粘性命中的既有绑定渠道 ID（0=无绑定/未启用）。非 0 时该渠道候选被
	// 置顶，绝对优先于 Mode 排序与失败软冷却 demote（R5）；其余候选仍按策略序作 fallback。
	// 该渠道已被硬摘除（不在候选池 / 能力不符 / 熔断 open）时置顶落空，由 CandidatePlan.StickyPinned
	// 报告，调用方据此清除绑定重选。
	StickyChannelID int64
}

// Candidate 是共享 lifecycle 已过滤并估算过的一个可尝试候选。
type Candidate struct {
	// RouteIndex 是候选在 SQL routing plan 中的原始位置，用于 attempt 审计保留过滤空洞。
	RouteIndex int

	// Route 是 routing 返回的 channel 运行时参数与 provider/model 事实。
	Route routing.ChatRouteCandidate

	// Balance 是 balanced 调度使用的容量、健康与权重事实，供日志、trace 和 Admin 复用。
	Balance BalanceScore
}

// CandidatePlan 是 authorization 与 attempt 共用的保守 fallback 计划。
type CandidatePlan struct {
	// Candidates 保持 SQL routing 的稳定顺序，只包含具备 operation capability 且当前可用的候选。
	Candidates []Candidate

	// ConservativeInputTokens 是所有可用 fallback candidates 输入估算的最大值。
	ConservativeInputTokens int64

	// StickyPinned 报告 StickyChannelID 是否真的被置顶到 fallback 首位。
	// false 且请求带绑定时说明粘住渠道已被硬摘除，调用方应清除 sticky 绑定（R5）。
	StickyPinned bool

	// StickyPinnedNonPreferred 报告置顶发生了实际重排（sticky 渠道并非策略排序首选）。
	// 该占比用于观察 sticky 覆盖 balanced 首选顺序的频率。
	StickyPinnedNonPreferred bool

	// CapacityDegraded 表示至少一个容量快照读取失败；AllCapacityZero 表示所有候选容量分均为 0。
	CapacityDegraded bool
	AllCapacityZero  bool

	// Excluded records capability and breaker/cooldown hard filters from the SQL candidate plan.
	Excluded []CandidateExclusion
}

type CandidateExclusion struct {
	ChannelID  int64
	RouteIndex int
	Reason     string
}

// CandidateSalePrices 提取候选池各命中渠道的当前售价，供保守预授权上界估算（阶段 15）。
func (p CandidatePlan) CandidateSalePrices() []billing.CustomerPriceSnapshot {
	prices := make([]billing.CustomerPriceSnapshot, 0, len(p.Candidates))
	for _, c := range p.Candidates {
		prices = append(prices, c.Route.SalePrice)
	}
	return prices
}

// LongContextPolicy 取候选池共用的长上下文策略（同一请求模型基准价窗口相同；取首个候选即可）。
func (p CandidatePlan) LongContextPolicy() billing.LongContextPolicy {
	if len(p.Candidates) == 0 {
		return billing.LongContextPolicy{}
	}
	return p.Candidates[0].Route.LongContextPolicy
}

// CandidateMaxOutputTokens 取候选池中各模型 models.max_output_tokens 的最大值（0 表示候选均未配置）。
// 客户未显式给出输出上限时，authorization 用它做保守冻结上界；取最大值保证 fallback 命中
// 任一候选时预冻结额度都足够（更大的输出上限只会冻结更多，不会少冻结）。
func (p CandidatePlan) CandidateMaxOutputTokens() int64 {
	maxOut := int64(0)
	for _, c := range p.Candidates {
		if c.Route.MaxOutputTokens > maxOut {
			maxOut = c.Route.MaxOutputTokens
		}
	}
	return maxOut
}

// Executor 放置 OpenAI 与 Anthropic 共享的 gateway 生命周期执行能力。
//
// 当前先收口 authorization 前的候选准备段；后续 attempt、settlement 与 delivery 迁移继续
// 复用该类型，避免协议 service 各自实现不同的 fallback 风险边界。
type Executor struct {
	registry CandidateCapabilityRegistry
	random   func() float64
	balance  atomic.Pointer[BalanceConfig]
}

// BalanceConfig 是 balanced 的热更新开关。
type BalanceConfig struct {
	Enabled           bool
	WeightByRemaining bool
}

// NewExecutor 创建共享 lifecycle executor。
func NewExecutor(registry CandidateCapabilityRegistry) *Executor {
	if registry == nil {
		panic("lifecycle: adapter capability registry is required")
	}

	executor := &Executor{registry: registry, random: rand.Float64}
	executor.SetBalanceConfig(true, true)
	return executor
}

// SetBalanceConfig 原子替换 balanced 调度开关。
func (e *Executor) SetBalanceConfig(enabled, weightByRemaining bool) {
	if e == nil {
		return
	}
	e.balance.Store(&BalanceConfig{Enabled: enabled, WeightByRemaining: weightByRemaining})
}

// SetRandomSource 替换 balanced 随机源。生产默认使用 math/rand/v2，测试传固定 seed。
func (e *Executor) SetRandomSource(random func() float64) {
	if e == nil || random == nil {
		return
	}
	e.random = random
}

// PrepareCandidates 按 capability、熔断可用性和候选级保守估算生成 fallback plan。
func (e *Executor) PrepareCandidates(ctx context.Context, params PrepareCandidatesParams) (CandidatePlan, error) {
	if params.EstimateInputTokens == nil {
		return CandidatePlan{}, candidateEstimateFailure(
			ErrCandidateInputTokenEstimateInvalid,
			"candidate input token estimator is missing",
		)
	}

	// 先做能力与 breaker/cooldown 硬过滤，再读取容量并排序，避免为不可尝试渠道读取运行时状态。
	filtered := e.registry.FilterCandidates(params.Protocol, params.Candidates, params.Capabilities...)
	filteredIDs := make(map[int64]struct{}, len(filtered))
	for _, candidate := range filtered {
		filteredIDs[candidate.Channel.ID] = struct{}{}
	}
	excluded := make([]CandidateExclusion, 0, len(params.Candidates)-len(filtered))
	for index, candidate := range params.Candidates {
		if _, ok := filteredIDs[candidate.Channel.ID]; !ok {
			excluded = append(excluded, CandidateExclusion{ChannelID: candidate.Channel.ID, RouteIndex: index, Reason: "capability_unsupported"})
		}
	}
	available := make([]routing.ChatRouteCandidate, 0, len(filtered))
	for _, candidate := range filtered {
		if params.Available == nil || params.Available(candidate) {
			available = append(available, candidate)
		} else {
			excluded = append(excluded, CandidateExclusion{
				ChannelID: candidate.Channel.ID, RouteIndex: candidateRouteIndexes(params.Candidates)[candidate.Channel.ID], Reason: "breaker_or_cooldown",
			})
		}
	}
	config := e.balance.Load()
	if config == nil {
		config = &BalanceConfig{Enabled: true, WeightByRemaining: true}
	}
	ordered, scores, degraded, allZero := orderBalancedCandidates(
		ctx, available, params.Mode, params.ChannelCapacitySnapshot, params.ChannelHealthScore, e.random, *config,
	)
	routeIndexes := candidateRouteIndexes(params.Candidates)

	plan := CandidatePlan{
		Candidates:       make([]Candidate, 0, len(ordered)),
		CapacityDegraded: degraded,
		AllCapacityZero:  allZero,
		Excluded:         excluded,
	}
	for _, candidate := range ordered {
		inputTokens, err := params.EstimateInputTokens(ctx, candidate)
		if err != nil {
			return CandidatePlan{}, candidateEstimateFailure(err, "estimate candidate input tokens")
		}
		if inputTokens < 0 {
			return CandidatePlan{}, candidateEstimateFailure(
				ErrCandidateInputTokenEstimateInvalid,
				"candidate input token estimate must not be negative",
			)
		}

		plan.Candidates = append(plan.Candidates, Candidate{
			RouteIndex: routeIndexes[candidate.Channel.ID],
			Route:      candidate,
			Balance:    scores[candidate.Channel.ID],
		})
		if inputTokens > plan.ConservativeInputTokens {
			plan.ConservativeInputTokens = inputTokens
		}
	}

	if len(plan.Candidates) == 0 {
		return CandidatePlan{}, failure.Wrap(
			failure.CodeRoutingNoAvailableChannel,
			routing.ErrNoAvailableChannel,
			failure.WithMessage(routing.ErrNoAvailableChannel.Error()),
		)
	}

	// 失败软冷却 demote（DEC-029）：健康候选保序在前，软冷却候选保序垫后。
	// 只重排不剔除——候选总数与保守估算不变，唯一候选时顺序天然不变。
	plan.Candidates = demoteFailureCooled(plan.Candidates, params.FailurePreferred)

	// 会话粘性置顶（大 uncache 缺口 P0）：sticky 绑定渠道移到 fallback 首位，绝对优先于
	// mode 排序与软冷却 demote（R5，故意放在 demote 之后）。渠道已被硬摘除时置顶落空
	// （StickyPinned=false），由调用方清除绑定；其余候选顺序不受影响。
	if params.StickyChannelID != 0 {
		plan.Candidates, plan.StickyPinned, plan.StickyPinnedNonPreferred = pinStickyCandidate(plan.Candidates, params.StickyChannelID)
	}

	return plan, nil
}

// pinStickyCandidate 把命中 channelID 的候选稳定移到列表首位（其余保持相对顺序）。
// 未找到时原样返回 pinned=false；reordered 报告置顶是否发生实际重排（渠道原本不在首位）。
func pinStickyCandidate(candidates []Candidate, channelID int64) (out []Candidate, pinned bool, reordered bool) {
	for i, c := range candidates {
		if c.Route.Channel.ID != channelID {
			continue
		}
		if i == 0 {
			return candidates, true, false
		}
		result := make([]Candidate, 0, len(candidates))
		result = append(result, candidates[i])
		result = append(result, candidates[:i]...)
		result = append(result, candidates[i+1:]...)
		return result, true, true
	}
	return candidates, false, false
}

// demoteFailureCooled 把软冷却中的候选稳定移到列表末尾（不剔除、组内保持原相对顺序）。
// preferred 为 nil 时原样返回。
func demoteFailureCooled(candidates []Candidate, preferred CandidateAvailability) []Candidate {
	if preferred == nil || len(candidates) < 2 {
		return candidates
	}

	healthy := make([]Candidate, 0, len(candidates))
	cooled := make([]Candidate, 0)
	for _, c := range candidates {
		if preferred(c.Route) {
			healthy = append(healthy, c)
		} else {
			cooled = append(cooled, c)
		}
	}
	if len(cooled) == 0 || len(healthy) == 0 {
		return candidates
	}
	return append(healthy, cooled...)
}

func candidateRouteIndexes(candidates []routing.ChatRouteCandidate) map[int64]int {
	indexes := make(map[int64]int, len(candidates))
	for index, candidate := range candidates {
		if _, exists := indexes[candidate.Channel.ID]; !exists {
			indexes[candidate.Channel.ID] = index
		}
	}
	return indexes
}

func candidateEstimateFailure(cause error, message string) error {
	return failure.Wrap(
		failure.CodeGatewayInputTokenEstimateFailed,
		cause,
		failure.WithMessage(message),
	)
}
