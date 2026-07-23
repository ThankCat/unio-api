// Package channel 编排 admin 管理端的 channel 读写。
//
// channel 写入路径负责：① 校验 (protocol, adapter_key) 复合键在 adapter registry 注册
// （关 GAP-6-003，避免把不可运行绑定写入业务数据）；② 把上游凭据以明文落库
// （产品决策：渠道凭据明文存储，管理端可查看/复制/编辑）。
package channel

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/core/runtimecontrol"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

const (
	// ProtocolOpenAI / ProtocolAnthropic 是 channel 对外协议族，与 channels.protocol 的 DB 约束一致。
	ProtocolOpenAI    = "openai"
	ProtocolAnthropic = "anthropic"

	// StatusEnabled / StatusDisabled 是 channel 启停状态，与 channels.status 的 DB 约束一致。
	StatusEnabled  = "enabled"
	StatusDisabled = "disabled"
	// StatusArchived 表示 channel 已归档（默认隐藏、不参与路由、已退出线路池；可恢复）。
	StatusArchived = "archived"
)

// Store 定义 channel 管理所需的存储能力。
type Store interface {
	GetProvider(ctx context.Context, id int64) (sqlc.Provider, error)
	GetProviderEndpoint(ctx context.Context, id int64) (sqlc.ProviderEndpoint, error)
	ListChannelsPage(ctx context.Context, arg sqlc.ListChannelsPageParams) ([]sqlc.ListChannelsPageRow, error)
	CountChannels(ctx context.Context, arg sqlc.CountChannelsParams) (int64, error)
	GetChannel(ctx context.Context, id int64) (sqlc.Channel, error)
	CreateChannel(ctx context.Context, arg sqlc.CreateChannelParams) (sqlc.Channel, error)
	UpdateChannel(ctx context.Context, arg sqlc.UpdateChannelParams) (sqlc.Channel, error)
	SetChannelBillingBehavior(ctx context.Context, arg sqlc.SetChannelBillingBehaviorParams) (sqlc.Channel, error)
	DeleteChannelCascade(ctx context.Context, id int64) (int64, error)
	ArchiveChannelCascade(ctx context.Context, id int64) (int64, error)
	ArchiveChannelWithReplacement(ctx context.Context, arg sqlc.ArchiveChannelWithReplacementParams) (int64, error)
	ListEnabledRoutesEmptiedByChannel(ctx context.Context, channelID int64) ([]sqlc.ListEnabledRoutesEmptiedByChannelRow, error)
	RestoreChannel(ctx context.Context, id int64) (int64, error)
}

// RuntimeControlPublisher 是 Channel 四维限额的 durable publisher。
type RuntimeControlPublisher interface {
	Publish(ctx context.Context, req runtimecontrol.PublishRequest) (runtimecontrol.PublishResult, error)
}

// AdmissionControlStore 提供 Channel admission control 的定位、初始化与只读核对能力。
type AdmissionControlStore interface {
	ChannelAdmissionControl(channelID int64) breakerstore.ControlTarget
	RestoreMissingControl(ctx context.Context, target breakerstore.ControlTarget, revision int64, payload string) (bool, error)
	ReadControl(ctx context.Context, target breakerstore.ControlTarget, expectedRevision int64) (breakerstore.ControlSnapshot, error)
}

// AdapterRegistry 暴露 channel 写入前校验复合键是否被当前进程支持的最小能力，
// 以及把可选 adapter_key 枚举出来供 admin 前端下拉。
type AdapterRegistry interface {
	HasAny(protocol string, adapterKey string) bool
	// AdapterKeys 返回指定协议族下当前进程注册的全部 adapter key（去重、字典序）。
	AdapterKeys(protocol string) []string
}

// AdapterKeyOption 是某协议族下一个可选 adapter_key 的枚举项，供 admin 前端把
// adapter_key 渲染成下拉而非手填。
//
// IsDefault 标记「与协议同名的忠实透传 adapter」——创建 channel 时 adapter_key 留空即默认取它
// （见 Create 注释）。
type AdapterKeyOption struct {
	Protocol   string
	AdapterKey string
	IsDefault  bool
}

// Channel 是 admin 视角的 channel 业务事实，含明文上游凭据（产品决策：渠道凭据明文，管理端可查看/复制）。
//
// ProviderName 由 enrichProviderName 在单条读取/写入后补全；列表场景由 JOIN 直接带出。
type Channel struct {
	ID           int64
	ProviderID   int64
	ProviderName string
	// ProviderEndpointID 是 channel 绑定的 ProviderEndpoint（唯一 API Root/公共故障域）。
	ProviderEndpointID int64
	// ProviderEndpointName / ProviderEndpointStatus / BaseURL 为只读展示，来源于所绑定 Endpoint。
	ProviderEndpointName            string
	ProviderEndpointStatus          string
	ProviderEndpointBaseURLRevision int64
	ProviderEndpointStatusRevision  int64
	// ConfigRevision / AdmissionLimitsRevision 为只读返回（P4 §4.4）。
	ConfigRevision          int64
	AdmissionLimitsRevision int64
	// RuntimeSyncPending 表示 PostgreSQL 已保存，但 revision 对应的 Redis control 尚未确认 active。
	RuntimeSyncPending bool
	Name               string
	Protocol           string
	AdapterKey         string
	BaseURL            string
	Credential         string
	Status             string
	Priority           int32
	TimeoutMs          *int32
	// RPMLimit/TPMLimit/RPDLimit 是渠道级限流上限（P2-8）：nil=继承渠道默认限流，0=不限，>0=具体上限。
	RPMLimit *int64
	TPMLimit *int64
	RPDLimit *int64
	// ConcurrencyLimit 是渠道在途并发上限（DEC-029）：nil=继承并发默认 channel_limit，0=不限，>0=具体上限。
	ConcurrencyLimit *int64
	// BillsOnDisconnect 标记上游「断开仍计费」（DESIGN-bill-on-cancel 阶段一）：
	// true 时失败/取消路径会记平台成本敞口，纯观测不影响路由与客户计费。
	BillsOnDisconnect bool
	CreatedAt         time.Time
	UpdatedAt         time.Time
	ArchivedAt        *time.Time
	// LastTested* 是最近一次主动检测结果（渠道检测，阶段一）：全 nil 表示从未检测。
	// 仅由检测端点写入，不参与路由/计费，也不改渠道启停状态。
	LastTestedAt      *time.Time
	LastTestOK        *bool
	LastTestLatencyMs *int32
	LastTestError     *string
}

