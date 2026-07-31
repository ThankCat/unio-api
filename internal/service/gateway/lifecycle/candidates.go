package lifecycle

import (
	"context"
	"errors"

	"github.com/ThankCat/unio-gateway/internal/core/billing"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/requestadmission"
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

	// EstimateInputTokens 对每个可用 fallback candidate 做 provider-specific 保守估算。
	EstimateInputTokens CandidateInputTokenEstimator

	// Mode 是线路策略（balanced/fixed）；fixed 保持唯一候选，balanced 按容量和健康度排序。
	// 排序叠加在能力过滤/熔断可用性之前，故最终 fallback 顺序即策略顺序。
	Mode string

	// StickyChannelID 是会话粘性命中的既有绑定渠道 ID（0=无绑定/未启用）。非 0 时该渠道候选被
	// 置顶，绝对优先于 Mode 排序与失败软冷却 demote（R5）；其余候选仍按策略序作 fallback。
	// 该渠道未进入可尝试候选时置顶落空，由 CandidatePlan.StickyPinned 报告。调用方必须结合 Excluded
	// 区分 cooldown 临时绕行与永久失格，不能仅凭 StickyPinned=false 清除绑定。
	StickyChannelID int64
}

// Candidate 是共享 lifecycle 已过滤并估算过的一个可尝试候选。
type Candidate struct {
	// RouteIndex 是候选在 SQL routing plan 中的原始位置，用于 attempt 审计保留过滤空洞。
	RouteIndex int

	// Route 是 routing 返回的 channel 运行时参数与 provider/model 事实。
	Route routing.ChatRouteCandidate

	// InputEstimate 是该候选最终 wire 的完整输入 token 估算。
	InputEstimate int64

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
	// false 且请求带绑定时说明粘住渠道未进入可尝试候选；具体是临时绕行还是永久失格由 Excluded 解释。
	StickyPinned bool

	// StickyPinnedNonPreferred 报告置顶发生了实际重排（sticky 渠道并非策略排序首选）。
	// 该占比用于观察 sticky 覆盖 balanced 首选顺序的频率。
	StickyPinnedNonPreferred bool

	// AllCapacityZero 表示所有候选容量分均为 0。
	AllCapacityZero bool

	// BaselineOrder 是 Sticky 置顶之前、纯按五项总分排序的基准顺序（channel_id 列表）。
	// trace 同时保留基准顺序与实际扫描顺序，才能解释「Sticky 把谁提到了前面」（§13.3）。
	BaselineOrder []int64

	// Excluded records capability and breaker/cooldown hard filters from the SQL candidate plan.
	Excluded []CandidateExclusion
}

type CandidateExclusion struct {
	ChannelID  int64
	RouteIndex int
	Reason     string
	Route      routing.ChatRouteCandidate
	Balance    BalanceScore
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
}

// BalanceConfig 是 Redis committed routing-balance control 的五项评分参数（objective_v1，§7/§14.6）。
type BalanceConfig struct {
	Revision             int64
	CostWeightPct        int
	ConcurrencyWeightPct int
	TTFTWeightPct        int
	ErrorRateWeightPct   int
	PriorityWeightPct    int

	TTFTPenaltyUnitMs        int64
	TTFTPenaltyPointsPerUnit float64

	ErrorPenaltyPointsPerPercent float64
}

// NewExecutor 创建共享 lifecycle executor。
func NewExecutor(registry CandidateCapabilityRegistry) *Executor {
	if registry == nil {
		panic("lifecycle: adapter capability registry is required")
	}

	return &Executor{registry: registry}
}

