package lifecycle

import (
	"context"
	"errors"

	"github.com/ThankCat/unio-api/internal/core/routing"
	"github.com/ThankCat/unio-api/internal/platform/failure"
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

	// EstimateInputTokens 对每个可用 fallback candidate 做 provider-specific 保守估算。
	EstimateInputTokens CandidateInputTokenEstimator
}

// Candidate 是共享 lifecycle 已过滤并估算过的一个可尝试候选。
type Candidate struct {
	// RouteIndex 是候选在 SQL routing plan 中的原始位置，用于 attempt 审计保留过滤空洞。
	RouteIndex int

	// Route 是 routing 返回的 channel 运行时参数与 provider/model 事实。
	Route routing.ChatRouteCandidate
}

// CandidatePlan 是 authorization 与 attempt 共用的保守 fallback 计划。
type CandidatePlan struct {
	// Candidates 保持 SQL routing 的稳定顺序，只包含具备 operation capability 且当前可用的候选。
	Candidates []Candidate

	// ConservativeInputTokens 是所有可用 fallback candidates 输入估算的最大值。
	ConservativeInputTokens int64
}

// Executor 放置 OpenAI 与 Anthropic 共享的 gateway 生命周期执行能力。
//
// 当前先收口 authorization 前的候选准备段；后续 attempt、settlement 与 delivery 迁移继续
// 复用该类型，避免协议 service 各自实现不同的 fallback 风险边界。
type Executor struct {
	registry CandidateCapabilityRegistry
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

	filtered := e.registry.FilterCandidates(params.Protocol, params.Candidates, params.Capabilities...)
	routeIndexes := candidateRouteIndexes(params.Candidates)

	plan := CandidatePlan{
		Candidates: make([]Candidate, 0, len(filtered)),
	}
	for _, candidate := range filtered {
		if params.Available != nil && !params.Available(candidate) {
			continue
		}

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

	return plan, nil
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