// AdmissionLimits 是 Channel 四维限额的完整覆盖值。三维 rate 的 nil=继承渠道默认限流，
// concurrency 的 nil=继承并发默认 channel_limit；0=不限，正数=明确上限。
type AdmissionLimits struct {
	RPM         *int64
	RPD         *int64
	TPM         *int64
	Concurrency *int64
}

type admissionLimitsPayload struct {
	RPM         *int64 `json:"rpm"`
	RPD         *int64 `json:"rpd"`
	TPM         *int64 `json:"tpm"`
	Concurrency *int64 `json:"concurrency"`
}

// CanonicalAdmissionLimitsPayload 返回 Redis admission control 使用的规范化完整 JSON。
// 字段固定存在，因此 nil 会稳定编码为 null（继承），不会与 0（不限）混淆。
func CanonicalAdmissionLimitsPayload(limits AdmissionLimits) (string, error) {
	if err := validateChannelRateLimits(limits.RPM, limits.TPM, limits.RPD, limits.Concurrency); err != nil {
		return "", err
	}
	raw, err := json.Marshal(admissionLimitsPayload{
		RPM: limits.RPM, RPD: limits.RPD, TPM: limits.TPM, Concurrency: limits.Concurrency,
	})
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// CanonicalAdmissionLimitsPayloadFromChannel 从 PostgreSQL Channel 事实还原同一规范 payload，
// 供启动恢复和 runtime-control reconciler 共用，避免各处猜测 JSON schema。
func CanonicalAdmissionLimitsPayloadFromChannel(row sqlc.Channel) (string, error) {
	return CanonicalAdmissionLimitsPayload(admissionLimitsFromChannel(row))
}

// ListParams 是分页/过滤列出 channel 的入参；ProviderID<=0、Status/Query 为空表示不过滤。
type ListParams struct {
	ProviderID int64
	Status     string
	Query      string
	Limit      int32
	Offset     int32
}

// ListResult 是分页列表结果：当前页条目 + 过滤后总数。
type ListResult struct {
	Items []Channel
	Total int64
}

// CreateInput 是创建 channel 的入参；Credential 为明文上游凭据，落库前加密。
//
// AdapterKey 可选：留空时默认为 Protocol 同名的忠实透传 adapter（见 Create 注释）。
type CreateInput struct {
	ProviderID         int64
	ProviderEndpointID int64
	Name               string
	Protocol           string
	AdapterKey         string
	Credential         string
	Status             string
	Priority           int32
	TimeoutMs          *int32
	// RateLimitsProvided=true 时按 RPM/TPM/RPD/并发设置渠道级限流；rate 的 nil 继承渠道默认限流，
	// concurrency 的 nil 继承并发默认 channel_limit，0=不限，>0=具体上限。
	RateLimitsProvided bool
	RPMLimit           *int64
	TPMLimit           *int64
	RPDLimit           *int64
	ConcurrencyLimit   *int64
	// BillsOnDisconnect 非 nil 时设置「断开仍计费」标记（DESIGN-bill-on-cancel 阶段一）。
	BillsOnDisconnect *bool
}

// UpdateInput 是更新 channel 的入参；protocol、adapter_key 与凭据不在此修改。
type UpdateInput struct {
	ID                 int64
	Name               string
	ProviderEndpointID int64
	Status             string
	Priority           int32
	TimeoutMs          *int32
	// RateLimitsProvided=true 时按 RPM/TPM/RPD/并发 原子设置渠道级限流。
	RateLimitsProvided bool
	RPMLimit           *int64
	TPMLimit           *int64
	RPDLimit           *int64
	ConcurrencyLimit   *int64
	// BillsOnDisconnect 非 nil 时设置「断开仍计费」标记（DESIGN-bill-on-cancel 阶段一）。
	BillsOnDisconnect *bool
}

// RotateCredentialInput 是轮换 channel 上游凭据的入参。
type RotateCredentialInput struct {
	ID         int64
	Credential string
}

type CredentialVerificationState string

const (
	CredentialVerificationPassed          CredentialVerificationState = "passed"
	CredentialVerificationFailed          CredentialVerificationState = "failed"
	CredentialVerificationStale           CredentialVerificationState = "stale"
	CredentialVerificationExecutionFailed CredentialVerificationState = "execution_failed"
	CredentialVerificationNotRequired     CredentialVerificationState = "not_required"
)

// CredentialProbeResult 是凭据轮换响应中可安全返回的主动检测事实，不含 credential。
type CredentialProbeResult struct {
	Success       bool
	LatencyMs     int64
	TestedModel   string
	HTTPStatus    int
	ErrorCode     string
	Message       string
	UpstreamError string
	TestedAt      time.Time
}

type CredentialVerification struct {
	State                         CredentialVerificationState
	TestedEndpointBaseURLRevision *int64
	TestedEndpointStatusRevision  *int64
	TestedConfigRevision          *int64
	StateChangeApplied            bool
	CredentialValidAfter          bool
	Result                        *CredentialProbeResult
}

// RotateCredentialResult 明确区分「凭据已保存」与「即时检测是否通过」。
type RotateCredentialResult struct {
	CredentialSaved       bool
	CredentialChanged     bool
	SavedConfigRevision   int64
	Verification          CredentialVerification
	CurrentConfigRevision int64
}

// CredentialRotator 由 channeltest application service 实现，拥有原子保存、真实探测和 revision CAS。
type CredentialRotator interface {
	RotateCredentialAndTest(ctx context.Context, in RotateCredentialInput) (RotateCredentialResult, error)
}

// Service 编排 channel 管理读写。
type Service struct {
	store             Store
	registry          AdapterRegistry
	credentialRotator CredentialRotator
	runtimePublisher  RuntimeControlPublisher
	runtimeStore      AdmissionControlStore
}

// NewService 创建 channel 管理服务。
func NewService(store Store, registry AdapterRegistry) *Service {
	return &Service{store: store, registry: registry}
}

// WithRuntimeControl 注入 Channel 四维限额的 durable publisher 与 Redis control store。
// 生产 bootstrap 必须注入；缺失时限额真变化会 fail-closed，创建结果会标记 runtime_sync_pending。
func (s *Service) WithRuntimeControl(publisher RuntimeControlPublisher, runtimeStore AdmissionControlStore) *Service {
	if s != nil {
		s.runtimePublisher = publisher
		s.runtimeStore = runtimeStore
	}
	return s
}

// WithCredentialRotator 接入凭据保存 + 即时检测编排；生产 bootstrap 必须注入。
func (s *Service) WithCredentialRotator(rotator CredentialRotator) *Service {
	if s != nil {
		s.credentialRotator = rotator
	}
	return s
}

// AdapterKeyOptions 列出当前进程在受支持协议族下注册的全部 adapter_key，
// 供 admin 前端把 adapter_key 渲染成下拉枚举（替代手填，避免写入未注册的不可运行绑定）。
func (s *Service) AdapterKeyOptions() []AdapterKeyOption {
	options := make([]AdapterKeyOption, 0)
	for _, protocol := range []string{ProtocolOpenAI, ProtocolAnthropic} {
		for _, key := range s.registry.AdapterKeys(protocol) {
			options = append(options, AdapterKeyOption{
				Protocol:   protocol,
				AdapterKey: key,
				IsDefault:  key == protocol,
			})
		}
	}
	return options
}

// List 按 params 过滤分页列出 channel（连带 provider 名称），并返回过滤后的总数。
func (s *Service) List(ctx context.Context, params ListParams) (ListResult, error) {
	providerID := int8Param(params.ProviderID)
	status := textParam(params.Status)
	q := textParam(params.Query)

	rows, err := s.store.ListChannelsPage(ctx, sqlc.ListChannelsPageParams{
		ProviderID: providerID,
		Status:     status,
		Q:          q,
		PageLimit:  params.Limit,
		PageOffset: params.Offset,
	})
	if err != nil {
		return ListResult{}, storeFailed(err, "list channels")
	}

	total, err := s.store.CountChannels(ctx, sqlc.CountChannelsParams{
		ProviderID: providerID,
		Status:     status,
		Q:          q,
	})
	if err != nil {
		return ListResult{}, storeFailed(err, "count channels")
	}

	items := make([]Channel, 0, len(rows))
	for _, row := range rows {
		item := toChannelRow(row)
		payload, payloadErr := CanonicalAdmissionLimitsPayload(AdmissionLimits{
			RPM: item.RPMLimit, RPD: item.RPDLimit, TPM: item.TPMLimit, Concurrency: item.ConcurrencyLimit,
		})
		item.RuntimeSyncPending = payloadErr != nil || !s.admissionControlIsActive(
			ctx, item.ID, item.AdmissionLimitsRevision, payload,
		)
		items = append(items, item)
	}

	return ListResult{Items: items, Total: total}, nil
}

// Get 按 id 读取单个 channel。
func (s *Service) Get(ctx context.Context, id int64) (Channel, error) {
	if id <= 0 {
		return Channel{}, invalidArgument("id", "channel id must be positive")
	}

	row, err := s.store.GetChannel(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Channel{}, notFound("channel not found")
		}
		return Channel{}, storeFailed(err, "get channel")
	}

	ch := toChannel(row)
	payload, payloadErr := CanonicalAdmissionLimitsPayloadFromChannel(row)
	ch.RuntimeSyncPending = payloadErr != nil || !s.admissionControlIsActive(
		ctx, row.ID, row.AdmissionLimitsRevision, payload,
	)
	return s.enrichProviderName(ctx, ch)
}