// PrepareCandidates 按 capability、熔断可用性和候选级保守估算生成 fallback plan。
func (e *Executor) PrepareCandidates(ctx context.Context, params PrepareCandidatesParams) (CandidatePlan, error) {
	if params.EstimateInputTokens == nil {
		return CandidatePlan{}, candidateEstimateFailure(
			ErrCandidateInputTokenEstimateInvalid,
			"candidate input token estimator is missing",
		)
	}

	// 先做代码能力过滤，再以一次 Redis SnapshotMany 同时完成所有运行态硬门禁和评分读取。
	filtered := e.registry.FilterCandidates(params.Protocol, params.Candidates, params.Capabilities...)
	filteredIDs := make(map[int64]struct{}, len(filtered))
	for _, candidate := range filtered {
		filteredIDs[candidate.Channel.ID] = struct{}{}
	}
	excluded := make([]CandidateExclusion, 0, len(params.Candidates)-len(filtered))
	for index, candidate := range params.Candidates {
		if _, ok := filteredIDs[candidate.Channel.ID]; !ok {
			excluded = append(excluded, CandidateExclusion{
				ChannelID: candidate.Channel.ID, RouteIndex: index,
				Reason: "capability_unsupported", Route: candidate,
			})
		}
	}
	if len(filtered) == 0 {
		return CandidatePlan{Excluded: excluded}, noAvailableCandidateError()
	}
	routeIndexes := candidateRouteIndexes(params.Candidates)
	runtimeInputs := make([]breakerstore.SnapshotCandidateInput, 0, len(filtered))
	for _, candidate := range filtered {
		runtimeInputs = append(runtimeInputs, breakerstore.SnapshotCandidateInput{
			ProviderID: candidate.ProviderID, ChannelID: candidate.Channel.ID,
			OriginRevision:          candidate.OriginRevision,
			ProviderStatusRevision:  candidate.ProviderStatusRevision,
			ChannelConfigRevision:   candidate.ChannelConfigRevision,
			ChannelCapacityRevision: candidate.ChannelCapacityRevision,
		})
	}
	runtimeResult, runtimePresent, err := requestadmission.SnapshotManyIfPresent(ctx, filtered[0].ModelDBID, runtimeInputs)
	if err != nil {
		return CandidatePlan{}, err
	}
	available := make([]routing.ChatRouteCandidate, 0, len(filtered))
	runtimeInputsByChannel := make(map[int64]ChannelScoreInputs, len(filtered))
	runtimeSnapshots := make(map[int64]breakerstore.CandidateSnapshot, len(filtered))
	capabilityExclusions := len(excluded)
	rateLimitedSnapshots := 0
	minCooldownRemainingMs := int64(0)
	if runtimePresent {
		if len(runtimeResult.Candidates) != len(filtered) {
			return CandidatePlan{}, failure.New(
				failure.CodeGatewayRuntimeSyncRequired,
				failure.WithMessage("candidate runtime snapshot count does not match routing candidates"),
			)
		}
		for index, candidate := range filtered {
			snapshot := runtimeResult.Candidates[index]
			runtimeSnapshots[candidate.Channel.ID] = snapshot
			switch snapshot.Status {
			case breakerstore.CandidateSnapshotCurrent, breakerstore.CandidateSnapshotNoSample,
				breakerstore.CandidateSnapshotHalfOpen:
				available = append(available, candidate)
				// 并发是 Redis 原子快照事实；TTFT/错误率稍后由 30 分钟样本聚合填入（§12）。
				runtimeInputsByChannel[candidate.Channel.ID] = ChannelScoreInputs{
					Concurrency:  CapacitySignal{Used: snapshot.Concurrency.Used, Limit: snapshot.Concurrency.Limit, Known: true},
					HalfOpen:     snapshot.Status == breakerstore.CandidateSnapshotHalfOpen,
					RuntimeKnown: true,
				}
			default:
				if snapshot.Status == breakerstore.CandidateSnapshotRateLimited {
					rateLimitedSnapshots++
					if snapshot.CooldownRemainingMs > 0 &&
						(minCooldownRemainingMs == 0 || snapshot.CooldownRemainingMs < minCooldownRemainingMs) {
						minCooldownRemainingMs = snapshot.CooldownRemainingMs
					}
				}
				excluded = append(excluded, CandidateExclusion{
					ChannelID: candidate.Channel.ID, RouteIndex: routeIndexes[candidate.Channel.ID],
					Reason: string(snapshot.Status), Route: candidate,
				})
			}
		}
	} else {
		// Direct unit tests and maintenance callers may omit the HTTP-owned request session.
		// Without the Redis snapshot there is no authoritative runtime signal, so preserve SQL order
		// with neutral scores instead of consulting retired in-process breaker/health hooks.
		available = append(available, filtered...)
	}
	var unavailableErr error
	if len(available) == 0 {
		if runtimePresent && capabilityExclusions == 0 && rateLimitedSnapshots == len(filtered) {
			unavailableErr = failure.New(
				failure.CodeGatewayChannelRateLimited,
				failure.WithMessage("all candidate channels are in upstream rate-limit cooldown"),
				failure.WithField("retry_after_ms", minCooldownRemainingMs),
			)
		} else {
			unavailableErr = noAvailableCandidateError()
		}
	}
	selectedConfig := BalanceConfig{}
	orderingMode := params.Mode
	var scoreReader ChannelScoreSnapshotReader
	var sampleWindows map[int64]breakerstore.ChannelSampleWindow
	if runtimePresent {
		selectedConfig = BalanceConfig{
			Revision:                     runtimeResult.RoutingBalance.Revision,
			CostWeightPct:                runtimeResult.RoutingBalance.CostWeightPct,
			ConcurrencyWeightPct:         runtimeResult.RoutingBalance.ConcurrencyWeightPct,
			TTFTWeightPct:                runtimeResult.RoutingBalance.TTFTWeightPct,
			ErrorRateWeightPct:           runtimeResult.RoutingBalance.ErrorRateWeightPct,
			PriorityWeightPct:            runtimeResult.RoutingBalance.PriorityWeightPct,
			TTFTPenaltyUnitMs:            runtimeResult.RoutingBalance.TTFTPenaltyUnitMs,
			TTFTPenaltyPointsPerUnit:     runtimeResult.RoutingBalance.TTFTPenaltyPointsPerUnit,
			ErrorPenaltyPointsPerPercent: runtimeResult.RoutingBalance.ErrorPenaltyPointsPerPercent,
		}
		// 30 分钟评分样本（§12）：TTFT 与错误率的唯一来源；缺失即“无样本得满分”。
		scoredChannelIDs := make([]int64, 0, len(runtimeInputsByChannel))
		for channelID := range runtimeInputsByChannel {
			scoredChannelIDs = append(scoredChannelIDs, channelID)
		}
		for channelID := range runtimeSnapshots {
			if _, scored := runtimeInputsByChannel[channelID]; !scored {
				scoredChannelIDs = append(scoredChannelIDs, channelID)
			}
		}
		sampleWindows = requestadmission.AggregateChannelSamplesIfPresent(ctx, scoredChannelIDs)
		for channelID, window := range sampleWindows {
			inputs, ok := runtimeInputsByChannel[channelID]
			if !ok {
				continue
			}
			inputs.TTFTSumMs = window.TTFTSumMs
			inputs.TTFTCount = window.TTFTCount
			inputs.ErrorAttemptCount = window.ErrorAttemptCount
			inputs.ErrorCount = window.ErrorCount
			runtimeInputsByChannel[channelID] = inputs
		}
		scoreReader = func(_ context.Context, candidate routing.ChatRouteCandidate) (ChannelScoreInputs, error) {
			return runtimeInputsByChannel[candidate.Channel.ID], nil
		}
	} else {
		// A missing request session has no authoritative facts to justify weighted routing.
		// Preserve the SQL order while still producing neutral scores for test/maintenance callers.
		orderingMode = ""
	}
	ordered, scores, allZero := orderBalancedCandidates(
		ctx, available, orderingMode, scoreReader, selectedConfig,
	)
	if runtimePresent {
		for _, candidate := range available {
			score := scores[candidate.Channel.ID]
			scores[candidate.Channel.ID] = enrichBalanceScore(score, candidate, runtimeSnapshots[candidate.Channel.ID], runtimeResult)
		}
		// 被排除的候选也要展示完整评分事实（Admin 候选表显示整个基础池，§15.2），但顺序为空、总分不参与排序。
		for index := range excluded {
			snapshot, ok := runtimeSnapshots[excluded[index].ChannelID]
			if !ok {
				excluded[index].Balance = ScoreChannel(
					ChannelScoreInputs{},
					excluded[index].Route.CostRatio,
					excluded[index].Route.Priority,
					selectedConfig,
				)
				continue
			}
			score := ScoreChannel(
				channelScoreInputsFromRuntimeSnapshot(snapshot, sampleWindows[excluded[index].ChannelID]),
				excluded[index].Route.CostRatio,
				excluded[index].Route.Priority,
				selectedConfig,
			)
			excluded[index].Balance = enrichBalanceScore(score, excluded[index].Route, snapshot, runtimeResult)
		}
	}

	plan := CandidatePlan{
		Candidates:      make([]Candidate, 0, len(ordered)),
		AllCapacityZero: allZero,
		Excluded:        excluded,
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
			RouteIndex:    routeIndexes[candidate.Channel.ID],
			Route:         candidate,
			InputEstimate: inputTokens,
			Balance:       scores[candidate.Channel.ID],
		})
		if inputTokens > plan.ConservativeInputTokens {
			plan.ConservativeInputTokens = inputTokens
		}
	}

	if len(plan.Candidates) == 0 {
		if unavailableErr == nil {
			unavailableErr = noAvailableCandidateError()
		}
		return plan, unavailableErr
	}

	// 基准顺序必须在 Sticky 置顶之前冻结：trace 要能同时展示「评分本来的顺序」
	// 和「Sticky 把谁提到了前面」（§13.3）。
	plan.BaselineOrder = make([]int64, 0, len(plan.Candidates))
	for _, candidate := range plan.Candidates {
		plan.BaselineOrder = append(plan.BaselineOrder, candidate.Route.Channel.ID)
	}

	// 会话粘性置顶：sticky 绑定渠道移到 fallback 首位，绝对优先于评分排序。
	// 渠道未进入可尝试候选时置顶落空（StickyPinned=false），调用方结合 Excluded 决定保留或清除绑定；
	// 其余候选顺序不受影响。
	if params.StickyChannelID != 0 {
		plan.Candidates, plan.StickyPinned, plan.StickyPinnedNonPreferred = pinStickyCandidate(plan.Candidates, params.StickyChannelID)
	}

	return plan, nil
}

