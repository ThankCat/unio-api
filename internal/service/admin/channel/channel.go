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
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/core/runtimecontrol"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/admin/supply"
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
	ListChannelsPage(ctx context.Context, arg sqlc.ListChannelsPageParams) ([]sqlc.ListChannelsPageRow, error)
	CountChannels(ctx context.Context, arg sqlc.CountChannelsParams) (int64, error)
	GetChannel(ctx context.Context, id int64) (sqlc.Channel, error)
	CreateChannel(ctx context.Context, arg sqlc.CreateChannelParams) (sqlc.Channel, error)
	UpdateChannel(ctx context.Context, arg sqlc.UpdateChannelParams) (sqlc.Channel, error)
	DeleteChannelCascade(ctx context.Context, id int64) (int64, error)
	ArchiveChannel(ctx context.Context, id int64) (int64, error)
	ListRoutesReferencingChannel(ctx context.Context, channelID int64) ([]sqlc.ListRoutesReferencingChannelRow, error)
	CountEnabledBindingsByChannel(ctx context.Context, channelID int64) (int64, error)
	RestoreChannel(ctx context.Context, id int64) (int64, error)
}

// TxBeginner 提供事务能力（由 pgxpool 满足），用于 Channel 实体停用时的
// 结构支撑串行化与 Offering 联动（ADR-0018）。
type TxBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// RuntimeControlPublisher 是 Channel 并发容量的 durable publisher。
type RuntimeControlPublisher interface {
	Publish(ctx context.Context, req runtimecontrol.PublishRequest) (runtimecontrol.PublishResult, error)
}

// CapacityControlStore 提供 Channel capacity control 的定位、初始化与只读核对能力。
type CapacityControlStore interface {
	ChannelCapacityControl(channelID int64) breakerstore.ControlTarget
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
	ID                     int64
	ProviderID             int64
	ProviderName           string
	OriginRevision         int64
	ProviderStatusRevision int64
	// ConfigRevision / CapacityRevision 为只读返回。
	ConfigRevision   int64
	CapacityRevision int64
	// RuntimeSyncPending 表示 PostgreSQL 已保存，但 revision 对应的 Redis control 尚未确认 active。
	RuntimeSyncPending  bool
	Name                string
	Protocol            string
	AdapterKey          string
	Origin              string
	Credential          string
	Status              string
	Priority            int32
	ResponseTimeoutMs   *int32
	FirstTokenTimeoutMs *int32
	StickyEnabled       *bool
	StickyTTLms         *int64
	// ConcurrencyLimit 是渠道在途并发上限（DEC-029）：nil=继承并发默认 channel_limit，0=不限，>0=具体上限。
	ConcurrencyLimit *int64
	CreatedAt        time.Time
	UpdatedAt        time.Time
	ArchivedAt       *time.Time
	// LastTested* 是最近一次主动检测结果（渠道检测，阶段一）：全 nil 表示从未检测。
	// 仅由检测上游源站写入，不参与路由/计费，也不改渠道启停状态。
	LastTestedAt      *time.Time
	LastTestOK        *bool
	LastTestLatencyMs *int32
	LastTestError     *string
}

// ChannelCapacity 是 Channel 并发容量的完整覆盖值。
// nil=继承全局 channel_limit；0=不限；正数=明确上限。
type ChannelCapacity struct {
	Concurrency *int64
}

type channelCapacityPayload struct {
	Concurrency *int64 `json:"concurrency"`
}