// Create 创建 channel：校验复合键在 registry 注册、provider 存在，再加密凭据落库。
func (s *Service) Create(ctx context.Context, in CreateInput) (Channel, error) {
	name := strings.TrimSpace(in.Name)
	protocol := strings.TrimSpace(in.Protocol)
	adapterKey := strings.TrimSpace(in.AdapterKey)
	status := strings.TrimSpace(in.Status)

	if in.ProviderID <= 0 {
		return Channel{}, invalidArgument("provider_id", "provider_id must be positive")
	}
	if in.ProviderEndpointID <= 0 {
		return Channel{}, invalidArgument("provider_endpoint_id", "provider_endpoint_id must be positive")
	}
	if name == "" {
		return Channel{}, invalidArgument("name", "name is required")
	}
	if err := validateProtocol(protocol); err != nil {
		return Channel{}, err
	}
	// adapter_key 可选：留空默认为该协议的忠实透传 adapter。忠实 adapter 的注册键与协议同名
	// （openai→"openai"、anthropic→"anthropic"），故普通 OpenAI/Anthropic 兼容上游免填即可；
	// 仅需特殊方言/Drop 策略（如直连 DeepSeek 原厂）的上游才显式指定 adapter_key。
	if adapterKey == "" {
		adapterKey = protocol
	}
	if err := validateStatus(status); err != nil {
		return Channel{}, err
	}
	if in.Priority < 0 {
		return Channel{}, invalidArgument("priority", "priority must be >= 0")
	}
	if err := validateTimeout(in.TimeoutMs); err != nil {
		return Channel{}, err
	}
	if in.RateLimitsProvided {
		if err := validateChannelRateLimits(in.RPMLimit, in.TPMLimit, in.RPDLimit, in.ConcurrencyLimit); err != nil {
			return Channel{}, err
		}
	}
	limits := AdmissionLimits{}
	if in.RateLimitsProvided {
		limits = AdmissionLimits{
			RPM: in.RPMLimit, RPD: in.RPDLimit, TPM: in.TPMLimit, Concurrency: in.ConcurrencyLimit,
		}
	}
	admissionPayload, err := CanonicalAdmissionLimitsPayload(limits)
	if err != nil {
		return Channel{}, err
	}
	if strings.TrimSpace(in.Credential) == "" {
		return Channel{}, invalidArgument("credential", "credential is required")
	}

	// 关 GAP-6-003：复合键必须被当前进程 adapter registry 支持，避免写入不可运行绑定。
	if !s.registry.HasAny(protocol, adapterKey) {
		return Channel{}, failure.New(
			failure.CodeAdminAdapterBindingUnsupported,
			failure.WithMessage("(protocol, adapter_key) is not registered in adapter registry"),
			failure.WithField("protocol", protocol),
			failure.WithField("adapter_key", adapterKey),
		)
	}

	if _, err := s.store.GetProvider(ctx, in.ProviderID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Channel{}, invalidArgument("provider_id", "provider not found")
		}
		return Channel{}, storeFailed(err, "load provider for channel")
	}

	// P4 §4.4：Channel 必须绑定同一 Provider 下的 Endpoint（复合外键在 DB 兜底，这里给出可读错误）。
	if _, err := s.resolveEndpointForProvider(ctx, in.ProviderEndpointID, in.ProviderID); err != nil {
		return Channel{}, err
	}

	billsOnDisconnect := false
	if in.BillsOnDisconnect != nil {
		billsOnDisconnect = *in.BillsOnDisconnect
	}
	row, err := s.store.CreateChannel(ctx, sqlc.CreateChannelParams{
		ProviderID:                in.ProviderID,
		ProviderEndpointID:        in.ProviderEndpointID,
		Name:                      name,
		Protocol:                  protocol,
		AdapterKey:                adapterKey,
		Credential:                strings.TrimSpace(in.Credential),
		Status:                    status,
		Priority:                  in.Priority,
		TimeoutMs:                 timeoutParam(in.TimeoutMs),
		RpmLimit:                  rateLimitParam(limits.RPM),
		TpmLimit:                  rateLimitParam(limits.TPM),
		RpdLimit:                  rateLimitParam(limits.RPD),
		ConcurrencyLimit:          rateLimitParam(limits.Concurrency),
		UpstreamBillsOnDisconnect: billsOnDisconnect,
	})
	if err != nil {
		if isUniqueViolation(err) {
			return Channel{}, conflict("channel name already exists for this provider")
		}
		if isForeignKeyViolation(err) {
			return Channel{}, invalidArgument("provider_id", "provider not found")
		}
		return Channel{}, storeFailed(err, "create channel")
	}

	ch := toChannel(row)
	ch.RuntimeSyncPending = !s.initializeAdmissionControl(ctx, row, admissionPayload)
	return s.enrichProviderName(ctx, ch)
}

