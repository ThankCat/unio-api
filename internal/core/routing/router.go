package routing

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-api/internal/core/billing"
	"github.com/ThankCat/unio-api/internal/core/channel"
	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
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
	SalePrice billing.CustomerPriceSnapshot

	// ChannelPriceID 是命中渠道当前生效的 channel_prices 行 ID（成本来源，供结算/快照审计）。
	ChannelPriceID int64
	// ChannelCost 是命中渠道当前生效的上游成本快照；毛利 = SalePrice − ChannelCost。
	ChannelCost billing.ProviderCostSnapshot

	// RPMLimit/TPMLimit/RPDLimit 是该候选命中渠道的渠道级限流上限（P2-8）：
	// nil 表示「继承全局默认」，0 表示「显式不限」，>0 表示具体上限。调用上游前在 attempt runner 生效。
	RPMLimit *int64
	TPMLimit *int64
	RPDLimit *int64
}

// ChatRoutePlan 表示一次 chat 请求的同模型候选计划。
type ChatRoutePlan struct {
	RequestedModel string
	Candidates     []ChatRouteCandidate

	// RouteMode 是本次请求解析出的线路策略（cheapest/stable/fixed），供 lifecycle 候选排序消费（阶段 15）。
	RouteMode string
}

// Store 定义 routing 查询候选渠道所需的最小数据库能力。
type Store interface {
	ModelExistsByID(ctx context.Context, requestedModelID string) (bool, error)
	UserCanUseModel(ctx context.Context, arg sqlc.UserCanUseModelParams) (bool, error)
	FindRouteCandidates(ctx context.Context, arg sqlc.FindRouteCandidatesParams) ([]sqlc.FindRouteCandidatesRow, error)
	GetRouteByID(ctx context.Context, id int64) (sqlc.Route, error)
}

// resolvedRoute 是线路解析后的最小事实（候选池 + 策略 + 价格倍率）。
type resolvedRoute struct {
	ID         int64
	Mode       string
	PoolKind   string
	PriceRatio pgtype.Numeric
}

// Router 负责根据 project 和 requested model 选择可用 channel。
type Router struct {
	store          Store
	defaultTimeout time.Duration
	logger         *slog.Logger
}

// Option 调整 Router 的可选依赖（如日志）。
type Option func(*Router)

// WithLogger 注入结构化日志器，用于记录被跳过的坏候选（P1-1）。
func WithLogger(logger *slog.Logger) Option {
	return func(r *Router) {
		if logger != nil {
			r.logger = logger
		}
	}
}

