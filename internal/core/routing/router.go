package routing

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/core/billing"
	"github.com/ThankCat/unio-gateway/internal/core/channel"
	"github.com/ThankCat/unio-gateway/internal/core/fx"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/logging"
	"github.com/ThankCat/unio-gateway/internal/platform/observability/logfields"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

// defaultResponseTimeoutFallback / defaultFirstTokenTimeoutFallback 是 settings 尚未推送时的内置兜底。
// 它们只在装配阶段短暂生效；真实默认来自 gateway.default_response_timeout_ms /
// gateway.default_first_token_timeout_ms（§11.3）。
const (
	defaultResponseTimeoutFallback   = 200 * time.Second
	defaultFirstTokenTimeoutFallback = 60 * time.Second
)

const (
	// ProtocolOpenAI 是 OpenAI Chat Completions ingress 协议族标识。
	ProtocolOpenAI = "openai"
	// ProtocolAnthropic 是 Anthropic Messages ingress 协议族标识。
	ProtocolAnthropic = "anthropic"
)

const (
	// EndpointChatCompletions 是 OpenAI Chat Completions ingress 表面。
	EndpointChatCompletions = "chat_completions"
	// EndpointMessages 是 Anthropic Messages ingress 表面。
	EndpointMessages = "messages"
	// EndpointResponses 是 OpenAI Responses ingress 表面。
	EndpointResponses = "responses"
)

var (
	// ErrModelNotFound 表示请求的模型不存在或没有启用。
	ErrModelNotFound = errors.New("model not found")

	// ErrNoAvailableChannel 表示模型存在但当前没有可用渠道。
	ErrNoAvailableChannel = errors.New("no available channel")

	// ErrModelNotAvailable 表示模型存在但当前用户不允许使用。
	ErrModelNotAvailable = errors.New("model not available for user")

	// ErrModelProtocolUnsupported 表示模型配置过供给，但请求的入口协议族不在其
	// （未归档）供给协议集合内，属于客户端用错协议（如用 OpenAI 协议调只有
	// Anthropic 渠道的模型）。
	ErrModelProtocolUnsupported = errors.New("model does not support requested ingress protocol")

	// ErrChannelCredentialMissing 表示 channel 未配置上游凭据。
	ErrChannelCredentialMissing = errors.New("channel credential missing")

	// ErrIngressProtocolInvalid 表示 routing 请求没有携带受支持的 ingress 协议族。
	ErrIngressProtocolInvalid = errors.New("ingress protocol invalid")
)

// ChatRouteRequest 表示一次 routing 选择所需上下文。
type ChatRouteRequest struct {
	UserID  int64
	ModelID string

	// IngressProtocol 是客户请求的协议族（如 openai）；routing 只返回同协议 channel 候选。
	IngressProtocol string

	// Endpoint 是本次请求的 ingress 表面（chat_completions/messages/responses），供审计/日志维度。
	Endpoint string
}