// Update 更新 channel 的展示名、上游地址、状态、优先级与超时。
func (s *Service) Update(ctx context.Context, in UpdateInput) (Channel, error) {
	if in.ID <= 0 {
		return Channel{}, invalidArgument("id", "channel id must be positive")
	}
	name := strings.TrimSpace(in.Name)
	status := strings.TrimSpace(in.Status)

	if name == "" {
		return Channel{}, invalidArgument("name", "name is required")
	}
	if in.ProviderEndpointID <= 0 {
		return Channel{}, invalidArgument("provider_endpoint_id", "provider_endpoint_id must be positive")
	}
	if err := validateStatus(status); err != nil {
		return Channel{}, err
	}
	if in.Priority < 0 {
		return Channel{}, invalidArgument("priority", "priority must be >= 0")
	}
	if err := validateTimeout(in.TimeoutMs); err != nil {
		return Channel{}, err
	}

	// P4 §4.4：换绑 Endpoint 必须仍属于该 channel 的 Provider。
	cur, err := s.store.GetChannel(ctx, in.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Channel{}, notFound("channel not found")
		}
		return Channel{}, storeFailed(err, "get channel for update")
	}
	if _, err := s.resolveEndpointForProvider(ctx, in.ProviderEndpointID, cur.ProviderID); err != nil {
		return Channel{}, err
	}
	if in.RateLimitsProvided {
		desiredLimits := AdmissionLimits{
			RPM: in.RPMLimit, RPD: in.RPDLimit, TPM: in.TPMLimit, Concurrency: in.ConcurrencyLimit,
		}
		desiredPayload, payloadErr := CanonicalAdmissionLimitsPayload(desiredLimits)
		if payloadErr != nil {
			return Channel{}, payloadErr
		}
		currentPayload, payloadErr := CanonicalAdmissionLimitsPayloadFromChannel(cur)
		if payloadErr != nil {
			return Channel{}, storeFailed(payloadErr, "encode current channel admission limits")
		}
		if currentPayload != desiredPayload {
			return s.updateWithPublishedAdmissionLimits(ctx, in, cur, desiredLimits, desiredPayload)
		}
	}

	row, err := s.store.UpdateChannel(ctx, sqlc.UpdateChannelParams{
		ID:                 in.ID,
		Name:               name,
		ProviderEndpointID: in.ProviderEndpointID,
		Status:             status,
		Priority:           in.Priority,
		TimeoutMs:          timeoutParam(in.TimeoutMs),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Channel{}, notFound("channel not found")
		}
		if isUniqueViolation(err) {
			return Channel{}, conflict("channel name already exists for this provider")
		}
		return Channel{}, storeFailed(err, "update channel")
	}

	if in.BillsOnDisconnect != nil {
		flagged, err := s.store.SetChannelBillingBehavior(ctx, sqlc.SetChannelBillingBehaviorParams{
			ID:                        in.ID,
			UpstreamBillsOnDisconnect: *in.BillsOnDisconnect,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return Channel{}, notFound("channel not found")
			}
			return Channel{}, storeFailed(err, "set channel billing behavior")
		}
		row = flagged
	}

	ch := toChannel(row)
	if payload, payloadErr := CanonicalAdmissionLimitsPayloadFromChannel(row); payloadErr == nil {
		ch.RuntimeSyncPending = !s.admissionControlIsActive(ctx, row.ID, row.AdmissionLimitsRevision, payload)
	} else {
		ch.RuntimeSyncPending = true
	}
	return s.enrichProviderName(ctx, ch)
}

