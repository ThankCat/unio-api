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
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

const defaultChannelTimeout = 30 * time.Second

const (
	// ProtocolOpenAI 是 OpenAI Chat Completions ingress 协议族标识。
	ProtocolOpenAI = "openai"
	// ProtocolAnthropic 是 Anthropic Messages ingress 协议族标识。
	ProtocolAnthropic = "anthropic"
)

const (
	// OperationChatCompletions 是 OpenAI Chat Completions ingress 表面。
	OperationChatCompletions = "chat_completions"
	// OperationMessages 是 Anthropic Messages ingress 表面。
	OperationMessages = "messages"
	// OperationResponses 是 OpenAI Responses ingress 表面。
	OperationResponses = "responses"
)

var (
	// ErrModelNotFound 表示请求的模型不存在或没有启用。
	ErrModelNotFound = errors.New("model not found")

	// ErrNoAvailableChannel 表示模型存在但当前没有可用渠道。
	ErrNoAvailableChannel = errors.New("no available channel")

	// ErrModelNotAvailable 表示模型存在但当前用户不允许使用。
	ErrModelNotAvailable = errors.New("model not available for user")

	// ErrRouteNotConfigured 表示 API Key 绑定的线路缺失或已停用（线路必填，无默认回落）。
	ErrRouteNotConfigured = errors.New("route not configured")

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

	// Operation 是本次请求的 ingress 表面（chat_completions/messages/responses），供审计/日志维度。
	Operation string

	// RouteID 是 API Key 绑定的线路 ID（线路必填，恒有值）；线路缺失或已停用则拒绝请求（无默认回落）。
	RouteID *int64
}

// ChatRouteCandidate 表示一个可尝试的 chat 上游候选。
type ChatRouteCandidate struct {
	ModelDBID     int64
	ProviderID    int64
	AdapterKey    string
	Protocol      string
	Channel       channel.Runtime
	UpstreamModel string

	// RouteName 是本次请求绑定线路的名称（routes.name），供 access log 的 router 字段使用。
	RouteName string

	// MaxOutputTokens 是该候选逻辑模型 models.max_output_tokens（0 表示未配置）。
	// 客户未显式给出输出上限时，authorization 用它（取候选最大值）做保守冻结上界，
	// 避免按全局兜底偏小导致预冻结不足、超额进平台核销。
	MaxOutputTokens int64

	// ModelPriceID 是计算 SalePrice 所用的模型基准售价行 ID（model_prices.id，供结算审计/快照）。
	ModelPriceID int64
	// PriceRatio 是计算 SalePrice 所用的线路价格倍率（routes.price_ratio，供结算审计/快照）。
	PriceRatio pgtype.Numeric
	// SalePrice 是客户最终售价向量 = 模型基准价 × 线路倍率（DEC-026）；同一请求所有候选共享同一售价，
	// 供保守预授权上界与结算扣费，不随命中哪条渠道变化。
	// 注意：此处为短上下文牌价；长上下文阶梯在授权/结算时按 LongContextPolicy + 输入合计再缩放。
	SalePrice billing.CustomerPriceSnapshot

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

	// RPMLimit/TPMLimit/RPDLimit 是该候选命中渠道的渠道级限流上限（P2-8）：
	// nil 表示「继承全局默认」，0 表示「显式不限」，>0 表示具体上限。调用上游前在 attempt runner 生效。
	RPMLimit *int64
	TPMLimit *int64
	RPDLimit *int64

	// ConcurrencyLimit 是该候选命中渠道的在途并发上限（DEC-029）：
	// nil=继承全局默认，0=显式不限，>0=具体上限。命中时该候选被跳过 fallback 到下一渠道。
	ConcurrencyLimit *int64

	// BillsOnDisconnect 标记该候选渠道的上游「断开仍计费」（DESIGN-bill-on-cancel 阶段一）：
	// true 时失败/取消路径会记平台成本敞口（channel_cost_exposures），纯观测不影响路由与客户计费。
	BillsOnDisconnect bool
}

// ChatRoutePlan 表示一次 chat 请求的同模型候选计划。
type ChatRoutePlan struct {
	RequestedModel string
	Candidates     []ChatRouteCandidate
	PoolSize       int

	// RouteMode 是本次请求解析出的线路策略（balanced/fixed），供 lifecycle 候选排序消费。
	RouteMode string

	// RouteStickyEnabled 是线路行 sticky_enabled（会话粘性路由开关，大 uncache 缺口 P0）：
	// nil=继承系统设置 gateway.routing_sticky.enabled_default，true/false=线路显式覆盖。
	RouteStickyEnabled *bool
}

