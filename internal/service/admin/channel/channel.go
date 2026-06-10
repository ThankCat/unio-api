// Package channel 编排 admin 管理端的 channel 读写。
//
// channel 写入路径负责：① 校验 (protocol, adapter_key) 复合键在 adapter registry 注册
// （关 GAP-6-003，避免把不可运行绑定写入业务数据）；② 把明文上游凭据经 cipher 加密成
// credential_encrypted 落库。明文凭据绝不回读、不进日志、不出 DTO。
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

	"github.com/ThankCat/unio-api/internal/core/credential"
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
	UpdateChannelCredential(ctx context.Context, arg sqlc.UpdateChannelCredentialParams) (int64, error)
}

// AdapterRegistry 暴露 channel 写入前校验复合键是否被当前进程支持的最小能力。
type AdapterRegistry interface {
	HasAny(protocol string, adapterKey string) bool
}

// Channel 是 admin 视角的 channel 业务事实；不含上游凭据。
//
// ProviderName 仅在分页列表场景由 JOIN 带出；单条读取/写入路径为空串。
type Channel struct {
	ID           int64
	ProviderID   int64
	ProviderName string
	Name         string
	Protocol     string
	AdapterKey   string
	BaseURL      string
	Status       string
	Priority     int32
	TimeoutMs    *int32
	CreatedAt    time.Time
	UpdatedAt    time.Time
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
}

// UpdateInput 是更新 channel 的入参；protocol、adapter_key 与凭据不在此修改。
type UpdateInput struct {
	ID        int64
	Name      string
	BaseURL   string
	Status    string
	Priority  int32
	TimeoutMs *int32
}

// RotateCredentialInput 是轮换 channel 上游凭据的入参。
type RotateCredentialInput struct {
	ID         int64
	Credential string
}

// Service 编排 channel 管理读写。
type Service struct {
	store    Store
	cipher   credential.Cipher
	registry AdapterRegistry
}

// NewService 创建 channel 管理服务。
func NewService(store Store, cipher credential.Cipher, registry AdapterRegistry) *Service {
	return &Service{store: store, cipher: cipher, registry: registry}
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

	return toChannel(row), nil
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
	if adapterKey == "" {
		return Channel{}, invalidArgument("adapter_key", "adapter_key is required")
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

	encrypted, err := s.cipher.Encrypt(in.Credential)
	if err != nil {
		return Channel{}, err
	}

	row, err := s.store.CreateChannel(ctx, sqlc.CreateChannelParams{
		ProviderID:          in.ProviderID,
		Name:                name,
		Protocol:            protocol,
		AdapterKey:          adapterKey,
		BaseUrl:             baseURL,
		CredentialEncrypted: encrypted,
		Status:              status,
		Priority:            in.Priority,
		TimeoutMs:           timeoutParam(in.TimeoutMs),
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

	return toChannel(row), nil
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

	return toChannel(row), nil
}

// RotateCredential 轮换 channel 上游凭据；目标不存在返回 not_found。
func (s *Service) RotateCredential(ctx context.Context, in RotateCredentialInput) error {
	if in.ID <= 0 {
		return invalidArgument("id", "channel id must be positive")
	}
	if strings.TrimSpace(in.Credential) == "" {
		return invalidArgument("credential", "credential is required")
	}

	encrypted, err := s.cipher.Encrypt(in.Credential)
	if err != nil {
		return err
	}

	affected, err := s.store.UpdateChannelCredential(ctx, sqlc.UpdateChannelCredentialParams{
		ID:                  in.ID,
		CredentialEncrypted: encrypted,
	})
	if err != nil {
		return storeFailed(err, "rotate channel credential")
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
		Status:     c.Status,
		Priority:   c.Priority,
		TimeoutMs:  timeoutResult(c.TimeoutMs),
		CreatedAt:  c.CreatedAt.Time,
		UpdatedAt:  c.UpdatedAt.Time,
	}
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
		Status:       c.Status,
		Priority:     c.Priority,
		TimeoutMs:    timeoutResult(c.TimeoutMs),
		CreatedAt:    c.CreatedAt.Time,
		UpdatedAt:    c.UpdatedAt.Time,
	}
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
