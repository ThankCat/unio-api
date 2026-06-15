package routing

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-api/internal/core/billing"
	"github.com/ThankCat/unio-api/internal/core/capability"
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

// CapabilityEnforcement 表示 capability 闸门按 ingress 表面独立可控的 enforce 开关（TASK-12.08）。
//
// 三个表面对应 DEC-015 灰度切换顺序（先 OpenAI Chat 再 Anthropic 再 Responses）；
// 全 false（零值）即 observe 模式：闸门只记录判定、不拒绝。enforce 切换是上线策略决策（GAP-12-009）。
type CapabilityEnforcement struct {
	OpenAIChat        bool
	AnthropicMessages bool
	OpenAIResponses   bool
}

// enabledFor 判断指定 ingress 表面是否启用 enforce。
func (e CapabilityEnforcement) enabledFor(operation string) bool {
	switch operation {
	case OperationChatCompletions:
		return e.OpenAIChat
	case OperationMessages:
		return e.AnthropicMessages
	case OperationResponses:
		return e.OpenAIResponses
	default:
		return false
	}
}

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

	// ErrModelCapabilityUnavailable 表示模型本身不支持请求所需能力（capability 闸门 Layer 2 缺失）。
	//
	// observe 模式只记录该判定、不返回；enforce 模式（TASK-12.08）据此拒绝并渲染三协议原生 capability 错误。
	ErrModelCapabilityUnavailable = errors.New("model capability unavailable")

	// ErrChannelCapabilityUnavailable 表示模型支持但所有候选 channel 都 override 关闭了所需能力（capability 闸门 Layer 3）。
	//
	// observe 模式只记录该判定、不返回；enforce 模式（TASK-12.08）据此拒绝并渲染三协议原生 capability 错误。
	ErrChannelCapabilityUnavailable = errors.New("channel capability unavailable")
)

// ChatRouteRequest 表示一次 routing 选择所需上下文。
type ChatRouteRequest struct {
	ProjectID int64
	ModelID   string

	// IngressProtocol 是客户请求的协议族（如 openai）；routing 只返回同协议 channel 候选。
	IngressProtocol string

	// Operation 是本次请求的 ingress 表面（chat_completions/messages/responses），
	// 供 capability 闸门按表面独立判断 enforce 是否启用；空值视为不 enforce。
	Operation string

	// RequiredCapabilities 是 ingress 推断出的本次请求所需能力集（TASK-12.02）。
	//
	// 由 capability 闸门在 observe 模式下消费：记录判定与 metric，不影响候选与返回（enforce 见 TASK-12.08）。
	// 零值集合表示未推断或无消费方，闸门跳过。
	RequiredCapabilities capability.Set

	// RequestLimits 是 ingress 推断出的本次请求「带值」能力约束（如 reasoning.effort 档位，GAP-12-012）。
	//
	// 供闸门对 limited 能力做超限判定；零值表示请求未声明档位，limited 一律视为满足。
	RequestLimits capability.RequestLimits

	// RouteID 是 API Key 绑定的线路 ID（阶段 15）；nil 表示未绑定。
	RouteID *int64
	// ProjectDefaultRouteID 是所属项目的默认线路 ID；nil 表示未设。
	// 线路解析优先级：RouteID ?? ProjectDefaultRouteID ?? 内置「经济」。
	ProjectDefaultRouteID *int64
}

// CapabilityObservation 是 capability 闸门对一次 routing 的判定快照，供 observe 记录与未来 enforce 拒绝。
type CapabilityObservation struct {
	// Required 是本次请求推断出的所需能力，升序。
	Required []capability.Key
	// Result 是闸门判定结论（ok/model_unavailable/channel_unavailable/unprovisioned/no_required）。
	Result capability.GateResult
	// Provisioned 表示模型是否已有能力声明行。
	Provisioned bool
	// MissingModel / MissingChannel 是模型层 / channel 层缺失的能力明细。
	MissingModel   []capability.Key
	MissingChannel []capability.Key
}

// CapabilityCheckInput 是 routing 交给 capability 闸门的判定入参。
//
// Protocol 是本次请求的 ingress 协议族（openai/anthropic），闸门仅用于 observe 指标与日志维度，
// 不参与能力判定本身（判定逻辑协议无关）。
type CapabilityCheckInput struct {
	Protocol   string
	ModelDBID  int64
	ChannelIDs []int64
	Required   capability.Set
	// Limits 是请求侧「带值」能力约束（如 reasoning.effort 档位），供闸门判定 limited 是否超限（GAP-12-012）。
	Limits capability.RequestLimits
}

