package lifecycle

import (
	"context"
	"errors"
	"sort"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-api/internal/core/billing"
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

	// Mode 是线路策略（cheapest/stable/fixed，阶段 15）；空串保持 SQL routing 的 priority 基序。
	// 排序叠加在能力过滤/熔断可用性之前，故最终 fallback 顺序即策略顺序。
	Mode string

	// ChannelHealthScore 给 stable 排序提供渠道健康分（越小越健康）；nil 时 stable 退化为 priority 序。
	ChannelHealthScore func(channelKey string) float64
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

// CandidateSalePrices 提取候选池各命中渠道的当前售价，供保守预授权上界估算（阶段 15）。
func (p CandidatePlan) CandidateSalePrices() []billing.CustomerPriceSnapshot {
	prices := make([]billing.CustomerPriceSnapshot, 0, len(p.Candidates))
	for _, c := range p.Candidates {
		prices = append(prices, c.Route.SalePrice)
	}
	return prices
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

	// 先按线路策略排序，再做能力过滤 / 熔断可用性 / 估算；过滤保持顺序，故策略顺序即最终 fallback 顺序。
	ordered := sortCandidatesByMode(params.Candidates, params.Mode, params.ChannelHealthScore)

	filtered := e.registry.FilterCandidates(params.Protocol, ordered, params.Capabilities...)
	routeIndexes := candidateRouteIndexes(ordered)

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

// sortCandidatesByMode 按线路策略对候选稳定排序（返回新切片，不改入参）。
//   - cheapest：按代表售价升序（output_price 为主键，uncached_input_price 次之）。
//   - stable：按渠道健康分升序（越小越健康）；health 为 nil 时保持 priority 基序。
//   - fixed/其它：保持 SQL routing 的 priority 基序（fixed 池本就只有一条候选）。
//
// 稳定排序保证同键候选保留 SQL 的 priority 顺序，故平手回落 priority。
func sortCandidatesByMode(in []routing.ChatRouteCandidate, mode string, health func(channelKey string) float64) []routing.ChatRouteCandidate {
	out := make([]routing.ChatRouteCandidate, len(in))
	copy(out, in)

	switch mode {
	case "cheapest":
		sort.SliceStable(out, func(i, j int) bool {
			return saleSnapshotLess(out[i].SalePrice, out[j].SalePrice)
		})
	case "stable":
		if health != nil {
			sort.SliceStable(out, func(i, j int) bool {
				return health(MetricsID(out[i].Channel.ID)) < health(MetricsID(out[j].Channel.ID))
			})
		}
	}

	return out
}

// saleSnapshotLess 定义 cheapest 排序的代表价口径：output_price 优先，uncached_input_price 次之。
func saleSnapshotLess(a, b billing.CustomerPriceSnapshot) bool {
	if c := compareNumeric(a.OutputPrice, b.OutputPrice); c != 0 {
		return c < 0
	}
	return compareNumeric(a.UncachedInputPrice, b.UncachedInputPrice) < 0
}

// compareNumeric 比较两个 NUMERIC：返回 -1/0/1；无效值视为更大（排到末尾）。
func compareNumeric(a, b pgtype.Numeric) int {
	ra, oka := chatSettlementNumericRat(a)
	rb, okb := chatSettlementNumericRat(b)
	switch {
	case !oka && !okb:
		return 0
	case !oka:
		return 1
	case !okb:
		return -1
	default:
		return ra.Cmp(rb)
	}
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
