package routing

import (
	"context"
	"errors"
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

	// ErrModelNotAvailable 表示模型存在但当前 project 不允许使用。
	ErrModelNotAvailable = errors.New("model not available for project")

	// ErrChannelCredentialMissing 表示 channel 未配置加密凭据。
	ErrChannelCredentialMissing = errors.New("channel credential missing")

	// ErrIngressProtocolInvalid 表示 routing 请求没有携带受支持的 ingress 协议族。
	ErrIngressProtocolInvalid = errors.New("ingress protocol invalid")
)

// ChatRouteRequest 表示一次 routing 选择所需上下文。
type ChatRouteRequest struct {
	ProjectID int64
	ModelID   string

	// IngressProtocol 是客户请求的协议族（如 openai）；routing 只返回同协议 channel 候选。
	IngressProtocol string

	// Operation 是本次请求的 ingress 表面（chat_completions/messages/responses），供审计/日志维度。
	Operation string

	// RouteID 是 API Key 绑定的线路 ID（阶段 15）；nil 表示未绑定。
	RouteID *int64
	// ProjectDefaultRouteID 是所属项目的默认线路 ID；nil 表示未设。
	// 线路解析优先级：RouteID ?? ProjectDefaultRouteID ?? 内置「经济」。
	ProjectDefaultRouteID *int64
}

// ChatRouteCandidate 表示一个可尝试的 chat 上游候选。
type ChatRouteCandidate struct {
	ModelDBID     int64
	ProviderID    int64
	AdapterKey    string
	Protocol      string
	Channel       channel.Runtime
	UpstreamModel string

	// ChannelPriceID 是该候选当前生效的 channel_prices 行 ID（阶段 15，供审计/快照参考）。
	ChannelPriceID int64
	// SalePrice 是该候选命中渠道的当前生效售价向量，供 cheapest 排序与保守预授权上界。
	SalePrice billing.CustomerPriceSnapshot
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
	ProjectCanUseModel(ctx context.Context, arg sqlc.ProjectCanUseModelParams) (bool, error)
	FindRouteCandidates(ctx context.Context, arg sqlc.FindRouteCandidatesParams) ([]sqlc.FindRouteCandidatesRow, error)
	GetRouteByID(ctx context.Context, id int64) (sqlc.Route, error)
	GetBuiltinCheapestRoute(ctx context.Context) (sqlc.Route, error)
}

// resolvedRoute 是线路解析后的最小事实（候选池 + 策略）。
type resolvedRoute struct {
	ID       int64
	Mode     string
	PoolKind string
}

// CredentialDecryptor 把 channel 入库密文解出上游明文 API key。
type CredentialDecryptor interface {
	Decrypt(ciphertext []byte) (string, error)
}

// Router 负责根据 project 和 requested model 选择可用 channel。
type Router struct {
	store               Store
	credentialDecryptor CredentialDecryptor
	defaultTimeout      time.Duration
}

// NewRouter 创建 routing router。
func NewRouter(store Store, credentialDecryptor CredentialDecryptor, defaultTimeout time.Duration) *Router {
	if defaultTimeout <= 0 {
		defaultTimeout = defaultChannelTimeout
	}

	return &Router{
		store:               store,
		credentialDecryptor: credentialDecryptor,
		defaultTimeout:      defaultTimeout,
	}
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
		candidate, err := r.buildChatRouteCandidate(ctx, row)
		if err != nil {
			return ChatRoutePlan{}, err
		}
		candidates = append(candidates, candidate)
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

// resolveRoute 把 Key/项目绑定解析成本次请求的有效线路（候选池 + 策略）。
// 优先级：Key 线路 ?? 项目默认线路 ?? 内置「经济」；命中但已停用的线路视为未选，继续回落。
//
// TODO(阶段15/production): 线路解析每请求读 routes 表（最多 1~3 次点查）；routes 量极小、改动罕见，
// 后续接入与渠道/能力同款缓存层避免每请求打 DB（PLAN §7）。
func (r *Router) resolveRoute(ctx context.Context, req ChatRouteRequest) (resolvedRoute, error) {
	if route, ok := r.loadEnabledRoute(ctx, req.RouteID); ok {
		return route, nil
	}
	if route, ok := r.loadEnabledRoute(ctx, req.ProjectDefaultRouteID); ok {
		return route, nil
	}

	builtin, err := r.store.GetBuiltinCheapestRoute(ctx)
	if err != nil {
		return resolvedRoute{}, failure.Wrap(
			failure.CodeRoutingStoreFailed,
			err,
			failure.WithMessage("resolve builtin cheapest route"),
		)
	}
	return resolvedRoute{ID: builtin.ID, Mode: builtin.Mode, PoolKind: builtin.PoolKind}, nil
}

// loadEnabledRoute 读取指定线路；不存在或已停用返回 ok=false 让上层继续回落。
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
	return resolvedRoute{ID: row.ID, Mode: row.Mode, PoolKind: row.PoolKind}, true
}