func (s *Service) updateWithPublishedAdmissionLimits(
	ctx context.Context,
	in UpdateInput,
	current sqlc.Channel,
	limits AdmissionLimits,
	payload string,
) (Channel, error) {
	if s.runtimePublisher == nil || s.runtimeStore == nil {
		return Channel{}, failure.New(
			failure.CodeGatewayBreakerStoreUnavailable,
			failure.WithMessage("channel: admission runtime-control publisher unavailable"),
		)
	}
	token, err := newAdmissionControlToken()
	if err != nil {
		return Channel{}, failure.Wrap(
			failure.CodeConfigInvalid,
			err,
			failure.WithMessage("channel: generate admission runtime-control token"),
		)
	}

	nextRevision := current.AdmissionLimitsRevision + 1
	channelID := current.ID
	var committedRow sqlc.Channel
	publishResult, err := s.runtimePublisher.Publish(ctx, runtimecontrol.PublishRequest{
		Kind:            runtimecontrol.KindChannelAdmissionLimits,
		Target:          s.runtimeStore.ChannelAdmissionControl(channelID),
		Token:           token,
		Payload:         payload,
		CurrentRevision: current.AdmissionLimitsRevision,
		NextRevision:    nextRevision,
		ChannelID:       &channelID,
		BusinessCommit: func(ctx context.Context, tx pgx.Tx) error {
			qtx := sqlc.New(tx)
			row, updateErr := qtx.UpdateChannel(ctx, sqlc.UpdateChannelParams{
				ID:                 in.ID,
				Name:               strings.TrimSpace(in.Name),
				ProviderEndpointID: in.ProviderEndpointID,
				Status:             strings.TrimSpace(in.Status),
				Priority:           in.Priority,
				TimeoutMs:          timeoutParam(in.TimeoutMs),
			})
			if updateErr != nil {
				return channelUpdateError(updateErr)
			}
			row, updateErr = qtx.CommitChannelAdmissionLimitsAtRevision(ctx, sqlc.CommitChannelAdmissionLimitsAtRevisionParams{
				RpmLimit:         rateLimitParam(limits.RPM),
				TpmLimit:         rateLimitParam(limits.TPM),
				RpdLimit:         rateLimitParam(limits.RPD),
				ConcurrencyLimit: rateLimitParam(limits.Concurrency),
				NextRevision:     nextRevision,
				ID:               channelID,
				CurrentRevision:  current.AdmissionLimitsRevision,
			})
			if updateErr != nil {
				if errors.Is(updateErr, pgx.ErrNoRows) {
					return conflict("channel admission limits changed during publish; retry with current state")
				}
				return storeFailed(updateErr, "commit channel admission limits")
			}
			if in.BillsOnDisconnect != nil {
				row, updateErr = qtx.SetChannelBillingBehavior(ctx, sqlc.SetChannelBillingBehaviorParams{
					ID: channelID, UpstreamBillsOnDisconnect: *in.BillsOnDisconnect,
				})
				if updateErr != nil {
					return channelUpdateError(updateErr)
				}
			}
			committedRow = row
			return nil
		},
	})
	if err != nil {
		return Channel{}, err
	}
	if publishResult.State != runtimecontrol.PublishCommitted && publishResult.State != runtimecontrol.PublishRuntimeSyncPending {
		return Channel{}, failure.New(
			failure.CodeConfigInvalid,
			failure.WithMessage("channel: admission runtime-control publish did not commit business state"),
		)
	}

	row := committedRow
	if row.ID == 0 {
		row, err = s.store.GetChannel(ctx, channelID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return Channel{}, notFound("channel not found after admission limits publish")
			}
			return Channel{}, storeFailed(err, "get channel after admission limits publish")
		}
	}
	ch := toChannel(row)
	ch.RuntimeSyncPending = publishResult.State == runtimecontrol.PublishRuntimeSyncPending
	return s.enrichProviderName(ctx, ch)
}