// ChatRouteCandidate 表示一个可尝试的 chat 上游候选。
type ChatRouteCandidate struct {
	ModelDBID  int64
	ProviderID int64
	// Provider identity and revisions are immutable facts of this candidate.
	// Admission and audit code must not infer them later from mutable rows.
	OriginRevision          int64
	ProviderStatusRevision  int64
	ChannelConfigRevision   int64
	ChannelCapacityRevision int64
	AdapterKey              string
	Protocol                string
	SupportsOpenAIFast      bool
	Channel                 channel.Runtime
	UpstreamModel           string

	// MaxOutputTokens 是该候选逻辑模型 models.max_output_tokens（0 表示未配置）。
	// 客户未显式给出输出上限时，authorization 用它（取候选最大值）做保守冻结上界，
	// 避免按全局兜底偏小导致预冻结不足、超额进平台核销。
	MaxOutputTokens int64

	// ModelPriceID 是计算 SalePrice 所用的模型基准售价行 ID（model_prices.id，供结算审计/快照）。
	ModelPriceID int64
	// PriceRatio 是计算 SalePrice 所用的模型售价倍率（供结算审计/快照）；绝对售价路径下为空。
	// 模型配了绝对售价时为空：那条路径下售价直接取自模型，没有倍率参与。
	PriceRatio pgtype.Numeric
	// SalePrice 是客户最终售价向量 = 模型基准价 × 线路倍率（DEC-026）；同一请求所有候选共享同一售价，
	// 供保守预授权上界与结算扣费，不随命中哪条渠道变化。
	// 注意：此处为短上下文牌价；长上下文阶梯在授权/结算时按 LongContextPolicy + 输入合计再缩放。
	SalePrice billing.CustomerPriceSnapshot
	// FastModelPriceServiceTierID/FastSalePrice 是同一模型价格窗口下可选的 Fast 精确售价。
	// 缺失只表示无法按 Fast 计费，绝不影响候选资格或排序。
	FastModelPriceServiceTierID int64
	FastSalePrice               billing.CustomerPriceSnapshot

	// LongContextPolicy 来自计算 SalePrice 所用的 model_prices 窗口；启用时输入合计超过阈值则整单输入/输出单价按倍率放大。
	LongContextPolicy billing.LongContextPolicy

	// ChannelPriceID 是命中的 channel_prices 绝对成本覆盖行 ID（DEC-027：优先级最高，0 表示无覆盖、走倍率路径）。
	// 供结算 pin 取价，语义与旧版一致但收窄为「覆盖行」。
	ChannelPriceID int64
	// CostBaseModelPriceID/ChannelCostMultiplierID/ChannelRechargeFactorID 是倍率路径下算 ChannelCost 用到的
	// 来源行 id（DEC-031 pin）；透传到结算/恢复，按这些不可改行确定性重算成本，防改倍率漂移。
	// DEC-031：成本基数复用 model_prices，故 CostBaseModelPriceID == ModelPriceID（同一基准价行，售价成本共用）。
	// 覆盖路径下三者为 0；充值倍率未配置时 ChannelRechargeFactorID=0（结算按 1.0）。
	CostBaseModelPriceID    int64
	ChannelCostMultiplierID int64
	ChannelRechargeFactorID int64
	// ChannelCost 是命中渠道当前生效的上游真实成本快照（覆盖值 或 基准价×价格倍率×充值倍率）；毛利 = SalePrice − ChannelCost。
	ChannelCost billing.ProviderCostSnapshot
	// FastChannelPriceServiceTierID 是绝对成本覆盖路径的 Fast 子记录；倍率路径为 0，
	// Fast 成本基数由 FastModelPriceServiceTierID 与现有倍率 pin 确定。
	FastChannelPriceServiceTierID int64
	// CostRatio 是七个归一化计价分项中最大的 provider 成本/客户售价比率。
	// routing 在负毛利校验后冻结该值，balanced 调度只消费该不可变事实。
	CostRatio float64
	// Priority 以 0 为最高优先级，参与客观评分且只允许 0,10,...,100。
	Priority int32
	// StickyEnabled 为渠道级覆盖：nil 继承全局，true 使用 StickyTTL，false 禁用。
	StickyEnabled *bool
	StickyTTL     *time.Duration

	// ConcurrencyLimit 是该候选命中渠道的在途并发上限（DEC-029）：
	// nil=继承并发默认 channel_limit，0=显式不限，>0=具体上限。命中时该候选被跳过 fallback 到下一渠道。
	ConcurrencyLimit *int64
}

// ChatRoutePlan 表示一次 chat 请求的同模型候选计划。
type ChatRoutePlan struct {
	RequestedModel string
	Candidates     []ChatRouteCandidate
	PoolSize       int

	// ModelDBID 是 RequestedModel 解析出的模型主键。Sticky key 需要它以避免同一会话
	// 跨模型共享绑定（§10.1）；候选行天然共享同一个模型，所以这是计划级事实。
	ModelDBID int64
}

// Store 定义 routing 查询候选渠道所需的最小数据库能力。
type Store interface {
	ModelIngressQualification(ctx context.Context, arg sqlc.ModelIngressQualificationParams) (sqlc.ModelIngressQualificationRow, error)
	FindModelCandidates(ctx context.Context, arg sqlc.FindModelCandidatesParams) ([]sqlc.FindModelCandidatesRow, error)
}