// CapabilityChecker 评估一次 routing 的 capability 闸门结论。
//
// 实现负责读取 model_capabilities / channel overrides、调用纯判定、并在内部发 metric/审计日志。
// 约定：实现绝不返回 error，存储读取失败等异常在内部降级处理并记 result=error，
// 保证 observe（及未来 enforce 的 fail-open）不会因闸门基础设施抖动而中断主流程。
type CapabilityChecker interface {
	Check(ctx context.Context, in CapabilityCheckInput) CapabilityObservation
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

	// Capability 是 capability 闸门 observe 判定快照；闸门未启用或无 required 时为 nil。
	// observe 模式下它不影响 Candidates，仅供调用方审计/持久化与未来 enforce 消费。
	Capability *CapabilityObservation
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
	store                 Store
	credentialDecryptor   CredentialDecryptor
	defaultTimeout        time.Duration
	capabilityChecker     CapabilityChecker
	capabilityEnforcement CapabilityEnforcement
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

// SetCapabilityChecker 注入 capability 闸门判定器（observe 模式）。
//
// 闸门是可选基础设施，由 bootstrap 在装配阶段、开始服务前注入一次；nil 表示不做能力判定。
// 单独设置而非进构造函数，避免影响既有 NewRouter 调用方与测试。
func (r *Router) SetCapabilityChecker(checker CapabilityChecker) {
	r.capabilityChecker = checker
}

// SetCapabilityEnforcement 注入 capability 闸门按表面独立的 enforce 开关（TASK-12.08）。
//
// 由 bootstrap 在装配阶段、开始服务前注入一次；零值（全 false）即 observe 模式。
// 与 SetCapabilityChecker 分离：checker 负责判定与可观测，enforcement 只决定「判定为不可用时是否拒绝」。
func (r *Router) SetCapabilityEnforcement(enforcement CapabilityEnforcement) {
	r.capabilityEnforcement = enforcement
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
	plan.Capability = r.observeCapability(ctx, req.IngressProtocol, candidates, req.RequiredCapabilities, req.RequestLimits)

	if err := r.enforceCapability(req, plan.Capability); err != nil {
		// 返回只携带判定快照的空计划 + 错误：候选清空（与其它错误返回一致，调用方必须先判 err），
		// 但保留 Capability 供调用方写 request_records 审计列（enforce 拒绝是最该审计的判定）。
		return ChatRoutePlan{Capability: plan.Capability}, err
	}

	return plan, nil
}

// enforceCapability 在该 ingress 表面启用 enforce 时，把闸门「不可用」判定升级为路由错误（TASK-12.08）。
//
// 默认（observe，全表面 enforce=false）恒返回 nil，行为与历史一致。仅当对应表面 enforce 开启
// 且判定为 model/channel 不可用时返回 sentinel + 稳定错误码，由 app 层渲染为协议原生 capability 错误；
// 缺失能力 key 附在 failure field（capability key 是公开稳定标识，安全可暴露；channel 身份绝不暴露）。
func (r *Router) enforceCapability(req ChatRouteRequest, observation *CapabilityObservation) error {
	if observation == nil || !r.capabilityEnforcement.enabledFor(req.Operation) {
		return nil
	}

	switch observation.Result {
	case capability.GateResultModelUnavailable:
		return failure.Wrap(
			failure.CodeRoutingModelCapabilityUnavailable,
			ErrModelCapabilityUnavailable,
			failure.WithMessage(ErrModelCapabilityUnavailable.Error()),
			failure.WithField("missing_capabilities", joinKeys(observation.MissingModel)),
		)
	case capability.GateResultChannelUnavailable:
		return failure.Wrap(
			failure.CodeRoutingChannelCapabilityUnavailable,
			ErrChannelCapabilityUnavailable,
			failure.WithMessage(ErrChannelCapabilityUnavailable.Error()),
			failure.WithField("missing_capabilities", joinKeys(observation.MissingChannel)),
		)
	default:
		return nil
	}
}

// joinKeys 把缺失能力 key 拼成稳定有序的逗号分隔串，用于错误 field（不含敏感信息）。
func joinKeys(keys []capability.Key) string {
	parts := make([]string, len(keys))
	for i, key := range keys {
		parts[i] = string(key)
	}
	return strings.Join(parts, ",")
}

// MissingCapabilities 从 capability enforce 错误中提取缺失能力 key 的逗号分隔串，供 app 层渲染客户文案。
//
// 把 failure field key 作为 routing 的内部细节封装在此：非 capability 错误或无 field 时返回空串。
// 返回的 capability key 是公开稳定标识，安全可暴露给客户。
func MissingCapabilities(err error) string {
	for _, field := range failure.FieldsOf(err) {
		if field.Key != "missing_capabilities" {
			continue
		}
		if value, ok := field.Value.(string); ok {
			return value
		}
	}
	return ""
}

// observeCapability 在 observe 模式下评估 capability 闸门并返回判定快照，绝不改变候选或返回错误。
//
// 闸门未注入、无候选或无 required 时返回 nil。所有候选属于同一 requested model，
// 故模型层判定共用 candidates[0].ModelDBID；channel 层逐候选评估 override。
func (r *Router) observeCapability(ctx context.Context, protocol string, candidates []ChatRouteCandidate, required capability.Set, limits capability.RequestLimits) *CapabilityObservation {
	if r.capabilityChecker == nil || len(candidates) == 0 || required.Len() == 0 {
		return nil
	}

	channelIDs := make([]int64, 0, len(candidates))
	for _, candidate := range candidates {
		channelIDs = append(channelIDs, candidate.Channel.ID)
	}

	observation := r.capabilityChecker.Check(ctx, CapabilityCheckInput{
		Protocol:   protocol,
		ModelDBID:  candidates[0].ModelDBID,
		ChannelIDs: channelIDs,
		Required:   required,
		Limits:     limits,
	})

	return &observation
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