func (r *Router) findCandidateRows(ctx context.Context, req ChatRouteRequest, route resolvedRoute) ([]sqlc.FindRouteCandidatesRow, error) {
	// TODO(阶段6/production): [GAP-6-005] routing 已支持 project_model_policies 模型 allow-list/deny-list，但尚未表达 project 禁用、预算约束或专属 channel 策略；预算约束进入 reservation，project 禁用进入后台管理策略。
	rows, err := r.store.FindRouteCandidates(ctx, sqlc.FindRouteCandidatesParams{
		RequestedModelID: req.ModelID,
		IngressProtocol:  req.IngressProtocol,
		ProjectID:        req.ProjectID,
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

	// 3.1 模型存在，再问 project 是否允许
	allowed, err := r.store.ProjectCanUseModel(ctx, sqlc.ProjectCanUseModelParams{
		ProjectID:        req.ProjectID,
		RequestedModelID: req.ModelID,
	})
	if err != nil {
		return nil, failure.Wrap(
			failure.CodeRoutingStoreFailed,
			err,
			failure.WithMessage("check project model policy"),
		)
	}
	// 3.2 project 不允许，返回 ErrModelNotAvailable。
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

func (r *Router) buildChatRouteCandidate(ctx context.Context, row sqlc.FindRouteCandidatesRow) (ChatRouteCandidate, error) {
	if len(row.CredentialEncrypted) == 0 {
		return ChatRouteCandidate{}, failure.Wrap(
			failure.CodeCredentialCiphertextInvalid,
			ErrChannelCredentialMissing,
			failure.WithMessage(ErrChannelCredentialMissing.Error()),
		)
	}

	apiKey, err := r.credentialDecryptor.Decrypt(row.CredentialEncrypted)
	if err != nil {
		return ChatRouteCandidate{}, failure.Wrap(
			failure.CodeRoutingCredentialResolveFailed,
			err,
			failure.WithMessage("decrypt channel credential"),
		)
	}

	timeout := r.defaultTimeout
	if row.TimeoutMs.Valid {
		timeout = time.Duration(row.TimeoutMs.Int32) * time.Millisecond
	}

	return ChatRouteCandidate{
		ModelDBID:  row.ModelDbID,
		ProviderID: row.ProviderID,
		AdapterKey: row.AdapterKey,
		Protocol:   row.Protocol,
		Channel: channel.Runtime{
			ID:           row.ChannelID,
			BaseURL:      row.BaseUrl,
			APIKey:       apiKey,
			Timeout:      timeout,
			ProviderSlug: row.ProviderSlug,
		},
		UpstreamModel:  row.UpstreamModel,
		ChannelPriceID: row.ChannelPriceID,
		SalePrice: billing.CustomerPriceSnapshot{
			Currency:               row.PriceCurrency,
			PricingUnit:            row.PricePricingUnit,
			UncachedInputPrice:     row.UncachedInputPrice,
			CacheReadInputPrice:    row.CacheReadInputPrice,
			CacheWrite5mInputPrice: row.CacheWrite5mInputPrice,
			CacheWrite1hInputPrice: row.CacheWrite1hInputPrice,
			OutputPrice:            row.OutputPrice,
			ReasoningOutputPrice:   row.ReasoningOutputPrice,
			FormulaVersion:         billing.FormulaVersionV1,
		},
	}, nil
}