func channelUpdateError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return notFound("channel not found")
	}
	if isUniqueViolation(err) {
		return conflict("channel name already exists for this provider")
	}
	return storeFailed(err, "update channel")
}

func (s *Service) initializeAdmissionControl(ctx context.Context, row sqlc.Channel, payload string) bool {
	if s.runtimeStore == nil || row.AdmissionLimitsRevision <= 0 {
		return false
	}
	target := s.runtimeStore.ChannelAdmissionControl(row.ID)
	if _, err := s.runtimeStore.RestoreMissingControl(ctx, target, row.AdmissionLimitsRevision, payload); err != nil {
		return false
	}
	return s.admissionControlIsActive(ctx, row.ID, row.AdmissionLimitsRevision, payload)
}

func (s *Service) admissionControlIsActive(ctx context.Context, channelID, revision int64, payload string) bool {
	if s.runtimeStore == nil {
		return false
	}
	snapshot, err := s.runtimeStore.ReadControl(
		ctx,
		s.runtimeStore.ChannelAdmissionControl(channelID),
		revision,
	)
	return err == nil &&
		snapshot.SyncState == "active" &&
		snapshot.ActiveRevision == revision &&
		snapshot.PendingRevision == 0 &&
		snapshot.ActivePayload == payload
}

// RotateCredential 原子保存 channel 上游凭据并同步执行 revision-safe 主动检测。
func (s *Service) RotateCredential(ctx context.Context, in RotateCredentialInput) (RotateCredentialResult, error) {
	if in.ID <= 0 {
		return RotateCredentialResult{}, invalidArgument("id", "channel id must be positive")
	}
	in.Credential = strings.TrimSpace(in.Credential)
	if in.Credential == "" {
		return RotateCredentialResult{}, invalidArgument("credential", "credential is required")
	}
	if s.credentialRotator == nil {
		return RotateCredentialResult{}, storeFailed(errors.New("credential rotator is unavailable"), "rotate channel credential")
	}
	return s.credentialRotator.RotateCredentialAndTest(ctx, in)
}

// Delete 物理删除 channel，用于清理录错的脏数据，并级联清理它自身的配置子表
// （模型绑定、成本价、能力收紧）。channel 名随之释放，可在同一 provider 下重新录入同名。
//
// 一旦 channel 或其子配置已被请求/账务历史（NO ACTION 外键）引用，DB 拒绝删除（23503），
// 降级为 conflict，提示改用停用——保住计费/审计链路。
func (s *Service) Delete(ctx context.Context, id int64) error {
	if id <= 0 {
		return invalidArgument("id", "channel id must be positive")
	}

	// 硬删闸门（D-4）：只允许删除已归档渠道。
	cur, err := s.store.GetChannel(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound("channel not found")
		}
		return storeFailed(err, "get channel")
	}
	if cur.Status != StatusArchived {
		return conflict("channel must be archived before deletion")
	}

	affected, err := s.store.DeleteChannelCascade(ctx, id)
	if err != nil {
		if isForeignKeyViolation(err) {
			return conflict("channel is referenced by request/billing history; keep it archived instead of deleting")
		}
		return storeFailed(err, "delete channel")
	}
	if affected == 0 {
		return notFound("channel not found")
	}

	return nil
}

// Archive 归档渠道：从所有线路候选池移除、置 archived、释放渠道名（追加 __archived_<id> 后缀）。
// 幂等：已归档返回 not_found（0 行）。
func (s *Service) Archive(ctx context.Context, id int64, replacementChannelID *int64) error {
	if id <= 0 {
		return invalidArgument("id", "channel id must be positive")
	}
	if replacementChannelID != nil {
		if *replacementChannelID <= 0 || *replacementChannelID == id {
			return invalidArgument("replacement_channel_id", "replacement channel must be a different positive channel id")
		}
		replacement, err := s.store.GetChannel(ctx, *replacementChannelID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return invalidArgument("replacement_channel_id", "replacement channel not found")
			}
			return storeFailed(err, "get replacement channel")
		}
		if replacement.Status != StatusEnabled || !replacement.CredentialValid || replacement.Credential == "" {
			return conflict("replacement channel must be enabled, credential-valid, and fully configured")
		}
		provider, err := s.store.GetProvider(ctx, replacement.ProviderID)
		if err != nil {
			return storeFailed(err, "get replacement channel provider")
		}
		if provider.Status != StatusEnabled {
			return conflict("replacement channel provider must be enabled")
		}
		affected, err := s.store.ArchiveChannelWithReplacement(ctx, sqlc.ArchiveChannelWithReplacementParams{
			ID: id, ReplacementChannelID: *replacementChannelID,
		})
		if err != nil {
			return storeFailed(err, "replace and archive channel")
		}
		if affected == 0 {
			return conflict("channel archive could not commit because the target or replacement changed")
		}
		return nil
	}
	affectedRoutes, err := s.store.ListEnabledRoutesEmptiedByChannel(ctx, id)
	if err != nil {
		return storeFailed(err, "check channel archive route impact")
	}
	if len(affectedRoutes) > 0 {
		return conflict(fmt.Sprintf(
			"archiving channel would empty enabled route %q (%d); replace the channel or disable the route first",
			affectedRoutes[0].Name, affectedRoutes[0].ID,
		))
	}
	affected, err := s.store.ArchiveChannelCascade(ctx, id)
	if err != nil {
		return storeFailed(err, "archive channel")
	}
	if affected == 0 {
		return notFound("channel not found or already archived")
	}
	return nil
}