// CanonicalCapacityPayload 返回 Redis capacity control 使用的规范化完整 JSON。
// 字段固定存在，因此 nil 会稳定编码为 null（继承），不会与 0（不限）混淆。
func CanonicalCapacityPayload(capacity ChannelCapacity) (string, error) {
	if err := validateChannelCapacity(capacity.Concurrency); err != nil {
		return "", err
	}
	raw, err := json.Marshal(channelCapacityPayload{Concurrency: capacity.Concurrency})
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// CanonicalCapacityPayloadFromChannel 从 PostgreSQL Channel 事实还原同一规范 payload，
// 供启动恢复和 runtime-control reconciler 共用，避免各处猜测 JSON schema。
func CanonicalCapacityPayloadFromChannel(row sqlc.Channel) (string, error) {
	return CanonicalCapacityPayload(channelCapacityFromChannel(row))
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
	ProviderID          int64
	Name                string
	Protocol            string
	AdapterKey          string
	Credential          string
	Status              string
	Priority            int32
	ResponseTimeoutMs   *int32
	FirstTokenTimeoutMs *int32
	StickyEnabled       *bool
	StickyTTLms         *int64
	ConcurrencyLimit    *int64
}

// UpdateInput 是更新 channel 的入参；protocol、adapter_key 与凭据不在此修改。
type UpdateInput struct {
	ID                  int64
	Name                string
	ProviderID          int64
	Status              string
	Priority            int32
	ResponseTimeoutMs   *int32
	FirstTokenTimeoutMs *int32
	StickyEnabled       *bool
	StickyTTLms         *int64
	// CapacityProvided 区分「字段缺省=保持不变」与「显式 null=继承全局默认」。
	CapacityProvided bool
	ConcurrencyLimit *int64
	// Confirmation 是停用 Channel 触发 Offering 联动时的二次确认参数（ADR-0018）。
	Confirmation supply.Confirmation
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
	State                        CredentialVerificationState
	TestedOriginRevision         *int64
	TestedProviderStatusRevision *int64
	TestedConfigRevision         *int64
	StateChangeApplied           bool
	CredentialValidAfter         bool
	Result                       *CredentialProbeResult
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
	runtimeStore      CapacityControlStore
	db                TxBeginner
	queries           *sqlc.Queries
}

// NewService 创建 channel 管理服务。
func NewService(store Store, registry AdapterRegistry) *Service {
	return &Service{store: store, registry: registry}
}

// WithSupplyLinkage 注入 Channel 实体停用联动所需的事务能力（ADR-0018）；
// 生产 bootstrap 必须注入，缺失时停用转换 fail-closed。
func (s *Service) WithSupplyLinkage(db TxBeginner, queries *sqlc.Queries) *Service {
	if s != nil {
		s.db = db
		s.queries = queries
	}
	return s
}

// WithRuntimeControl 注入 Channel 并发容量的 durable publisher 与 Redis control store。
// 生产 bootstrap 必须注入；缺失时限额真变化会 fail-closed，创建结果会标记 runtime_sync_pending。
func (s *Service) WithRuntimeControl(publisher RuntimeControlPublisher, runtimeStore CapacityControlStore) *Service {
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
		payload, payloadErr := CanonicalCapacityPayload(ChannelCapacity{Concurrency: item.ConcurrencyLimit})
		item.RuntimeSyncPending = payloadErr != nil || !s.capacityControlIsActive(
			ctx, item.ID, item.CapacityRevision, payload,
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
	payload, payloadErr := CanonicalCapacityPayloadFromChannel(row)
	ch.RuntimeSyncPending = payloadErr != nil || !s.capacityControlIsActive(
		ctx, row.ID, row.CapacityRevision, payload,
	)
	return s.enrichProviderName(ctx, ch)
}

// Create 创建 channel：校验复合键在 registry 注册、provider 与请求状态合法，再保存凭据和配置。
func (s *Service) Create(ctx context.Context, in CreateInput) (Channel, error) {
	name := strings.TrimSpace(in.Name)
	protocol := strings.TrimSpace(in.Protocol)
	adapterKey := strings.TrimSpace(in.AdapterKey)
	status := strings.TrimSpace(in.Status)

	if in.ProviderID <= 0 {
		return Channel{}, invalidArgument("provider_id", "provider_id must be positive")
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
	if err := validatePriority(in.Priority); err != nil {
		return Channel{}, err
	}
	if err := validateTimeout("response_timeout_ms", in.ResponseTimeoutMs); err != nil {
		return Channel{}, err
	}
	if err := validateTimeout("first_token_timeout_ms", in.FirstTokenTimeoutMs); err != nil {
		return Channel{}, err
	}
	if err := validateStickyPolicy(in.StickyEnabled, in.StickyTTLms); err != nil {
		return Channel{}, err
	}
	capacity := ChannelCapacity{Concurrency: in.ConcurrencyLimit}
	capacityPayload, err := CanonicalCapacityPayload(capacity)
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

	provider, err := s.store.GetProvider(ctx, in.ProviderID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Channel{}, invalidArgument("provider_id", "provider not found")
		}
		return Channel{}, storeFailed(err, "load provider for channel")
	}

	if provider.Status == StatusArchived {
		return Channel{}, conflict("archived provider cannot be configured")
	}
	if status == StatusEnabled && provider.Status != StatusEnabled {
		return Channel{}, conflict("enabled channel requires an enabled provider")
	}

	row, err := s.store.CreateChannel(ctx, sqlc.CreateChannelParams{
		ProviderID:          in.ProviderID,
		Name:                name,
		Protocol:            protocol,
		AdapterKey:          adapterKey,
		Credential:          strings.TrimSpace(in.Credential),
		Status:              status,
		Priority:            in.Priority,
		ResponseTimeoutMs:   timeoutParam(in.ResponseTimeoutMs),
		FirstTokenTimeoutMs: timeoutParam(in.FirstTokenTimeoutMs),
		ConcurrencyLimit:    rateLimitParam(capacity.Concurrency),
		StickyEnabled:       boolParam(in.StickyEnabled),
		StickyTtlMs:         nullableInt8Param(in.StickyTTLms),
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
	ch.RuntimeSyncPending = !s.initializeCapacityControl(ctx, row, capacityPayload)
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
	if in.ProviderID <= 0 {
		return Channel{}, invalidArgument("provider_id", "provider_id must be positive")
	}
	if err := validateStatus(status); err != nil {
		return Channel{}, err
	}
	if err := validatePriority(in.Priority); err != nil {
		return Channel{}, err
	}
	if err := validateTimeout("response_timeout_ms", in.ResponseTimeoutMs); err != nil {
		return Channel{}, err
	}
	if err := validateTimeout("first_token_timeout_ms", in.FirstTokenTimeoutMs); err != nil {
		return Channel{}, err
	}
	if err := validateStickyPolicy(in.StickyEnabled, in.StickyTTLms); err != nil {
		return Channel{}, err
	}

	cur, err := s.store.GetChannel(ctx, in.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Channel{}, notFound("channel not found")
		}
		return Channel{}, storeFailed(err, "get channel for update")
	}
	if in.ProviderID != 0 && in.ProviderID != cur.ProviderID {
		return Channel{}, invalidArgument("provider_id", "channel provider cannot be changed")
	}
	provider, err := s.store.GetProvider(ctx, cur.ProviderID)
	if err != nil {
		return Channel{}, storeFailed(err, "load provider for channel")
	}
	if provider.Status == StatusArchived {
		return Channel{}, conflict("archived provider cannot be configured")
	}
	if status == StatusEnabled && provider.Status != StatusEnabled {
		return Channel{}, conflict("enabled channel requires an enabled provider")
	}
	// Channel 实体停用是结构供给收缩（ADR-0018）：需要 Model 锁、影响预览与指纹确认联动。
	disabling := cur.Status == StatusEnabled && status == StatusDisabled

	if in.CapacityProvided {
		desiredCapacity := ChannelCapacity{Concurrency: in.ConcurrencyLimit}
		desiredPayload, payloadErr := CanonicalCapacityPayload(desiredCapacity)
		if payloadErr != nil {
			return Channel{}, payloadErr
		}
		currentPayload, payloadErr := CanonicalCapacityPayloadFromChannel(cur)
		if payloadErr != nil {
			return Channel{}, storeFailed(payloadErr, "encode current channel capacity")
		}
		if currentPayload != desiredPayload {
			return s.updateWithPublishedCapacity(ctx, in, cur, desiredCapacity, desiredPayload, disabling)
		}
	}

	if disabling {
		return s.disableChannelWithLinkage(ctx, in, name)
	}

	row, err := s.store.UpdateChannel(ctx, sqlc.UpdateChannelParams{
		ID:                  in.ID,
		Name:                name,
		Status:              status,
		Priority:            in.Priority,
		ResponseTimeoutMs:   timeoutParam(in.ResponseTimeoutMs),
		FirstTokenTimeoutMs: timeoutParam(in.FirstTokenTimeoutMs),
		StickyEnabled:       boolParam(in.StickyEnabled),
		StickyTtlMs:         nullableInt8Param(in.StickyTTLms),
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

	ch := toChannel(row)
	if payload, payloadErr := CanonicalCapacityPayloadFromChannel(row); payloadErr == nil {
		ch.RuntimeSyncPending = !s.capacityControlIsActive(ctx, row.ID, row.CapacityRevision, payload)
	} else {
		ch.RuntimeSyncPending = true
	}
	return s.enrichProviderName(ctx, ch)
}

// disableChannelWithLinkage 在一个事务内停用 Channel 实体并联动暂停失去最后结构支撑的
// Offering（原因 channel_disabled）。Binding 行不改写、Model 不自动停用（ADR-0018 全局影响
// 计算只读取 Binding 行）；重新启用 Channel 后 Offering 不自动恢复。
func (s *Service) disableChannelWithLinkage(ctx context.Context, in UpdateInput, name string) (Channel, error) {
	if s.db == nil || s.queries == nil {
		return Channel{}, storeFailed(errors.New("supply linkage dependencies are unavailable"), "disable channel")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Channel{}, storeFailed(err, "begin channel disable transaction")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.queries.WithTx(tx)

	// 结构支撑串行化：聚合该 Channel 全部 enabled Binding 的 Model 并按稳定顺序锁定，
	// 锁内计算影响，不复用锁外预检结果。
	if _, err := supply.LockModelsForChannel(ctx, q, in.ID); err != nil {
		return Channel{}, storeFailed(err, "lock models for channel disable")
	}
	impact, err := supply.ChannelImpact(ctx, q, in.ID)
	if err != nil {
		return Channel{}, storeFailed(err, "compute channel disable impact")
	}
	if err := supply.Authorize(impact,
		"channel_disable_confirmation_required",
		"disabling this channel removes the last structural support for some route offerings; confirm with the impact fingerprint",
		in.Confirmation,
	); err != nil {
		return Channel{}, err
	}

	row, err := q.UpdateChannel(ctx, sqlc.UpdateChannelParams{
		ID:                  in.ID,
		Name:                name,
		Status:              StatusDisabled,
		Priority:            in.Priority,
		ResponseTimeoutMs:   timeoutParam(in.ResponseTimeoutMs),
		FirstTokenTimeoutMs: timeoutParam(in.FirstTokenTimeoutMs),
		StickyEnabled:       boolParam(in.StickyEnabled),
		StickyTtlMs:         nullableInt8Param(in.StickyTTLms),
	})
	if err != nil {
		return Channel{}, channelUpdateError(err)
	}
	if err := supply.DisableOfferings(ctx, q, impact.AffectedOfferings, supply.ReasonChannelDisabled); err != nil {
		return Channel{}, storeFailed(err, "disable offerings losing support")
	}
	if err := tx.Commit(ctx); err != nil {
		return Channel{}, storeFailed(err, "commit channel disable transaction")
	}

	ch := toChannel(row)
	if payload, payloadErr := CanonicalCapacityPayloadFromChannel(row); payloadErr == nil {
		ch.RuntimeSyncPending = !s.capacityControlIsActive(ctx, row.ID, row.CapacityRevision, payload)
	} else {
		ch.RuntimeSyncPending = true
	}
	return s.enrichProviderName(ctx, ch)
}

func (s *Service) updateWithPublishedCapacity(
	ctx context.Context,
	in UpdateInput,
	current sqlc.Channel,
	capacity ChannelCapacity,
	payload string,
	disabling bool,
) (Channel, error) {
	if s.runtimePublisher == nil || s.runtimeStore == nil {
		return Channel{}, failure.New(
			failure.CodeGatewayBreakerStoreUnavailable,
			failure.WithMessage("channel: capacity runtime-control publisher unavailable"),
		)
	}
	token, err := newCapacityControlToken()
	if err != nil {
		return Channel{}, failure.Wrap(
			failure.CodeConfigInvalid,
			err,
			failure.WithMessage("channel: generate capacity runtime-control token"),
		)
	}

	nextRevision := current.CapacityRevision + 1
	channelID := current.ID
	var committedRow sqlc.Channel
	publishResult, err := s.runtimePublisher.Publish(ctx, runtimecontrol.PublishRequest{
		Kind:            runtimecontrol.KindChannelCapacity,
		Target:          s.runtimeStore.ChannelCapacityControl(channelID),
		Token:           token,
		Payload:         payload,
		CurrentRevision: current.CapacityRevision,
		NextRevision:    nextRevision,
		ChannelID:       &channelID,
		BusinessCommit: func(ctx context.Context, tx pgx.Tx) error {
			qtx := sqlc.New(tx)
			// Channel 实体停用联动（ADR-0018）：与容量提交同事务，锁内重算影响并确认。
			var linkageOfferings []supply.AffectedOffering
			if disabling {
				if _, lockErr := supply.LockModelsForChannel(ctx, qtx, channelID); lockErr != nil {
					return storeFailed(lockErr, "lock models for channel disable")
				}
				impact, impactErr := supply.ChannelImpact(ctx, qtx, channelID)
				if impactErr != nil {
					return storeFailed(impactErr, "compute channel disable impact")
				}
				if authErr := supply.Authorize(impact,
					"channel_disable_confirmation_required",
					"disabling this channel removes the last structural support for some route offerings; confirm with the impact fingerprint",
					in.Confirmation,
				); authErr != nil {
					return authErr
				}
				linkageOfferings = impact.AffectedOfferings
			}
			if _, updateErr := qtx.UpdateChannel(ctx, sqlc.UpdateChannelParams{
				ID:                  in.ID,
				Name:                strings.TrimSpace(in.Name),
				Status:              strings.TrimSpace(in.Status),
				Priority:            in.Priority,
				ResponseTimeoutMs:   timeoutParam(in.ResponseTimeoutMs),
				FirstTokenTimeoutMs: timeoutParam(in.FirstTokenTimeoutMs),
				StickyEnabled:       boolParam(in.StickyEnabled),
				StickyTtlMs:         nullableInt8Param(in.StickyTTLms),
			}); updateErr != nil {
				return channelUpdateError(updateErr)
			}
			row, updateErr := qtx.CommitChannelCapacityAtRevision(ctx, sqlc.CommitChannelCapacityAtRevisionParams{
				ConcurrencyLimit: rateLimitParam(capacity.Concurrency),
				NextRevision:     nextRevision,
				ID:               channelID,
				CurrentRevision:  current.CapacityRevision,
			})
			if updateErr != nil {
				if errors.Is(updateErr, pgx.ErrNoRows) {
					return conflict("channel capacity changed during publish; retry with current state")
				}
				return storeFailed(updateErr, "commit channel capacity")
			}
			if disabling {
				if linkErr := supply.DisableOfferings(ctx, qtx, linkageOfferings, supply.ReasonChannelDisabled); linkErr != nil {
					return storeFailed(linkErr, "disable offerings losing support")
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
			failure.WithMessage("channel: capacity runtime-control publish did not commit business state"),
		)
	}

	row := committedRow
	if row.ID == 0 {
		row, err = s.store.GetChannel(ctx, channelID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return Channel{}, notFound("channel not found after capacity publish")
			}
			return Channel{}, storeFailed(err, "get channel after capacity publish")
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

func (s *Service) initializeCapacityControl(ctx context.Context, row sqlc.Channel, payload string) bool {
	if s.runtimeStore == nil || row.CapacityRevision <= 0 {
		return false
	}
	target := s.runtimeStore.ChannelCapacityControl(row.ID)
	if _, err := s.runtimeStore.RestoreMissingControl(ctx, target, row.CapacityRevision, payload); err != nil {
		return false
	}
	return s.capacityControlIsActive(ctx, row.ID, row.CapacityRevision, payload)
}

func (s *Service) capacityControlIsActive(ctx context.Context, channelID, revision int64, payload string) bool {
	if s.runtimeStore == nil {
		return false
	}
	snapshot, err := s.runtimeStore.ReadControl(
		ctx,
		s.runtimeStore.ChannelCapacityControl(channelID),
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

// Archive 只归档已显式移出所有 Route 池的渠道，不静默拆线或替换。
func (s *Service) Archive(ctx context.Context, id int64) error {
	if id <= 0 {
		return invalidArgument("id", "channel id must be positive")
	}
	affectedRoutes, err := s.store.ListRoutesReferencingChannel(ctx, id)
	if err != nil {
		return storeFailed(err, "check channel archive route impact")
	}
	if len(affectedRoutes) > 0 {
		return conflict(fmt.Sprintf(
			"remove channel from route %q (%d) before archiving it",
			affectedRoutes[0].Name, affectedRoutes[0].ID,
		))
	}
	// 归档前置（ADR-0018）：archived Channel 下不得存在 enabled Binding；
	// 先经影响预览停用全部 enabled Binding，再归档。
	enabledBindings, err := s.store.CountEnabledBindingsByChannel(ctx, id)
	if err != nil {
		return storeFailed(err, "check channel archive binding impact")
	}
	if enabledBindings > 0 {
		return conflict("disable all enabled model bindings on this channel before archiving it")
	}
	affected, err := s.store.ArchiveChannel(ctx, id)
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
		ID:                  c.ID,
		ProviderID:          c.ProviderID,
		ConfigRevision:      c.ConfigRevision,
		CapacityRevision:    c.CapacityRevision,
		Name:                c.Name,
		Protocol:            c.Protocol,
		AdapterKey:          c.AdapterKey,
		Credential:          c.Credential,
		Status:              c.Status,
		Priority:            c.Priority,
		ResponseTimeoutMs:   timeoutResult(c.ResponseTimeoutMs),
		FirstTokenTimeoutMs: timeoutResult(c.FirstTokenTimeoutMs),
		ConcurrencyLimit:    rateLimitResult(c.ConcurrencyLimit),
		StickyEnabled:       boolResult(c.StickyEnabled),
		StickyTTLms:         int8Result(c.StickyTtlMs),
		CreatedAt:           c.CreatedAt.Time,
		UpdatedAt:           c.UpdatedAt.Time,
		ArchivedAt:          timestampResult(c.ArchivedAt),

		LastTestedAt:      timestampResult(c.LastTestedAt),
		LastTestOK:        boolResult(c.LastTestOk),
		LastTestLatencyMs: timeoutResult(c.LastTestLatencyMs),
		LastTestError:     textResult(c.LastTestError),
	}
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
			ch.Origin = provider.Origin
			ch.OriginRevision = provider.OriginRevision
			ch.ProviderStatusRevision = provider.StatusRevision
		}
	}
	return ch, nil
}

// toChannelRow 映射分页列表行，额外带出 JOIN 出的 provider 名称。
func toChannelRow(c sqlc.ListChannelsPageRow) Channel {
	return Channel{
		ID:                  c.ID,
		ProviderID:          c.ProviderID,
		ProviderName:        c.ProviderName,
		ConfigRevision:      c.ConfigRevision,
		CapacityRevision:    c.CapacityRevision,
		Name:                c.Name,
		Protocol:            c.Protocol,
		AdapterKey:          c.AdapterKey,
		Origin:              c.Origin,
		Credential:          c.Credential,
		Status:              c.Status,
		Priority:            c.Priority,
		ResponseTimeoutMs:   timeoutResult(c.ResponseTimeoutMs),
		FirstTokenTimeoutMs: timeoutResult(c.FirstTokenTimeoutMs),
		ConcurrencyLimit:    rateLimitResult(c.ConcurrencyLimit),
		StickyEnabled:       boolResult(c.StickyEnabled),
		StickyTTLms:         int8Result(c.StickyTtlMs),
		CreatedAt:           c.CreatedAt.Time,
		UpdatedAt:           c.UpdatedAt.Time,

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

func channelCapacityFromChannel(row sqlc.Channel) ChannelCapacity {
	return ChannelCapacity{Concurrency: rateLimitResult(row.ConcurrencyLimit)}
}

func newCapacityControlToken() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "rctl_channel_" + hex.EncodeToString(raw[:]), nil
}

// validateChannelCapacity 校验渠道并发容量非负。
func validateChannelCapacity(concurrency *int64) error {
	if concurrency != nil && *concurrency < 0 {
		return invalidArgument("concurrency_limit", "concurrency limit must be a non-negative integer (0 means unlimited)")
	}
	return nil
}

func validatePriority(priority int32) error {
	if priority < 0 || priority > 100 || priority%10 != 0 {
		return invalidArgument("priority", "priority must be one of 0, 10, ..., 100")
	}
	return nil
}

func validateStickyPolicy(enabled *bool, ttlMs *int64) error {
	if enabled == nil {
		if ttlMs != nil {
			return invalidArgument("sticky_ttl_ms", "sticky_ttl_ms must be null when sticky_enabled inherits the system default")
		}
		return nil
	}
	if !*enabled {
		if ttlMs != nil {
			return invalidArgument("sticky_ttl_ms", "sticky_ttl_ms must be null when sticky is disabled")
		}
		return nil
	}
	if ttlMs == nil || *ttlMs <= 0 {
		return invalidArgument("sticky_ttl_ms", "sticky_ttl_ms must be > 0 when sticky is enabled")
	}
	return nil
}

func boolParam(value *bool) pgtype.Bool {
	if value == nil {
		return pgtype.Bool{}
	}
	return pgtype.Bool{Bool: *value, Valid: true}
}

func nullableInt8Param(value *int64) pgtype.Int8 {
	if value == nil {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: *value, Valid: true}
}

func int8Result(value pgtype.Int8) *int64 {
	if !value.Valid {
		return nil
	}
	out := value.Int64
	return &out
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

func validateTimeout(field string, ms *int32) error {
	if ms != nil && *ms <= 0 {
		return invalidArgument(field, field+" must be > 0 when set")
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
