// Package channel 编排 admin 管理端的 channel 读写。
//
// channel 写入路径负责：① 校验 (protocol, adapter_key) 复合键在 adapter registry 注册
// （关 GAP-6-003，避免把不可运行绑定写入业务数据）；② 把上游凭据以明文落库
// （产品决策：渠道凭据明文存储，管理端可查看/复制/编辑）。
package channel

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
)

const (
	// ProtocolOpenAI / ProtocolAnthropic 是 channel 对外协议族，与 channels.protocol 的 DB 约束一致。
	ProtocolOpenAI    = "openai"
	ProtocolAnthropic = "anthropic"

	// StatusEnabled / StatusDisabled 是 channel 启停状态，与 channels.status 的 DB 约束一致。
	StatusEnabled  = "enabled"
	StatusDisabled = "disabled"
)

// Store 定义 channel 管理所需的存储能力。
type Store interface {
	GetProvider(ctx context.Context, id int64) (sqlc.Provider, error)
	ListChannelsPage(ctx context.Context, arg sqlc.ListChannelsPageParams) ([]sqlc.ListChannelsPageRow, error)
	CountChannels(ctx context.Context, arg sqlc.CountChannelsParams) (int64, error)
	GetChannel(ctx context.Context, id int64) (sqlc.Channel, error)
	CreateChannel(ctx context.Context, arg sqlc.CreateChannelParams) (sqlc.Channel, error)
	UpdateChannel(ctx context.Context, arg sqlc.UpdateChannelParams) (sqlc.Channel, error)
	SetChannelRateLimits(ctx context.Context, arg sqlc.SetChannelRateLimitsParams) (sqlc.Channel, error)
	UpdateChannelCredential(ctx context.Context, arg sqlc.UpdateChannelCredentialParams) (int64, error)
	DeleteChannelCascade(ctx context.Context, id int64) (int64, error)
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
	Name         string
	Protocol     string
	AdapterKey   string
	BaseURL      string
	Credential   string
	Status       string
	Priority     int32
	TimeoutMs    *int32
	// RPMLimit/TPMLimit/RPDLimit 是渠道级限流上限（P2-8）：nil=继承全局默认，0=不限，>0=具体上限。
	RPMLimit  *int64
	TPMLimit  *int64
	RPDLimit  *int64
	CreatedAt time.Time
	UpdatedAt time.Time
	// LastTested* 是最近一次主动检测结果（渠道检测，阶段一）：全 nil 表示从未检测。
	// 仅由检测端点写入，不参与路由/计费，也不改渠道启停状态。
	LastTestedAt      *time.Time
	LastTestOK        *bool
	LastTestLatencyMs *int32
	LastTestError     *string
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
	ProviderID int64
	Name       string
	Protocol   string
	AdapterKey string
	BaseURL    string
	Credential string
	Status     string
	Priority   int32
	TimeoutMs  *int32
	// RateLimitsProvided=true 时按 RPM/TPM/RPD 设置渠道级限流（各值 nil=继承全局默认，0=不限，>0=具体上限）。
	RateLimitsProvided bool
	RPMLimit           *int64
	TPMLimit           *int64
	RPDLimit           *int64
}

// UpdateInput 是更新 channel 的入参；protocol、adapter_key 与凭据不在此修改。
type UpdateInput struct {
	ID        int64
	Name      string
	BaseURL   string
	Status    string
	Priority  int32
	TimeoutMs *int32
	// RateLimitsProvided=true 时按 RPM/TPM/RPD 原子设置渠道级限流。
	RateLimitsProvided bool
	RPMLimit           *int64
	TPMLimit           *int64
	RPDLimit           *int64
}

// RotateCredentialInput 是轮换 channel 上游凭据的入参。
type RotateCredentialInput struct {
	ID         int64
	Credential string
}

// Service 编排 channel 管理读写。
type Service struct {
	store    Store
	registry AdapterRegistry
}

// NewService 创建 channel 管理服务。
func NewService(store Store, registry AdapterRegistry) *Service {
	return &Service{store: store, registry: registry}
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
		items = append(items, toChannelRow(row))
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

	return s.enrichProviderName(ctx, toChannel(row))
}