// stickyTemporaryBypassReason 判断绑定渠道是否只因临时运行态被候选准备阶段排除。
// 当前只有 Channel 级 429 cooldown 属于这种情况；它不代表绑定失效。
func (p CandidatePlan) stickyTemporaryBypassReason(channelID int64) (string, bool) {
	for _, excluded := range p.Excluded {
		if excluded.ChannelID != channelID {
			continue
		}
		if excluded.Reason == string(breakerstore.CandidateSnapshotRateLimited) {
			return string(breakerstore.ReasonCooldown), true
		}
		return "", false
	}
	return "", false
}

func noAvailableCandidateError() error {
	return failure.Wrap(
		failure.CodeRoutingNoAvailableChannel,
		routing.ErrNoAvailableChannel,
		failure.WithMessage(routing.ErrNoAvailableChannel.Error()),
	)
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

// channelScoreInputsFromRuntimeSnapshot 组合 Redis 并发快照与 30 分钟样本聚合（§7/§12）。
func channelScoreInputsFromRuntimeSnapshot(
	snapshot breakerstore.CandidateSnapshot,
	window breakerstore.ChannelSampleWindow,
) ChannelScoreInputs {
	return ChannelScoreInputs{
		Concurrency:       CapacitySignal{Used: snapshot.Concurrency.Used, Limit: snapshot.Concurrency.Limit, Known: true},
		TTFTSumMs:         window.TTFTSumMs,
		TTFTCount:         window.TTFTCount,
		ErrorAttemptCount: window.ErrorAttemptCount,
		ErrorCount:        window.ErrorCount,
		HalfOpen:          snapshot.Status == breakerstore.CandidateSnapshotHalfOpen,
		RuntimeKnown:      true,
	}
}

func enrichBalanceScore(
	score BalanceScore,
	candidate routing.ChatRouteCandidate,
	snapshot breakerstore.CandidateSnapshot,
	result breakerstore.SnapshotManyResult,
) BalanceScore {
	channel := snapshot.Channel
	if snapshot.Status == breakerstore.CandidateSnapshotNoSample {
		channel = breakerstore.ScopeSnapshot{}
	}
	score.ProviderID = candidate.ProviderID
	score.CandidateOriginRevision = candidate.OriginRevision
	score.RuntimeOriginRevision = snapshot.Provider.OriginRevision
	score.OriginRevisionCurrent = snapshot.Provider.OriginRevision == candidate.OriginRevision
	score.CandidateProviderStatusRevision = candidate.ProviderStatusRevision
	score.RuntimeProviderStatusRevision = snapshot.Provider.StatusRevision
	score.ProviderStatusRevisionCurrent = snapshot.Provider.StatusRevision == candidate.ProviderStatusRevision
	score.CandidateChannelConfigRevision = candidate.ChannelConfigRevision
	score.RuntimeChannelConfigRevision = positiveRevisionPtr(channel.ChannelConfigRevision)
	score.ChannelConfigRevisionCurrent = channel.ChannelConfigRevision == candidate.ChannelConfigRevision
	score.CandidateChannelCapacityRevision = candidate.ChannelCapacityRevision
	score.RuntimeChannelCapacityRevision = snapshot.Candidate.ChannelCapacityRevision
	score.ChannelCapacityRevisionCurrent = snapshot.Candidate.ChannelCapacityRevision == candidate.ChannelCapacityRevision
	score.RouteRateLimitsRevision = result.RouteRateRevision
	score.GlobalConcurrencyRevision = result.GlobalConcurrencyRevision
	score.CircuitBreakerRevision = result.CircuitBreakerRevision
	score.ProviderBreakerState = traceBreakerState(snapshot.Provider)
	score.ChannelBreakerState = traceBreakerState(channel)
	score.CooldownRemainingMs = snapshot.CooldownRemainingMs
	score.ModelPermissionPaused = snapshot.ModelPermissionPaused
	score.ModelPermissionRecheckState = snapshot.ModelPermissionRecheckState
	score.RuntimeControlState = "active"
	score.RuntimeRevisionCurrent = true
	score.BreakerStoreAdmission = "normal"
	return score
}

func positiveRevisionPtr(revision int64) *int64 {
	if revision <= 0 {
		return nil
	}
	value := revision
	return &value
}

func traceBreakerState(snapshot breakerstore.ScopeSnapshot) string {
	if !snapshot.Exists {
		return ""
	}
	if snapshot.State == breakerstore.StateOpen && snapshot.OpenRemainingMs <= 0 {
		return string(breakerstore.StateHalfOpen)
	}
	return string(snapshot.State)
}