// Restore 取消归档渠道：archived → disabled。护栏：所属 provider 仍归档时拦截（先恢复服务商）。
// 名字保持归档时的后缀名；不自动重加线路池（需手动）。
func (s *Service) Restore(ctx context.Context, id int64) error {
	if id <= 0 {
		return invalidArgument("id", "channel id must be positive")
	}

	cur, err := s.store.GetChannel(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound("channel not found")
		}
		return storeFailed(err, "get channel")
	}
	// 护栏：不允许在归档的服务商下恢复渠道（避免归档父级下出现半活子级）。
	provider, err := s.store.GetProvider(ctx, cur.ProviderID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return storeFailed(err, "get provider for channel restore")
		}
	} else if provider.Status == StatusArchived {
		return conflict("provider is archived; restore the provider first")
	}

	affected, err := s.store.RestoreChannel(ctx, id)
	if err != nil {
		return storeFailed(err, "restore channel")
	}
	if affected == 0 {
		return notFound("channel not found or not archived")
	}
	return nil
}

func toChannel(c sqlc.Channel) Channel {
	return Channel{
		ID:                      c.ID,
		ProviderID:              c.ProviderID,
		ProviderEndpointID:      c.ProviderEndpointID,
		ConfigRevision:          c.ConfigRevision,
		AdmissionLimitsRevision: c.AdmissionLimitsRevision,
		Name:                    c.Name,
		Protocol:                c.Protocol,
		AdapterKey:              c.AdapterKey,
		Credential:              c.Credential,
		Status:                  c.Status,
		Priority:                c.Priority,
		TimeoutMs:               timeoutResult(c.TimeoutMs),
		RPMLimit:                rateLimitResult(c.RpmLimit),
		TPMLimit:                rateLimitResult(c.TpmLimit),
		RPDLimit:                rateLimitResult(c.RpdLimit),
		ConcurrencyLimit:        rateLimitResult(c.ConcurrencyLimit),
		BillsOnDisconnect:       c.UpstreamBillsOnDisconnect,
		CreatedAt:               c.CreatedAt.Time,
		UpdatedAt:               c.UpdatedAt.Time,
		ArchivedAt:              timestampResult(c.ArchivedAt),

		LastTestedAt:      timestampResult(c.LastTestedAt),
		LastTestOK:        boolResult(c.LastTestOk),
		LastTestLatencyMs: timeoutResult(c.LastTestLatencyMs),
		LastTestError:     textResult(c.LastTestError),
	}
}

// resolveEndpointForProvider 校验 Endpoint 存在且归属指定 Provider（复合外键的可读前置校验）。
func (s *Service) resolveEndpointForProvider(ctx context.Context, endpointID, providerID int64) (sqlc.ProviderEndpoint, error) {
	ep, err := s.store.GetProviderEndpoint(ctx, endpointID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return sqlc.ProviderEndpoint{}, invalidArgument("provider_endpoint_id", "provider endpoint not found")
		}
		return sqlc.ProviderEndpoint{}, storeFailed(err, "load provider endpoint for channel")
	}
	if ep.ProviderID != providerID {
		return sqlc.ProviderEndpoint{}, invalidArgument("provider_endpoint_id", "provider endpoint does not belong to the channel provider")
	}
	return ep, nil
}

func (s *Service) enrichProviderName(ctx context.Context, ch Channel) (Channel, error) {
	if ch.ProviderID > 0 {
		provider, err := s.store.GetProvider(ctx, ch.ProviderID)
		if err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				return Channel{}, storeFailed(err, "load provider for channel")
			}
		} else {
			ch.ProviderName = provider.Name
		}
	}
	// 单条读取时从所绑定 Endpoint 只读带出 base_url/name/status（列表由 JOIN 直接带出）。
	if ch.ProviderEndpointID > 0 && ch.BaseURL == "" {
		ep, err := s.store.GetProviderEndpoint(ctx, ch.ProviderEndpointID)
		if err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				return Channel{}, storeFailed(err, "load provider endpoint for channel")
			}
		} else {
			ch.BaseURL = ep.BaseUrl
			ch.ProviderEndpointName = ep.Name
			ch.ProviderEndpointStatus = ep.Status
			ch.ProviderEndpointBaseURLRevision = ep.BaseUrlRevision
			ch.ProviderEndpointStatusRevision = ep.StatusRevision
		}
	}
	return ch, nil
}

