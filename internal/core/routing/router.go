package routing

import (
	"context"
	"errors"
	"time"

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
}

// ChatRouteCandidate 表示一个可尝试的 chat 上游候选。
type ChatRouteCandidate struct {
	ModelDBID     int64
	ProviderID    int64
	AdapterKey    string
	Protocol      string
	Channel       channel.Runtime
	UpstreamModel string
}

// ChatRoutePlan 表示一次 chat 请求的同模型候选计划。
type ChatRoutePlan struct {
	RequestedModel string
	Candidates     []ChatRouteCandidate
}

// Store 定义 routing 查询候选渠道所需的最小数据库能力。
type Store interface {
	ModelExistsByID(ctx context.Context, requestedModelID string) (bool, error)
	ProjectCanUseModel(ctx context.Context, arg sqlc.ProjectCanUseModelParams) (bool, error)
	FindRouteCandidates(ctx context.Context, arg sqlc.FindRouteCandidatesParams) ([]sqlc.FindRouteCandidatesRow, error)
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

	rows, err := r.findCandidateRows(ctx, req)
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

	return ChatRoutePlan{
		RequestedModel: req.ModelID,
		Candidates:     candidates,
	}, nil
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

func (r *Router) findCandidateRows(ctx context.Context, req ChatRouteRequest) ([]sqlc.FindRouteCandidatesRow, error) {
	// TODO(阶段6/production): [GAP-6-005] routing 已支持 project_model_policies 模型 allow-list/deny-list，但尚未表达 project 禁用、预算约束或专属 channel 策略；阶段 7 authorization/余额冻结和阶段 11 项目策略管理前；预算约束进入 reservation，project 禁用和 project_channel policy 进入后台管理策略。
	rows, err := r.store.FindRouteCandidates(ctx, sqlc.FindRouteCandidatesParams{
		RequestedModelID: req.ModelID,
		IngressProtocol:  req.IngressProtocol,
		ProjectID:        req.ProjectID,
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
		UpstreamModel: row.UpstreamModel,
	}, nil
}