// ValidateChat 在持久 request 生命周期开始前校验模型产品资格：协议合法、模型存在，
// 且请求协议属于该模型的产品面。「模型配置过供给、但请求协议不在其（未归档）供给协议
// 集合内」属于客户端用错协议（如用 OpenAI 协议调只有 Anthropic 渠道的模型）：在这里
// 拒绝就不会产生请求记录，只留 Warn 日志与拒绝指标。判定只看配置存在性，不看绑定/渠道
// 启停等运行时供给——暂停供给或解绑到零供给仍保持 503 落库口径（ADR-0020），
// 运行时能否打通仍由 PlanChat 的候选筛选负责。
func (r *Router) ValidateChat(ctx context.Context, req ChatRouteRequest) error {
	if !IsSupportedProtocol(req.IngressProtocol) {
		return failure.Wrap(
			failure.CodeRoutingProtocolInvalid,
			ErrIngressProtocolInvalid,
			failure.WithMessage(ErrIngressProtocolInvalid.Error()),
			failure.WithField("ingress_protocol", req.IngressProtocol),
		)
	}

	qual, err := r.store.ModelIngressQualification(ctx, sqlc.ModelIngressQualificationParams{
		RequestedModelID: req.ModelID,
		IngressProtocol:  req.IngressProtocol,
	})
	if err != nil {
		return failure.Wrap(
			failure.CodeRoutingStoreFailed,
			err,
			failure.WithMessage("check model ingress qualification"),
		)
	}
	if !qual.ModelExists {
		return failure.Wrap(
			failure.CodeRoutingModelNotFound,
			ErrModelNotFound,
			failure.WithMessage(ErrModelNotFound.Error()),
		)
	}
	if !qual.ProtocolSupported {
		fields := []zap.Field{
			zap.String("requested_model", req.ModelID),
			zap.String("ingress_protocol", req.IngressProtocol),
			zap.String("endpoint", req.Endpoint),
		}
		if requestFields, ok := logfields.FromContext(ctx); ok {
			fields = append(requestFields.ZapFields(), fields...)
		}
		logging.Warn(r.logger, "routing", "qualification",
			"model requested via unsupported ingress protocol; client is likely calling the wrong API", fields...)
		return failure.Wrap(
			failure.CodeRoutingModelProtocolUnsupported,
			ErrModelProtocolUnsupported,
			failure.WithMessage(ErrModelProtocolUnsupported.Error()),
			failure.WithField("ingress_protocol", req.IngressProtocol),
		)
	}

	return nil
}

// Router 负责根据 requested model 与入口协议选择可用 channel。
//
// 两个全局默认超时可运行时热改，用 atomic 存储（纳秒）：路由热路径每次候选构造都会读取，无锁竞争。
type Router struct {
	store                         Store
	defaultResponseTimeoutNanos   atomic.Int64
	defaultFirstTokenTimeoutNanos atomic.Int64
	logger                        *zap.Logger
	fxRates                       FxRateSource
}

// FxRateSource 提供成本币种 → USD 的最新汇率（由 core/fx.Service 实现）。
type FxRateSource interface {
	LatestRate(ctx context.Context, quote string) (fx.Rate, error)
}

// Option 调整 Router 的可选依赖（如日志）。
type Option func(*Router)

// WithLogger 注入结构化日志器，用于记录被跳过的坏候选（P1-1）。
func WithLogger(logger *zap.Logger) Option {
	return func(r *Router) {
		if logger != nil {
			r.logger = logger
		}
	}
}

// WithFxRates 注入汇率读取器（多货币候选比价用，D5/D11）。
// 未注入时跨币种候选一律被剔除（等价于缺汇率），同币种候选不受影响。
func WithFxRates(rates FxRateSource) Option {
	return func(r *Router) {
		r.fxRates = rates
	}
}