// toChannelRow 映射分页列表行，额外带出 JOIN 出的 provider 名称。
func toChannelRow(c sqlc.ListChannelsPageRow) Channel {
	return Channel{
		ID:                      c.ID,
		ProviderID:              c.ProviderID,
		ProviderName:            c.ProviderName,
		ProviderEndpointID:      c.ProviderEndpointID,
		ProviderEndpointName:    c.ProviderEndpointName,
		ProviderEndpointStatus:  c.ProviderEndpointStatus,
		ConfigRevision:          c.ConfigRevision,
		AdmissionLimitsRevision: c.AdmissionLimitsRevision,
		Name:                    c.Name,
		Protocol:                c.Protocol,
		AdapterKey:              c.AdapterKey,
		BaseURL:                 c.BaseUrl,
		Credential:              c.Credential,
		Status:                  c.Status,
		Priority:                c.Priority,
		TimeoutMs:               timeoutResult(c.TimeoutMs),
		RPMLimit:                rateLimitResult(c.RpmLimit),
		TPMLimit:                rateLimitResult(c.TpmLimit),
		RPDLimit:                rateLimitResult(c.RpdLimit),
		ConcurrencyLimit:        rateLimitResult(c.ConcurrencyLimit),
		BillsOnDisconnect:       c.UpstreamBillsOnDisconnect,
		CreatedAt:               c.CreatedAt.Time,
		UpdatedAt:               c.UpdatedAt.Time,

		LastTestedAt:      timestampResult(c.LastTestedAt),
		LastTestOK:        boolResult(c.LastTestOk),
		LastTestLatencyMs: timeoutResult(c.LastTestLatencyMs),
		LastTestError:     textResult(c.LastTestError),
	}
}

// rateLimitParam 把 *int64 转成可空 pgtype.Int4（nil=NULL 继承对应系统默认；含 0=显式不限）。
func rateLimitParam(v *int64) pgtype.Int4 {
	if v == nil {
		return pgtype.Int4{Valid: false}
	}
	return pgtype.Int4{Int32: int32(*v), Valid: true}
}

// rateLimitResult 把可空 pgtype.Int4 转成 *int64（nil=继承对应系统默认）。
func rateLimitResult(v pgtype.Int4) *int64 {
	if !v.Valid {
		return nil
	}
	out := int64(v.Int32)
	return &out
}

func admissionLimitsFromChannel(row sqlc.Channel) AdmissionLimits {
	return AdmissionLimits{
		RPM:         rateLimitResult(row.RpmLimit),
		RPD:         rateLimitResult(row.RpdLimit),
		TPM:         rateLimitResult(row.TpmLimit),
		Concurrency: rateLimitResult(row.ConcurrencyLimit),
	}
}

func newAdmissionControlToken() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "rctl_channel_" + hex.EncodeToString(raw[:]), nil
}

// validateChannelRateLimits 校验渠道级限流非负（限流上限不能为负数）。
func validateChannelRateLimits(rpm, tpm, rpd, concurrency *int64) error {
	for field, v := range map[string]*int64{"rpm_limit": rpm, "tpm_limit": tpm, "rpd_limit": rpd, "concurrency_limit": concurrency} {
		if v != nil && *v < 0 {
			return invalidArgument(field, "rate limit must be a non-negative integer (0 means unlimited)")
		}
	}
	return nil
}

// textParam 把空串转成 NULL（不过滤），非空转成有值 pgtype.Text。
func textParam(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}

// int8Param 把非正数转成 NULL（不过滤），正数转成有值 pgtype.Int8。
func int8Param(id int64) pgtype.Int8 {
	if id <= 0 {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: id, Valid: true}
}

func validateProtocol(protocol string) error {
	switch protocol {
	case ProtocolOpenAI, ProtocolAnthropic:
		return nil
	default:
		return invalidArgument("protocol", fmt.Sprintf("protocol must be %q or %q", ProtocolOpenAI, ProtocolAnthropic))
	}
}

func validateStatus(status string) error {
	switch status {
	case StatusEnabled, StatusDisabled:
		return nil
	default:
		return invalidArgument("status", fmt.Sprintf("status must be %q or %q", StatusEnabled, StatusDisabled))
	}
}

func validateBaseURL(raw string) error {
	if raw == "" {
		return invalidArgument("base_url", "base_url is required")
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return invalidArgument("base_url", "base_url must be a valid http(s) URL")
	}
	return nil
}

func validateTimeout(ms *int32) error {
	if ms != nil && *ms <= 0 {
		return invalidArgument("timeout_ms", "timeout_ms must be > 0 when set")
	}
	return nil
}

func timeoutParam(ms *int32) pgtype.Int4 {
	if ms == nil {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: *ms, Valid: true}
}

func timeoutResult(v pgtype.Int4) *int32 {
	if !v.Valid {
		return nil
	}
	ms := v.Int32
	return &ms
}

// timestampResult 把可空 pgtype.Timestamptz 转成 *time.Time（nil=未设置）。
func timestampResult(v pgtype.Timestamptz) *time.Time {
	if !v.Valid {
		return nil
	}
	t := v.Time
	return &t
}

// boolResult 把可空 pgtype.Bool 转成 *bool（nil=未设置）。
func boolResult(v pgtype.Bool) *bool {
	if !v.Valid {
		return nil
	}
	b := v.Bool
	return &b
}

// textResult 把可空 pgtype.Text 转成 *string（nil=未设置；空串也视为未设置以贴合“无错误”语义）。
func textResult(v pgtype.Text) *string {
	if !v.Valid || v.String == "" {
		return nil
	}
	s := v.String
	return &s
}

func invalidArgument(field, message string) error {
	return failure.New(
		failure.CodeAdminInvalidArgument,
		failure.WithMessage(message),
		failure.WithField("field", field),
	)
}

func notFound(message string) error {
	return failure.New(failure.CodeAdminNotFound, failure.WithMessage(message))
}

func conflict(message string) error {
	return failure.New(failure.CodeAdminConflict, failure.WithMessage(message))
}

func storeFailed(cause error, message string) error {
	return failure.Wrap(failure.CodeAdminStoreFailed, cause, failure.WithMessage(message))
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}