// Store 定义 routing 查询候选渠道所需的最小数据库能力。
type Store interface {
	ModelExistsByID(ctx context.Context, requestedModelID string) (bool, error)
	UserCanUseModel(ctx context.Context, arg sqlc.UserCanUseModelParams) (bool, error)
	FindRouteCandidates(ctx context.Context, arg sqlc.FindRouteCandidatesParams) ([]sqlc.FindRouteCandidatesRow, error)
	GetRouteByID(ctx context.Context, id int64) (sqlc.Route, error)
	CountRouteChannels(ctx context.Context, routeID int64) (int64, error)
}

// resolvedRoute 是线路解析后的最小事实（策略 + 价格倍率 + 会话粘性开关）。
type resolvedRoute struct {
	ID         int64
	Name       string
	Mode       string
	PriceRatio pgtype.Numeric
	// StickyEnabled：nil=继承全局默认，非 nil=线路显式覆盖（大 uncache 缺口 P0）。
	StickyEnabled *bool
}

// Router 负责根据 project 和 requested model 选择可用 channel。
//
// defaultTimeout 可运行时热改（SetDefaultTimeout），用 atomic 存储（纳秒）：
// 路由热路径每次候选构造都会读取，无锁竞争。
type Router struct {
	store               Store
	defaultTimeoutNanos atomic.Int64
	logger              *zap.Logger
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

// NewRouter 创建 routing router。
func NewRouter(store Store, defaultTimeout time.Duration, opts ...Option) *Router {
	r := &Router{
		store:  store,
		logger: zap.NewNop(),
	}
	r.SetDefaultTimeout(defaultTimeout)
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// SetDefaultTimeout 原子替换默认渠道超时（运行时热改入口）；<=0 兜底为内置 30s。
// 仅影响之后的候选构造；渠道行上的 timeout_ms 始终优先。
func (r *Router) SetDefaultTimeout(d time.Duration) {
	if d <= 0 {
		d = defaultChannelTimeout
	}
	r.defaultTimeoutNanos.Store(int64(d))
}

// defaultTimeout 返回当前生效的默认渠道超时。
func (r *Router) defaultTimeout() time.Duration {
	return time.Duration(r.defaultTimeoutNanos.Load())
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

	route, err := r.resolveRoute(ctx, req)
	if err != nil {
		return ChatRoutePlan{}, err
	}
	poolSize, err := r.store.CountRouteChannels(ctx, route.ID)
	if err != nil {
		return ChatRoutePlan{}, failure.Wrap(
			failure.CodeRoutingStoreFailed, err,
			failure.WithMessage("count route channels"),
		)
	}
	if route.Mode == "fixed" {
		if poolSize != 1 {
			return ChatRoutePlan{}, failure.Wrap(
				failure.CodeRoutingNoAvailableChannel, ErrNoAvailableChannel,
				failure.WithMessage("fixed route must contain exactly one channel"),
				failure.WithField("route_id", route.ID),
				failure.WithField("pool_size", poolSize),
			)
		}
	}

	rows, err := r.findCandidateRows(ctx, req, route)
	if err != nil {
		return ChatRoutePlan{}, err
	}

	candidates := make([]ChatRouteCandidate, 0, len(rows))
	marginFiltered := false
	for _, row := range rows {
		candidate, err := r.buildChatRouteCandidate(ctx, row, route)
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
			r.logger.Warn("routing: skip unusable candidate", fields...)
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
		RequestedModel:     req.ModelID,
		Candidates:         candidates,
		PoolSize:           int(poolSize),
		RouteMode:          route.Mode,
		RouteStickyEnabled: route.StickyEnabled,
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

// resolveRoute 把 Key 绑定解析成本次请求的有效线路（候选池 + 策略）。
// 线路必填、无默认回落：Key 绑定线路缺失或已停用即拒绝请求。
func (r *Router) resolveRoute(ctx context.Context, req ChatRouteRequest) (resolvedRoute, error) {
	if route, ok := r.loadEnabledRoute(ctx, req.RouteID); ok {
		return route, nil
	}

	return resolvedRoute{}, failure.Wrap(
		failure.CodeRoutingRouteNotConfigured,
		ErrRouteNotConfigured,
		failure.WithMessage(ErrRouteNotConfigured.Error()),
	)
}

// loadEnabledRoute 读取指定线路；不存在或已停用返回 ok=false（上层据此拒绝请求）。
func (r *Router) loadEnabledRoute(ctx context.Context, id *int64) (resolvedRoute, bool) {
	if id == nil {
		return resolvedRoute{}, false
	}
	row, err := r.store.GetRouteByID(ctx, *id)
	if err != nil {
		// 不存在（理论上被 FK 阻止）或读失败都保守回落，不阻断请求。
		return resolvedRoute{}, false
	}
	if row.Status != "enabled" {
		return resolvedRoute{}, false
	}
	resolved := resolvedRoute{ID: row.ID, Name: row.Name, Mode: row.Mode, PriceRatio: row.PriceRatio}
	if row.StickyEnabled.Valid {
		enabled := row.StickyEnabled.Bool
		resolved.StickyEnabled = &enabled
	}
	return resolved, true
}

func (r *Router) findCandidateRows(ctx context.Context, req ChatRouteRequest, route resolvedRoute) ([]sqlc.FindRouteCandidatesRow, error) {
	// TODO(阶段6/production): [GAP-6-005] routing 已支持 user_model_policies 模型 allow-list/deny-list，但尚未表达用户禁用、预算约束或专属 channel 策略；预算约束进入 reservation，用户禁用进入后台管理策略。
	rows, err := r.store.FindRouteCandidates(ctx, sqlc.FindRouteCandidatesParams{
		RequestedModelID: req.ModelID,
		IngressProtocol:  req.IngressProtocol,
		UserID:           req.UserID,
		RouteID:          route.ID,
		AtTime:           pgtype.Timestamptz{Time: time.Now(), Valid: true},
	})
	if err != nil {
		return nil, failure.Wrap(
			failure.CodeRoutingStoreFailed,
			err,
			failure.WithMessage("find route candidates"),
		)
	}

	// 1 有候选 channel，正常返回。
	if len(rows) > 0 {
		return rows, nil
	}

	// 2.1 没候选，先问模型是否存在。
	exists, err := r.store.ModelExistsByID(ctx, req.ModelID)
	if err != nil {
		return nil, failure.Wrap(
			failure.CodeRoutingStoreFailed,
			err,
			failure.WithMessage("check model exists"),
		)
	}
	// 2.2 模型不存在，返回 ErrModelNotFound。
	if !exists {
		return nil, failure.Wrap(
			failure.CodeRoutingModelNotFound,
			ErrModelNotFound,
			failure.WithMessage(ErrModelNotFound.Error()),
		)
	}

	// 3.1 模型存在，再问 user 是否允许
	allowed, err := r.store.UserCanUseModel(ctx, sqlc.UserCanUseModelParams{
		UserID:           req.UserID,
		RequestedModelID: req.ModelID,
	})
	if err != nil {
		return nil, failure.Wrap(
			failure.CodeRoutingStoreFailed,
			err,
			failure.WithMessage("check user model policy"),
		)
	}
	// 3.2 user 不允许，返回 ErrModelNotAvailable。
	if !allowed {
		return nil, failure.Wrap(
			failure.CodeRoutingModelNotAvailable,
			ErrModelNotAvailable,
			failure.WithMessage(ErrModelNotAvailable.Error()),
		)
	}

	// 4 都没问题但还是没候选，才是 ErrNoAvailableChannel。
	return nil, failure.Wrap(
		failure.CodeRoutingNoAvailableChannel,
		ErrNoAvailableChannel,
		failure.WithMessage(ErrNoAvailableChannel.Error()),
	)
}

// int4LimitPtr 把可空 pgtype.Int4 限流上限转成 *int64（nil=继承全局默认，0=不限，>0=上限）。
func int4LimitPtr(v pgtype.Int4) *int64 {
	if !v.Valid {
		return nil
	}
	out := int64(v.Int32)
	return &out
}

func (r *Router) buildChatRouteCandidate(ctx context.Context, row sqlc.FindRouteCandidatesRow, route resolvedRoute) (ChatRouteCandidate, error) {
	// 渠道凭据明文存储（产品决策）：直接取用，仅防御性校验非空（DB 已 NOT NULL + CHECK <> ''）。
	apiKey := strings.TrimSpace(row.Credential)
	if apiKey == "" {
		return ChatRouteCandidate{}, failure.Wrap(
			failure.CodeRoutingCredentialResolveFailed,
			ErrChannelCredentialMissing,
			failure.WithMessage(ErrChannelCredentialMissing.Error()),
		)
	}

	// 客户售价 = 模型基准售价（model_prices）× 线路倍率（routes.price_ratio）（DEC-026）。
	// 同一请求所有候选共享同一售价，不随命中哪条渠道而变。
	basePrice := billing.CustomerPriceSnapshot{
		Currency:                row.BaseCurrency,
		PricingUnit:             row.BasePricingUnit,
		UncachedInputPrice:      row.UncachedInputPrice,
		CacheReadInputPrice:     row.CacheReadInputPrice,
		CacheWrite5mInputPrice:  row.CacheWrite5mInputPrice,
		CacheWrite1hInputPrice:  row.CacheWrite1hInputPrice,
		CacheWrite30mInputPrice: row.CacheWrite30mInputPrice,
		OutputPrice:             row.OutputPrice,
		ReasoningOutputPrice:    row.ReasoningOutputPrice,
		FormulaVersion:          billing.FormulaVersionV1,
	}
	salePrice, err := billing.ScaleCustomerPrice(basePrice, route.PriceRatio)
	if err != nil {
		return ChatRouteCandidate{}, failure.Wrap(
			failure.CodeBillingInvalidPrice,
			err,
			failure.WithMessage("scale customer price by route price_ratio"),
		)
	}

	timeout := r.defaultTimeout()
	if row.TimeoutMs.Valid {
		timeout = time.Duration(row.TimeoutMs.Int32) * time.Millisecond
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
	violations, err := billing.ValidateNonNegativeMargin(salePrice, channelCost)
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

	return ChatRouteCandidate{
		ModelDBID:         row.ModelDbID,
		ProviderID:        row.ProviderID,
		AdapterKey:        row.AdapterKey,
		Protocol:          row.Protocol,
		MaxOutputTokens:   maxOutputTokens,
		RPMLimit:          int4LimitPtr(row.ChannelRpmLimit),
		TPMLimit:          int4LimitPtr(row.ChannelTpmLimit),
		RPDLimit:          int4LimitPtr(row.ChannelRpdLimit),
		ConcurrencyLimit:  int4LimitPtr(row.ChannelConcurrencyLimit),
		BillsOnDisconnect: row.ChannelBillsOnDisconnect,
		RouteName:         route.Name,
		Channel: channel.Runtime{
			ID:           row.ChannelID,
			Name:         row.ChannelName,
			BaseURL:      row.BaseUrl,
			APIKey:       apiKey,
			Timeout:      timeout,
			ProviderSlug: row.ProviderSlug,
		},
		UpstreamModel:           row.UpstreamModel,
		ModelPriceID:            row.ModelPriceID,
		PriceRatio:              route.PriceRatio,
		SalePrice:               salePrice,
		LongContextPolicy:       longContextPolicyFromRouteRow(row),
		ChannelPriceID:          row.ChannelPriceID,
		CostBaseModelPriceID:    costBaseModelPriceID,
		ChannelCostMultiplierID: channelCostMultiplierID,
		ChannelRechargeFactorID: channelRechargeFactorID,
		ChannelCost:             channelCost,
	}, nil
}

// resolveCandidateCost 从候选行解析渠道真实成本与来源 pin（DEC-027 倍率 + DEC-031 单基数）。
//   - 绝对覆盖（row.ChannelPriceID != 0）：直接用 channel_prices 成本列，来源 id 归零。
//   - 倍率路径：成本基数 = 模型基准价（base，DEC-031 复用 model_prices，由 basePrice 映射为成本向量），
//     真实成本 = 基数 × 价格倍率 × 充值倍率（充值缺省 1.0）；带回成本基数（model_price）/价格倍率/充值倍率行 id 作 pin。
//
// basePrice 是调用方已从 base(model_prices) 列构造的售价向量（与 SalePrice 同源），此处映射为成本向量作基数，
// 保证售价与成本共用同一 model_prices 基数（DEC-031 核心不变量）。
func resolveCandidateCost(row sqlc.FindRouteCandidatesRow, basePrice billing.CustomerPriceSnapshot) (cost billing.ProviderCostSnapshot, costBaseModelPriceID, multiplierID, rechargeFactorID int64, err error) {
	if row.ChannelPriceID != 0 {
		return billing.ProviderCostSnapshot{
			Currency:               row.CostCurrency,
			PricingUnit:            row.CostPricingUnit,
			UncachedInputCost:      row.UncachedInputCost,
			CacheReadInputCost:     row.CacheReadInputCost,
			CacheWrite5mInputCost:  row.CacheWrite5mInputCost,
			CacheWrite1hInputCost:  row.CacheWrite1hInputCost,
			CacheWrite30mInputCost: row.CacheWrite30mInputCost,
			OutputCost:             row.OutputCost,
			ReasoningOutputCost:    row.ReasoningOutputCost,
			FormulaVersion:         billing.FormulaVersionV1,
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
	return scaled, row.ModelPriceID, row.ChannelCostMultiplierID, row.ChannelRechargeFactorID, nil
}

// rechargeFactorOrDefault 充值倍率未配置（NULL）时按 1.0（名义即真实，向后兼容）。
func rechargeFactorOrDefault(factor pgtype.Numeric) pgtype.Numeric {
	if factor.Valid {
		return factor
	}
	return pgtype.Numeric{Int: big.NewInt(1), Exp: 0, Valid: true}
}

// longContextPolicyFromRouteRow 从候选行的 model_prices 长上下文字段组装策略。
func longContextPolicyFromRouteRow(row sqlc.FindRouteCandidatesRow) billing.LongContextPolicy {
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