// NewRouter 创建 routing router。
func NewRouter(store Store, defaultResponseTimeout time.Duration, opts ...Option) *Router {
	r := &Router{
		store:  store,
		logger: zap.NewNop(),
	}
	r.SetDefaultResponseTimeout(defaultResponseTimeout)
	r.SetDefaultFirstTokenTimeout(defaultFirstTokenTimeoutFallback)
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// SetDefaultResponseTimeout 原子替换全局默认响应超时（运行时热改入口）；<=0 兜底为内置默认。
// 仅影响之后的候选构造；渠道行上的正数 response_timeout_ms 始终优先。
func (r *Router) SetDefaultResponseTimeout(d time.Duration) {
	if d <= 0 {
		d = defaultResponseTimeoutFallback
	}
	r.defaultResponseTimeoutNanos.Store(int64(d))
}

// SetDefaultFirstTokenTimeout 原子替换全局默认首字超时；<=0 兜底为内置默认。
func (r *Router) SetDefaultFirstTokenTimeout(d time.Duration) {
	if d <= 0 {
		d = defaultFirstTokenTimeoutFallback
	}
	r.defaultFirstTokenTimeoutNanos.Store(int64(d))
}

func (r *Router) defaultResponseTimeout() time.Duration {
	return time.Duration(r.defaultResponseTimeoutNanos.Load())
}

func (r *Router) defaultFirstTokenTimeout() time.Duration {
	return time.Duration(r.defaultFirstTokenTimeoutNanos.Load())
}

// PlanChat 为 chat completion 请求生成有序候选计划。
func (r *Router) PlanChat(ctx context.Context, req ChatRouteRequest) (ChatRoutePlan, error) {
	if !IsSupportedProtocol(req.IngressProtocol) {
		return ChatRoutePlan{}, failure.Wrap(
			failure.CodeRoutingProtocolInvalid,
			ErrIngressProtocolInvalid,
			failure.WithMessage(ErrIngressProtocolInvalid.Error()),
			failure.WithField("ingress_protocol", req.IngressProtocol),
		)
	}

	rows, err := r.findCandidateRows(ctx, req)
	if err != nil {
		return ChatRoutePlan{}, err
	}

	candidates := make([]ChatRouteCandidate, 0, len(rows))
	marginFiltered := false
	for _, row := range rows {
		candidate, err := r.buildChatRouteCandidate(ctx, row)
		if err != nil {
			if failure.CodeOf(err) == failure.CodeRoutingNegativeMargin {
				marginFiltered = true
			}
			// P1-1：单个候选凭据缺失/解密失败时跳过该候选并记日志，不让整批 plan 失败；
			// 只有当全部候选都不可用时才在循环后报 no_available_channel，最大化可用性。
			fields := append([]zap.Field{
				zap.Int64("channel_id", row.ChannelID),
				zap.String("provider_slug", row.ProviderSlug),
				zap.String("adapter_key", row.AdapterKey),
				zap.String("upstream_model", row.UpstreamModel),
			}, failure.LogFields(err)...)
			if requestFields, ok := logfields.FromContext(ctx); ok {
				fields = append(requestFields.ZapFields(), fields...)
			}
			logging.Warn(r.logger, "routing", "candidate", "routing candidate excluded", fields...)
			continue
		}
		candidates = append(candidates, candidate)
	}

	// rows 非空（findCandidateRows 已区分 model 不存在/不可用/无渠道），若到此处候选全被跳过，
	// 说明命中渠道的凭据全部不可用：报 no_available_channel 而非泄露底层凭据错误。
	if len(candidates) == 0 {
		options := []failure.Option{failure.WithMessage("all matched channels are unusable")}
		if marginFiltered {
			options = append(options, failure.WithField("margin_guard_triggered", true))
		}
		return ChatRoutePlan{}, failure.Wrap(
			failure.CodeRoutingNoAvailableChannel,
			ErrNoAvailableChannel,
			options...,
		)
	}

	plan := ChatRoutePlan{
		RequestedModel: req.ModelID,
		Candidates:     candidates,
		PoolSize:       len(candidates),
		ModelDBID:      candidates[0].ModelDBID,
	}

	return plan, nil
}

// IsSupportedProtocol 判断 routing 是否支持指定 ingress 协议族。
func IsSupportedProtocol(protocol string) bool {
	switch protocol {
	case ProtocolOpenAI, ProtocolAnthropic:
		return true
	default:
		return false
	}
}

func (r *Router) findCandidateRows(ctx context.Context, req ChatRouteRequest) ([]sqlc.FindModelCandidatesRow, error) {
	rows, err := r.store.FindModelCandidates(ctx, sqlc.FindModelCandidatesParams{
		RequestedModelID: req.ModelID,
		IngressProtocol:  req.IngressProtocol,
		AtTime:           pgtype.Timestamptz{Time: time.Now(), Valid: true},
	})
	if err != nil {
		return nil, failure.Wrap(
			failure.CodeRoutingStoreFailed,
			err,
			failure.WithMessage("find model candidates"),
		)
	}

	// 模型产品资格已在 request_records 创建前由 ValidateChat 判定。此处只表达运行时供给：
	// 即使模型或渠道绑定随后并发变化，已接受的请求也按无可用 Channel 收口并保留审计。
	if len(rows) > 0 {
		return rows, nil
	}
	return nil, failure.Wrap(
		failure.CodeRoutingNoAvailableChannel,
		ErrNoAvailableChannel,
		failure.WithMessage(ErrNoAvailableChannel.Error()),
	)
}

// int4LimitPtr 把可空渠道限流上限转成 *int64（nil=继承渠道默认限流，0=不限，>0=上限）。
func int4LimitPtr(v pgtype.Int4) *int64 {
	if !v.Valid {
		return nil
	}
	out := int64(v.Int32)
	return &out
}

func optionalBool(v pgtype.Bool) *bool {
	if !v.Valid {
		return nil
	}
	out := v.Bool
	return &out
}

func optionalDurationMs(v pgtype.Int8) *time.Duration {
	if !v.Valid {
		return nil
	}
	out := time.Duration(v.Int64) * time.Millisecond
	return &out
}

func (r *Router) buildChatRouteCandidate(ctx context.Context, row sqlc.FindModelCandidatesRow) (ChatRouteCandidate, error) {
	// 渠道凭据明文存储（产品决策）：直接取用，仅防御性校验非空（DB 已 NOT NULL + CHECK <> ''）。
	apiKey := strings.TrimSpace(row.Credential)
	if apiKey == "" {
		return ChatRouteCandidate{}, failure.Wrap(
			failure.CodeRoutingCredentialResolveFailed,
			ErrChannelCredentialMissing,
			failure.WithMessage(ErrChannelCredentialMissing.Error()),
		)
	}

	// 客户售价定在模型上，两套独立实体：绝对售价整组覆盖，否则基准价 × 该模型自己的倍率。
	// 两者都随价格行走、可以共存，但不能混算；都空则 ResolveCustomerPrice 失败，本候选被排除。
	// 售价只取决于命中的价格行，与命中哪条渠道无关，同一请求的所有候选共享同一售价。
	basePrice := billing.CustomerPriceSnapshot{
		Currency:                   row.BaseCurrency,
		PricingUnit:                row.BasePricingUnit,
		UncachedInputPrice:         row.UncachedInputPrice,
		CacheReadInputPrice:        row.CacheReadInputPrice,
		CacheCreation5mInputPrice:  row.CacheCreation5mInputPrice,
		CacheCreation1hInputPrice:  row.CacheCreation1hInputPrice,
		CacheCreation30mInputPrice: row.CacheCreation30mInputPrice,
		OutputPrice:                row.OutputPrice,
		ReasoningOutputPrice:       row.ReasoningOutputPrice,
		FormulaVersion:             billing.FormulaVersionV1,
	}
	ratio := row.SalePriceRatio
	saleOverride := billing.SaleOverride{
		UncachedInputPrice:         row.SaleUncachedInputPrice,
		CacheReadInputPrice:        row.SaleCacheReadInputPrice,
		CacheCreation5mInputPrice:  row.SaleCacheCreation5mInputPrice,
		CacheCreation1hInputPrice:  row.SaleCacheCreation1hInputPrice,
		CacheCreation30mInputPrice: row.SaleCacheCreation30mInputPrice,
		OutputPrice:                row.SaleOutputPrice,
		ReasoningOutputPrice:       row.SaleReasoningOutputPrice,
	}
	salePrice, err := billing.ResolveCustomerPrice(basePrice, ratio, saleOverride)
	if err != nil {
		return ChatRouteCandidate{}, failure.Wrap(
			failure.CodeBillingInvalidPrice,
			err,
			failure.WithMessage("resolve model sale price"),
		)
	}
	// 落快照的倍率只在倍率路径下有意义：走绝对售价时留空，表示这个售价不是算出来的，
	// 避免审计端拿一个没参与计算的倍率去反推基准价。
	var appliedRatio pgtype.Numeric
	if !saleOverride.Configured() {
		appliedRatio = ratio
	}
	fastModelPriceServiceTierID := int64(0)
	fastSalePrice := billing.CustomerPriceSnapshot{}
	fastBasePrice := billing.CustomerPriceSnapshot{}
	if row.FastModelPriceServiceTierID > 0 {
		fastBasePrice = billing.CustomerPriceSnapshot{
			Currency:                   row.BaseCurrency,
			PricingUnit:                row.BasePricingUnit,
			UncachedInputPrice:         row.FastUncachedInputPrice,
			CacheReadInputPrice:        row.FastCacheReadInputPrice,
			CacheCreation5mInputPrice:  row.FastCacheCreation5mInputPrice,
			CacheCreation1hInputPrice:  row.FastCacheCreation1hInputPrice,
			CacheCreation30mInputPrice: row.FastCacheCreation30mInputPrice,
			OutputPrice:                row.FastOutputPrice,
			ReasoningOutputPrice:       row.FastReasoningOutputPrice,
			FormulaVersion:             billing.FormulaVersionV1,
		}
		// Fast 与 Standard 必须走同一套售价实体：绝对售价整组覆盖时两边都用绝对售价，
		// 否则两边都用各自基准价 × 同一倍率。缺 Fast 绝对售价时忽略 Fast，绝不回落到倍率。
		fastOverride := billing.SaleOverride{
			UncachedInputPrice:         row.FastSaleUncachedInputPrice,
			CacheReadInputPrice:        row.FastSaleCacheReadInputPrice,
			CacheCreation5mInputPrice:  row.FastSaleCacheCreation5mInputPrice,
			CacheCreation1hInputPrice:  row.FastSaleCacheCreation1hInputPrice,
			CacheCreation30mInputPrice: row.FastSaleCacheCreation30mInputPrice,
			OutputPrice:                row.FastSaleOutputPrice,
			ReasoningOutputPrice:       row.FastSaleReasoningOutputPrice,
		}
		if saleOverride.Configured() && !fastOverride.Configured() {
			logging.Warn(r.logger, "routing", "candidate", "fast sale price ignored because absolute override requires fast absolute",
				zap.Int64("channel_id", row.ChannelID),
				zap.Int64("model_price_service_tier_id", row.FastModelPriceServiceTierID),
			)
		} else {
			override := billing.SaleOverride{}
			if saleOverride.Configured() {
				override = fastOverride
			}
			fastResolved, fastErr := billing.ResolveCustomerPrice(fastBasePrice, ratio, override)
			if fastErr == nil {
				fastModelPriceServiceTierID = row.FastModelPriceServiceTierID
				fastSalePrice = fastResolved
			} else {
				logging.Warn(r.logger, "routing", "candidate", "fast sale price ignored because it is invalid",
					zap.Int64("channel_id", row.ChannelID),
					zap.Int64("model_price_service_tier_id", row.FastModelPriceServiceTierID),
					zap.Error(fastErr),
				)
			}
		}
	}

	// 超时只读新列（§11.3/§11.5）：NULL 继承全局默认，正数覆盖；0/负数不表示「无限」，
	// 因此非正值一律按继承处理，绝不关闭保护。
	responseTimeout := r.defaultResponseTimeout()
	if row.ResponseTimeoutMs.Valid && row.ResponseTimeoutMs.Int32 > 0 {
		responseTimeout = time.Duration(row.ResponseTimeoutMs.Int32) * time.Millisecond
	}
	firstTokenTimeout := r.defaultFirstTokenTimeout()
	if row.FirstTokenTimeoutMs.Valid && row.FirstTokenTimeoutMs.Int32 > 0 {
		firstTokenTimeout = time.Duration(row.FirstTokenTimeoutMs.Int32) * time.Millisecond
	}

	maxOutputTokens := int64(0)
	if row.ModelMaxOutputTokens.Valid {
		maxOutputTokens = row.ModelMaxOutputTokens.Int64
	}

	// 渠道真实成本（DEC-031）：绝对覆盖优先（channel_prices）；否则基准价（model_prices）× 价格倍率 × 充值倍率。
	// 已定价过滤已保证「有覆盖 OR 有价格倍率」，且 base 基准价 INNER JOIN 保证存在，此处不会无成本可解析。
	// 成本基数复用上面已构造的 basePrice（= model_prices 向量），倍率路径 pin = row.ModelPriceID。
	channelCost, costBaseModelPriceID, channelCostMultiplierID, channelRechargeFactorID, err := resolveCandidateCost(row, basePrice)
	if err != nil {
		return ChatRouteCandidate{}, err
	}
	fastChannelPriceServiceTierID := int64(0)
	if fastModelPriceServiceTierID > 0 || row.FastChannelPriceServiceTierID > 0 {
		if _, fastCostID, ok := resolveFastCandidateCost(row, fastBasePrice); ok {
			fastChannelPriceServiceTierID = fastCostID
		}
	}
	// 跨币种候选（绝对成本路径 + provider 币种 ≠ 售价币种）解析一次最新汇率（D5）；
	// 缺汇率 = 候选剔除（D11：宁可少赚一笔，不算错一分），走与负毛利同一条拒绝路径可观测。
	fxRate, err := r.resolveCandidateFxRate(ctx, salePrice.Currency, channelCost.Currency)
	if err != nil {
		return ChatRouteCandidate{}, failure.Wrap(
			failure.CodeRoutingNegativeMargin,
			err,
			failure.WithMessage("resolve fx rate for cross-currency candidate"),
			failure.WithField("channel_id", row.ChannelID),
			failure.WithField("model_id", row.RequestedModelID),
			failure.WithField("cost_currency", channelCost.Currency),
		)
	}
	violations, err := billing.ValidateNonNegativeMarginFX(salePrice, channelCost, fxRate)
	if err != nil || len(violations) > 0 {
		fields := []failure.Option{
			failure.WithMessage("candidate rejected by negative margin guard"),
			failure.WithField("channel_id", row.ChannelID),
			failure.WithField("model_id", row.RequestedModelID),
		}
		if len(violations) > 0 {
			fields = append(fields, failure.WithField("component", violations[0].Component))
		}
		if err != nil {
			return ChatRouteCandidate{}, failure.Wrap(failure.CodeRoutingNegativeMargin, err, fields...)
		}
		return ChatRouteCandidate{}, failure.New(failure.CodeRoutingNegativeMargin, fields...)
	}
	costRatio, err := billing.ProviderCostToSaleRatioFX(salePrice, channelCost, fxRate)
	if err != nil {
		return ChatRouteCandidate{}, failure.Wrap(
			failure.CodeRoutingNegativeMargin,
			err,
			failure.WithMessage("calculate candidate provider cost to sale ratio"),
			failure.WithField("channel_id", row.ChannelID),
			failure.WithField("model_id", row.RequestedModelID),
		)
	}

	return ChatRouteCandidate{
		ModelDBID:               row.ModelDbID,
		ProviderID:              row.ProviderID,
		OriginRevision:          row.ProviderOriginRevision,
		ProviderStatusRevision:  row.ProviderStatusRevision,
		ChannelConfigRevision:   row.ChannelConfigRevision,
		ChannelCapacityRevision: row.ChannelCapacityRevision,
		AdapterKey:              row.AdapterKey,
		Protocol:                row.Protocol,
		SupportsOpenAIFast:      row.SupportsOpenaiFast,
		MaxOutputTokens:         maxOutputTokens,
		ConcurrencyLimit:        int4LimitPtr(row.ChannelConcurrencyLimit),
		Priority:                row.Priority,
		StickyEnabled:           optionalBool(row.ChannelStickyEnabled),
		StickyTTL:               optionalDurationMs(row.ChannelStickyTtlMs),
		Channel: channel.Runtime{
			ID:                row.ChannelID,
			Name:              row.ChannelName,
			Origin:            row.Origin,
			APIKey:            apiKey,
			ResponseTimeout:   responseTimeout,
			FirstTokenTimeout: firstTokenTimeout,
			ProviderSlug:      row.ProviderSlug,
		},
		UpstreamModel:                 row.UpstreamModel,
		ModelPriceID:                  row.ModelPriceID,
		PriceRatio:                    appliedRatio,
		SalePrice:                     salePrice,
		FastModelPriceServiceTierID:   fastModelPriceServiceTierID,
		FastSalePrice:                 fastSalePrice,
		LongContextPolicy:             longContextPolicyFromCandidateRow(row),
		ChannelPriceID:                row.ChannelPriceID,
		CostBaseModelPriceID:          costBaseModelPriceID,
		ChannelCostMultiplierID:       channelCostMultiplierID,
		ChannelRechargeFactorID:       channelRechargeFactorID,
		ChannelCost:                   channelCost,
		FastChannelPriceServiceTierID: fastChannelPriceServiceTierID,
		CostRatio:                     costRatio,
	}, nil
}

// resolveCandidateFxRate 为跨币种候选解析最新汇率（1 售价币种兑多少成本币种）。
// 同币种返回 nil（不换汇）；跨币种但未注入汇率源或查不到汇率时报错（候选被剔除）。
// 进程内 fx.Service 自带短 TTL 缓存，单请求多候选的重复解析不放大查库。
func (r *Router) resolveCandidateFxRate(ctx context.Context, saleCurrency, costCurrency string) (*big.Rat, error) {
	if costCurrency == "" || costCurrency == saleCurrency {
		return nil, nil
	}
	if r.fxRates == nil {
		return nil, billing.ErrMissingFxRate
	}
	rate, err := r.fxRates.LatestRate(ctx, costCurrency)
	if err != nil {
		return nil, err
	}
	return rate.Value, nil
}

// resolveFastCandidateCost 只验证候选是否具备可锁定的 Fast Provider 成本来源。
// 返回结果不参与路由过滤、排序或负毛利守卫。
func resolveFastCandidateCost(row sqlc.FindModelCandidatesRow, fastBasePrice billing.CustomerPriceSnapshot) (billing.ProviderCostSnapshot, int64, bool) {
	if row.ChannelPriceID != 0 {
		if row.FastChannelPriceServiceTierID == 0 {
			return billing.ProviderCostSnapshot{}, 0, false
		}
		return billing.ProviderCostSnapshot{
			Currency:                  row.CostCurrency,
			PricingUnit:               row.CostPricingUnit,
			UncachedInputCost:         row.FastUncachedInputCost,
			CacheReadInputCost:        row.FastCacheReadInputCost,
			CacheCreation5mInputCost:  row.FastCacheCreation5mInputCost,
			CacheCreation1hInputCost:  row.FastCacheCreation1hInputCost,
			CacheCreation30mInputCost: row.FastCacheCreation30mInputCost,
			OutputCost:                row.FastOutputCost,
			ReasoningOutputCost:       row.FastReasoningOutputCost,
			FormulaVersion:            billing.FormulaVersionV1,
		}, row.FastChannelPriceServiceTierID, true
	}

	if row.FastModelPriceServiceTierID == 0 {
		return billing.ProviderCostSnapshot{}, 0, false
	}
	reference := billing.ModelPriceToProviderCost(fastBasePrice)
	scaled, err := billing.ScaleProviderCostByFactors(reference, row.CostMultiplier, rechargeFactorOrDefault(row.RechargeFactor))
	if err != nil {
		return billing.ProviderCostSnapshot{}, 0, false
	}
	// 倍率路径成本按 provider 结算币种记账（D2 修订），与 resolveCandidateCost 同口径。
	if row.ProviderCurrency != "" {
		scaled.Currency = row.ProviderCurrency
	}
	return scaled, 0, true
}

// resolveCandidateCost 从候选行解析渠道真实成本与来源 pin（DEC-027 倍率 + DEC-031 单基数）。
//   - 绝对覆盖（row.ChannelPriceID != 0）：直接用 channel_prices 成本列，来源 id 归零。
//   - 倍率路径：成本基数 = 模型基准价（base，DEC-031 复用 model_prices，由 basePrice 映射为成本向量），
//     真实成本 = 基数 × 价格倍率 × 充值倍率（充值缺省 1.0）；带回成本基数（model_price）/价格倍率/充值倍率行 id 作 pin。
//
// basePrice 是调用方已从 base(model_prices) 列构造的售价向量（与 SalePrice 同源），此处映射为成本向量作基数，
// 保证售价与成本共用同一 model_prices 基数（DEC-031 核心不变量）。
func resolveCandidateCost(row sqlc.FindModelCandidatesRow, basePrice billing.CustomerPriceSnapshot) (cost billing.ProviderCostSnapshot, costBaseModelPriceID, multiplierID, rechargeFactorID int64, err error) {
	if row.ChannelPriceID != 0 {
		return billing.ProviderCostSnapshot{
			Currency:                  row.CostCurrency,
			PricingUnit:               row.CostPricingUnit,
			UncachedInputCost:         row.UncachedInputCost,
			CacheReadInputCost:        row.CacheReadInputCost,
			CacheCreation5mInputCost:  row.CacheCreation5mInputCost,
			CacheCreation1hInputCost:  row.CacheCreation1hInputCost,
			CacheCreation30mInputCost: row.CacheCreation30mInputCost,
			OutputCost:                row.OutputCost,
			ReasoningOutputCost:       row.ReasoningOutputCost,
			FormulaVersion:            billing.FormulaVersionV1,
		}, 0, 0, 0, nil
	}

	// DEC-031：成本基数 = 模型基准价（映射为成本向量），不再走独立参考成本表。
	reference := billing.ModelPriceToProviderCost(basePrice)
	scaled, err := billing.ScaleProviderCostByFactors(reference, row.CostMultiplier, rechargeFactorOrDefault(row.RechargeFactor))
	if err != nil {
		return billing.ProviderCostSnapshot{}, 0, 0, 0, failure.Wrap(
			failure.CodeBillingInvalidPrice,
			err,
			failure.WithMessage("scale provider cost by channel multiplier and recharge factor"),
		)
	}
	// 倍率路径成本按 provider 结算币种记账（D2 修订）：基准价数值 × 倍率 = 原币金额，
	// 跨币种时由 margin/比价按当日汇率折算。空值防御（旧测试 fixture 未填）回退基准价币种。
	if row.ProviderCurrency != "" {
		scaled.Currency = row.ProviderCurrency
	}
	return scaled, row.ModelPriceID, row.ChannelCostMultiplierID, row.ChannelRechargeFactorID, nil
}

// rechargeFactorOrDefault 充值倍率未配置（NULL）时按 1.0（名义即真实，向后兼容）。
func rechargeFactorOrDefault(factor pgtype.Numeric) pgtype.Numeric {
	if factor.Valid {
		return factor
	}
	return pgtype.Numeric{Int: big.NewInt(1), Exp: 0, Valid: true}
}

// longContextPolicyFromCandidateRow 从候选行的 model_prices 长上下文字段组装策略。
func longContextPolicyFromCandidateRow(row sqlc.FindModelCandidatesRow) billing.LongContextPolicy {
	threshold := int64(0)
	if row.BaseLongContextThreshold.Valid {
		threshold = row.BaseLongContextThreshold.Int64
	}
	return billing.LongContextPolicy{
		Enabled:          row.BaseLongContextEnabled,
		Threshold:        threshold,
		InputMultiplier:  row.BaseLongContextInputMultiplier,
		OutputMultiplier: row.BaseLongContextOutputMultiplier,
	}
}