// Create 创建 channel：校验复合键在 registry 注册、provider 存在，再加密凭据落库。
func (s *Service) Create(ctx context.Context, in CreateInput) (Channel, error) {
	name := strings.TrimSpace(in.Name)
	protocol := strings.TrimSpace(in.Protocol)
	adapterKey := strings.TrimSpace(in.AdapterKey)
	baseURL := strings.TrimSpace(in.BaseURL)
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
	if err := validateBaseURL(baseURL); err != nil {
		return Channel{}, err
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

	row, err := s.store.CreateChannel(ctx, sqlc.CreateChannelParams{
		ProviderID: in.ProviderID,
		Name:       name,
		Protocol:   protocol,
		AdapterKey: adapterKey,
		BaseUrl:    baseURL,
		Credential: strings.TrimSpace(in.Credential),
		Status:     status,
		Priority:   in.Priority,
		TimeoutMs:  timeoutParam(in.TimeoutMs),
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

	// 渠道级限流作为独立 UPDATE（CreateChannel 不接收 rpm/tpm/rpd），创建后按需补设（P2-8）。
	if in.RateLimitsProvided {
		if err := validateChannelRateLimits(in.RPMLimit, in.TPMLimit, in.RPDLimit); err != nil {
			return Channel{}, err
		}
		limited, err := s.store.SetChannelRateLimits(ctx, sqlc.SetChannelRateLimitsParams{
			ID:       row.ID,
			RpmLimit: rateLimitParam(in.RPMLimit),
			TpmLimit: rateLimitParam(in.TPMLimit),
			RpdLimit: rateLimitParam(in.RPDLimit),
		})
		if err != nil {
			return Channel{}, storeFailed(err, "set channel rate limits")
		}
		row = limited
	}

	return s.enrichProviderName(ctx, toChannel(row))
}

// Update 更新 channel 的展示名、上游地址、状态、优先级与超时。
func (s *Service) Update(ctx context.Context, in UpdateInput) (Channel, error) {
	if in.ID <= 0 {
		return Channel{}, invalidArgument("id", "channel id must be positive")
	}
	name := strings.TrimSpace(in.Name)
	baseURL := strings.TrimSpace(in.BaseURL)
	status := strings.TrimSpace(in.Status)

	if name == "" {
		return Channel{}, invalidArgument("name", "name is required")
	}
	if err := validateBaseURL(baseURL); err != nil {
		return Channel{}, err
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

	row, err := s.store.UpdateChannel(ctx, sqlc.UpdateChannelParams{
		ID:        in.ID,
		Name:      name,
		BaseUrl:   baseURL,
		Status:    status,
		Priority:  in.Priority,
		TimeoutMs: timeoutParam(in.TimeoutMs),
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

	if in.RateLimitsProvided {
		if err := validateChannelRateLimits(in.RPMLimit, in.TPMLimit, in.RPDLimit); err != nil {
			return Channel{}, err
		}
		limited, err := s.store.SetChannelRateLimits(ctx, sqlc.SetChannelRateLimitsParams{
			ID:       in.ID,
			RpmLimit: rateLimitParam(in.RPMLimit),
			TpmLimit: rateLimitParam(in.TPMLimit),
			RpdLimit: rateLimitParam(in.RPDLimit),
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return Channel{}, notFound("channel not found")
			}
			return Channel{}, storeFailed(err, "set channel rate limits")
		}
		row = limited
	}

	return s.enrichProviderName(ctx, toChannel(row))
}

// RotateCredential 轮换 channel 上游凭据；目标不存在返回 not_found。
func (s *Service) RotateCredential(ctx context.Context, in RotateCredentialInput) error {
	if in.ID <= 0 {
		return invalidArgument("id", "channel id must be positive")
	}
	if strings.TrimSpace(in.Credential) == "" {
		return invalidArgument("credential", "credential is required")
	}

	affected, err := s.store.UpdateChannelCredential(ctx, sqlc.UpdateChannelCredentialParams{
		ID:         in.ID,
		Credential: strings.TrimSpace(in.Credential),
	})
	if err != nil {
		return storeFailed(err, "rotate channel credential")
	}
	if affected == 0 {
		return notFound("channel not found")
	}

	return nil
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

	affected, err := s.store.DeleteChannelCascade(ctx, id)
	if err != nil {
		if isForeignKeyViolation(err) {
			return conflict("channel is referenced by request/billing history; disable it instead of deleting")
		}
		return storeFailed(err, "delete channel")
	}
	if affected == 0 {
		return notFound("channel not found")
	}

	return nil
}

func toChannel(c sqlc.Channel) Channel {
	return Channel{
		ID:         c.ID,
		ProviderID: c.ProviderID,
		Name:       c.Name,
		Protocol:   c.Protocol,
		AdapterKey: c.AdapterKey,
		BaseURL:    c.BaseUrl,
		Credential: c.Credential,
		Status:     c.Status,
		Priority:   c.Priority,
		TimeoutMs:  timeoutResult(c.TimeoutMs),
		RPMLimit:   rateLimitResult(c.RpmLimit),
		TPMLimit:   rateLimitResult(c.TpmLimit),
		RPDLimit:   rateLimitResult(c.RpdLimit),
		CreatedAt:  c.CreatedAt.Time,
		UpdatedAt:  c.UpdatedAt.Time,

		LastTestedAt:      timestampResult(c.LastTestedAt),
		LastTestOK:        boolResult(c.LastTestOk),
		LastTestLatencyMs: timeoutResult(c.LastTestLatencyMs),
		LastTestError:     textResult(c.LastTestError),
	}
}

func (s *Service) enrichProviderName(ctx context.Context, ch Channel) (Channel, error) {
	if ch.ProviderID <= 0 {
		return ch, nil
	}
	provider, err := s.store.GetProvider(ctx, ch.ProviderID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ch, nil
		}
		return Channel{}, storeFailed(err, "load provider for channel")
	}
	ch.ProviderName = provider.Name
	return ch, nil
}

// toChannelRow 映射分页列表行，额外带出 JOIN 出的 provider 名称。
func toChannelRow(c sqlc.ListChannelsPageRow) Channel {
	return Channel{
		ID:           c.ID,
		ProviderID:   c.ProviderID,
		ProviderName: c.ProviderName,
		Name:         c.Name,
		Protocol:     c.Protocol,
		AdapterKey:   c.AdapterKey,
		BaseURL:      c.BaseUrl,
		Credential:   c.Credential,
		Status:       c.Status,
		Priority:     c.Priority,
		TimeoutMs:    timeoutResult(c.TimeoutMs),
		RPMLimit:     rateLimitResult(c.RpmLimit),
		TPMLimit:     rateLimitResult(c.TpmLimit),
		RPDLimit:     rateLimitResult(c.RpdLimit),
		CreatedAt:    c.CreatedAt.Time,
		UpdatedAt:    c.UpdatedAt.Time,

		LastTestedAt:      timestampResult(c.LastTestedAt),
		LastTestOK:        boolResult(c.LastTestOk),
		LastTestLatencyMs: timeoutResult(c.LastTestLatencyMs),
		LastTestError:     textResult(c.LastTestError),
	}
}

// rateLimitParam 把 *int64 转成可空 pgtype.Int4（nil=NULL 继承全局默认；含 0=显式不限）。
func rateLimitParam(v *int64) pgtype.Int4 {
	if v == nil {
		return pgtype.Int4{Valid: false}
	}
	return pgtype.Int4{Int32: int32(*v), Valid: true}
}

// rateLimitResult 把可空 pgtype.Int4 转成 *int64（nil=继承全局默认）。
func rateLimitResult(v pgtype.Int4) *int64 {
	if !v.Valid {
		return nil
	}
	out := int64(v.Int32)
	return &out
}

// validateChannelRateLimits 校验渠道级限流非负（限流上限不能为负数）。
func validateChannelRateLimits(rpm, tpm, rpd *int64) error {
	for field, v := range map[string]*int64{"rpm_limit": rpm, "tpm_limit": tpm, "rpd_limit": rpd} {
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