// NewRouter 创建 routing router。
func NewRouter(store Store, defaultTimeout time.Duration, opts ...Option) *Router {
	if defaultTimeout <= 0 {
		defaultTimeout = defaultChannelTimeout
	}

	r := &Router{
		store:          store,
		defaultTimeout: defaultTimeout,
		logger:         slog.Default(),
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
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

	rows, err := r.findCandidateRows(ctx, req, route)
	if err != nil {
		return ChatRoutePlan{}, err
	}

	candidates := make([]ChatRouteCandidate, 0, len(rows))
	for _, row := range rows {
		candidate, err := r.buildChatRouteCandidate(ctx, row, route)
		if err != nil {
			// P1-1：单个候选凭据缺失/解密失败时跳过该候选并记日志，不让整批 plan 失败；
			// 只有当全部候选都不可用时才在循环后报 no_available_channel，最大化可用性。
			r.logger.WarnContext(ctx, "routing: skip unusable candidate",
				append([]any{
					"channel_id", row.ChannelID,
					"provider_slug", row.ProviderSlug,
					"adapter_key", row.AdapterKey,
					"upstream_model", row.UpstreamModel,
				}, failure.LogArgs(err)...)...)
			continue
		}
		candidates = append(candidates, candidate)
	}

	// rows 非空（findCandidateRows 已区分 model 不存在/不可用/无渠道），若到此处候选全被跳过，
	// 说明命中渠道的凭据全部不可用：报 no_available_channel 而非泄露底层凭据错误。
	if len(candidates) == 0 {
		return ChatRoutePlan{}, failure.Wrap(
			failure.CodeRoutingNoAvailableChannel,
			ErrNoAvailableChannel,
			failure.WithMessage("all matched channels are unusable (credential missing)"),
		)
	}

	plan := ChatRoutePlan{
		RequestedModel: req.ModelID,
		Candidates:     candidates,
		RouteMode:      route.Mode,
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
	return resolvedRoute{ID: row.ID, Mode: row.Mode, PoolKind: row.PoolKind, PriceRatio: row.PriceRatio}, true
}

func (r *Router) findCandidateRows(ctx context.Context, req ChatRouteRequest, route resolvedRoute) ([]sqlc.FindRouteCandidatesRow, error) {
	// TODO(阶段6/production): [GAP-6-005] routing 已支持 user_model_policies 模型 allow-list/deny-list，但尚未表达用户禁用、预算约束或专属 channel 策略；预算约束进入 reservation，用户禁用进入后台管理策略。
	rows, err := r.store.FindRouteCandidates(ctx, sqlc.FindRouteCandidatesParams{
		RequestedModelID: req.ModelID,
		IngressProtocol:  req.IngressProtocol,
		UserID:           req.UserID,
		PoolKind:         route.PoolKind,
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
		Currency:               row.BaseCurrency,
		PricingUnit:            row.BasePricingUnit,
		UncachedInputPrice:     row.UncachedInputPrice,
		CacheReadInputPrice:    row.CacheReadInputPrice,
		CacheWrite5mInputPrice: row.CacheWrite5mInputPrice,
		CacheWrite1hInputPrice: row.CacheWrite1hInputPrice,
		OutputPrice:            row.OutputPrice,
		ReasoningOutputPrice:   row.ReasoningOutputPrice,
		FormulaVersion:         billing.FormulaVersionV1,
	}
	salePrice, err := billing.ScaleCustomerPrice(basePrice, route.PriceRatio)
	if err != nil {
		return ChatRouteCandidate{}, failure.Wrap(
			failure.CodeBillingInvalidPrice,
			err,
			failure.WithMessage("scale customer price by route price_ratio"),
		)
	}

	timeout := r.defaultTimeout
	if row.TimeoutMs.Valid {
		timeout = time.Duration(row.TimeoutMs.Int32) * time.Millisecond
	}

	maxOutputTokens := int64(0)
	if row.ModelMaxOutputTokens.Valid {
		maxOutputTokens = row.ModelMaxOutputTokens.Int64
	}

	return ChatRouteCandidate{
		ModelDBID:       row.ModelDbID,
		ProviderID:      row.ProviderID,
		AdapterKey:      row.AdapterKey,
		Protocol:        row.Protocol,
		MaxOutputTokens: maxOutputTokens,
		RPMLimit:        int4LimitPtr(row.ChannelRpmLimit),
		TPMLimit:        int4LimitPtr(row.ChannelTpmLimit),
		RPDLimit:        int4LimitPtr(row.ChannelRpdLimit),
		Channel: channel.Runtime{
			ID:           row.ChannelID,
			BaseURL:      row.BaseUrl,
			APIKey:       apiKey,
			Timeout:      timeout,
			ProviderSlug: row.ProviderSlug,
		},
		UpstreamModel:  row.UpstreamModel,
		ModelPriceID:   row.ModelPriceID,
		PriceRatio:     route.PriceRatio,
		SalePrice:      salePrice,
		ChannelPriceID: row.ChannelPriceID,
		ChannelCost: billing.ProviderCostSnapshot{
			Currency:              row.CostCurrency,
			PricingUnit:           row.CostPricingUnit,
			UncachedInputCost:     row.UncachedInputCost,
			CacheReadInputCost:    row.CacheReadInputCost,
			CacheWrite5mInputCost: row.CacheWrite5mInputCost,
			CacheWrite1hInputCost: row.CacheWrite1hInputCost,
			OutputCost:            row.OutputCost,
			ReasoningOutputCost:   row.ReasoningOutputCost,
			FormulaVersion:        billing.FormulaVersionV1,
		},
	}, nil
}
