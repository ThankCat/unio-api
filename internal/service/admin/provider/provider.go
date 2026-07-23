// Package provider 编排 admin 管理端的 provider 读写。
//
// 只做校验、存储编排与 sqlc row → 领域事实映射；不暴露 sqlc row 给上层。
package provider

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

const (
	// StatusEnabled 表示 provider 启用。
	StatusEnabled = "enabled"
	// StatusDisabled 表示 provider 停用。
	StatusDisabled = "disabled"
	// StatusArchived 表示 provider 已归档（默认隐藏、不参与路由；可恢复）。
	StatusArchived = "archived"
)

// slugPattern 限定 provider slug：小写字母数字开头，允许小写字母、数字与连字符，长度 1..64。
var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// Store 定义 provider 管理所需的存储能力。
type Store interface {
	ListProvidersPage(ctx context.Context, arg sqlc.ListProvidersPageParams) ([]sqlc.Provider, error)
	CountProviders(ctx context.Context, arg sqlc.CountProvidersParams) (int64, error)
	GetProvider(ctx context.Context, id int64) (sqlc.Provider, error)
	GetChannel(ctx context.Context, id int64) (sqlc.Channel, error)
	CreateProvider(ctx context.Context, arg sqlc.CreateProviderParams) (sqlc.Provider, error)
	UpdateProvider(ctx context.Context, arg sqlc.UpdateProviderParams) (sqlc.Provider, error)
	DeleteProvider(ctx context.Context, id int64) (int64, error)
	ArchiveProviderCascade(ctx context.Context, id int64) (int64, error)
	ArchiveProviderWithReplacement(ctx context.Context, arg sqlc.ArchiveProviderWithReplacementParams) (int64, error)
	ListEnabledRoutesEmptiedByProvider(ctx context.Context, providerID int64) ([]sqlc.ListEnabledRoutesEmptiedByProviderRow, error)
	RestoreProvider(ctx context.Context, id int64) (int64, error)
}

// ListParams 是分页/过滤列出 provider 的入参；Status、Query 为空表示不过滤。
type ListParams struct {
	Status string
	Query  string
	Limit  int32
	Offset int32
}

// ListResult 是分页列表结果：当前页条目 + 过滤后总数。
type ListResult struct {
	Items []Provider
	Total int64
}

// Provider 是 admin 视角的 provider 业务事实。
type Provider struct {
	ID                    int64
	Slug                  string
	Name                  string
	Status                string
	CreatedAt             time.Time
	UpdatedAt             time.Time
	ArchivedAt            *time.Time
	RuntimeSyncPending    bool
	AffectedEndpointCount int
}

// StatusChangeResult 是 Provider 状态写入后可安全返回给 Admin 的运行态同步摘要。
type StatusChangeResult struct {
	RuntimeSyncPending    bool
	AffectedEndpointCount int
}

// CreateInput 是创建 provider 的入参。
type CreateInput struct {
	Slug   string
	Name   string
	Status string
}

// UpdateInput 是更新 provider 的入参；slug 不可变，不在此修改。
type UpdateInput struct {
	ID     int64
	Name   string
	Status string
}

// Service 编排 provider 管理读写。
type Service struct {
	store    Store
	fencer   *StatusFencer
	batchMax func(context.Context) int
}

// NewService 创建 provider 管理服务。
func NewService(store Store) *Service {
	return &Service{store: store, batchMax: func(context.Context) int { return 256 }}
}

// WithStatusFencer enables the production Provider status batch fence.
func (s *Service) WithStatusFencer(fencer *StatusFencer, batchMax func(context.Context) int) *Service {
	s.fencer = fencer
	if batchMax != nil {
		s.batchMax = batchMax
	}
	return s
}

// List 按 params 过滤分页列出 provider，并返回过滤后的总数。
func (s *Service) List(ctx context.Context, params ListParams) (ListResult, error) {
	status := textParam(params.Status)
	q := textParam(params.Query)

	rows, err := s.store.ListProvidersPage(ctx, sqlc.ListProvidersPageParams{
		Status:     status,
		Q:          q,
		PageLimit:  params.Limit,
		PageOffset: params.Offset,
	})
	if err != nil {
		return ListResult{}, storeFailed(err, "list providers")
	}

	total, err := s.store.CountProviders(ctx, sqlc.CountProvidersParams{
		Status: status,
		Q:      q,
	})
	if err != nil {
		return ListResult{}, storeFailed(err, "count providers")
	}

	items := make([]Provider, 0, len(rows))
	for _, row := range rows {
		items = append(items, toProvider(row))
	}

	return ListResult{Items: items, Total: total}, nil
}

// textParam 把空串转成 NULL（不过滤），非空转成有值 pgtype.Text。
func textParam(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}

// Get 按 id 读取单个 provider。
func (s *Service) Get(ctx context.Context, id int64) (Provider, error) {
	if id <= 0 {
		return Provider{}, invalidArgument("id", "provider id must be positive")
	}

	row, err := s.store.GetProvider(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Provider{}, notFound("provider not found")
		}
		return Provider{}, storeFailed(err, "get provider")
	}

	return toProvider(row), nil
}

// Create 创建 provider；slug 重复返回 conflict。
func (s *Service) Create(ctx context.Context, in CreateInput) (Provider, error) {
	slug := strings.TrimSpace(in.Slug)
	name := strings.TrimSpace(in.Name)
	status := strings.TrimSpace(in.Status)

	if !slugPattern.MatchString(slug) {
		return Provider{}, invalidArgument("slug", "slug must match ^[a-z0-9][a-z0-9-]{0,63}$")
	}
	if name == "" {
		return Provider{}, invalidArgument("name", "name is required")
	}
	if err := validateStatus(status); err != nil {
		return Provider{}, err
	}

	row, err := s.store.CreateProvider(ctx, sqlc.CreateProviderParams{
		Slug:   slug,
		Name:   name,
		Status: status,
	})
	if err != nil {
		if isUniqueViolation(err) {
			return Provider{}, conflict("provider slug already exists")
		}
		return Provider{}, storeFailed(err, "create provider")
	}

	return toProvider(row), nil
}

// Update 更新 provider 的展示名与状态；目标不存在返回 not_found。
func (s *Service) Update(ctx context.Context, in UpdateInput) (Provider, error) {
	if in.ID <= 0 {
		return Provider{}, invalidArgument("id", "provider id must be positive")
	}
	name := strings.TrimSpace(in.Name)
	status := strings.TrimSpace(in.Status)

	if name == "" {
		return Provider{}, invalidArgument("name", "name is required")
	}
	if err := validateStatus(status); err != nil {
		return Provider{}, err
	}

	current, err := s.store.GetProvider(ctx, in.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Provider{}, notFound("provider not found")
		}
		return Provider{}, storeFailed(err, "get provider")
	}
	if current.Status != status && s.fencer != nil {
		endpoints, err := s.listEndpoints(ctx, in.ID)
		if err != nil {
			return Provider{}, err
		}
		result, err := s.fencer.publish(ctx, providerStatusChange{
			Current: current, NextName: name, NextStatus: status,
			Endpoints: endpoints, MaxBatch: s.batchMax(ctx),
		})
		if err != nil {
			return Provider{}, err
		}
		updated, err := s.Get(ctx, in.ID)
		if err != nil {
			return Provider{}, err
		}
		updated.RuntimeSyncPending = result.RuntimeSyncPending
		updated.AffectedEndpointCount = result.AffectedEndpointCount
		return updated, nil
	}

	row, err := s.store.UpdateProvider(ctx, sqlc.UpdateProviderParams{
		ID:     in.ID,
		Name:   name,
		Status: status,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Provider{}, notFound("provider not found")
		}
		return Provider{}, storeFailed(err, "update provider")
	}

	return toProvider(row), nil
}

// Delete 物理删除 provider，用于清理录错的脏数据；slug 随之释放，可重新录入同名。
//
// provider 无自身配置子表，不做级联：名下仍有 channel，或已被请求/账务历史（NO ACTION 外键）
// 引用时，DB 拒绝删除（23503），降级为 conflict，提示先删该服务商下的渠道或改用停用。
func (s *Service) Delete(ctx context.Context, id int64) error {
	if id <= 0 {
		return invalidArgument("id", "provider id must be positive")
	}

	// 硬删闸门（D-4）：只允许删除已归档实体，避免误删在用/停用的配置。
	cur, err := s.store.GetProvider(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound("provider not found")
		}
		return storeFailed(err, "get provider")
	}
	if cur.Status != StatusArchived {
		return conflict("provider must be archived before deletion")
	}

	affected, err := s.store.DeleteProvider(ctx, id)
	if err != nil {
		if isForeignKeyViolation(err) {
			return conflict("provider still has channels or is referenced by request/billing history; delete its channels first")
		}
		return storeFailed(err, "delete provider")
	}
	if affected == 0 {
		return notFound("provider not found")
	}

	return nil
}

// Archive 归档 provider：单事务内级联归档名下渠道（释放渠道名）+ 从线路池移除，再置 provider archived。
// slug 不变。幂等：已归档返回 not_found（0 行）。恢复不向下级联。
func (s *Service) Archive(ctx context.Context, id int64, replacementChannelID *int64) (StatusChangeResult, error) {
	if id <= 0 {
		return StatusChangeResult{}, invalidArgument("id", "provider id must be positive")
	}
	var current sqlc.Provider
	if s.fencer != nil {
		var err error
		current, err = s.store.GetProvider(ctx, id)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return StatusChangeResult{}, notFound("provider not found")
			}
			return StatusChangeResult{}, storeFailed(err, "get provider")
		}
		if current.Status == StatusArchived {
			return StatusChangeResult{}, notFound("provider not found or already archived")
		}
	}
	if replacementChannelID != nil {
		if *replacementChannelID <= 0 {
			return StatusChangeResult{}, invalidArgument("replacement_channel_id", "replacement channel id must be positive")
		}
		replacement, err := s.store.GetChannel(ctx, *replacementChannelID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return StatusChangeResult{}, invalidArgument("replacement_channel_id", "replacement channel not found")
			}
			return StatusChangeResult{}, storeFailed(err, "get replacement channel")
		}
		if replacement.ProviderID == id {
			return StatusChangeResult{}, invalidArgument("replacement_channel_id", "replacement channel must belong to another provider")
		}
		if replacement.Status != StatusEnabled || !replacement.CredentialValid || replacement.Credential == "" {
			return StatusChangeResult{}, conflict("replacement channel must be enabled, credential-valid, and fully configured")
		}
		replacementProvider, err := s.store.GetProvider(ctx, replacement.ProviderID)
		if err != nil {
			return StatusChangeResult{}, storeFailed(err, "get replacement channel provider")
		}
		if replacementProvider.Status != StatusEnabled {
			return StatusChangeResult{}, conflict("replacement channel provider must be enabled")
		}
		if s.fencer != nil {
			endpoints, err := s.listEndpoints(ctx, id)
			if err != nil {
				return StatusChangeResult{}, err
			}
			return s.fencer.publish(ctx, providerStatusChange{
				Current: current, NextName: current.Name, NextStatus: StatusArchived,
				Endpoints: endpoints, MaxBatch: s.batchMax(ctx), Archive: true,
				ArchiveReplacementID: replacementChannelID,
			})
		}
		affected, err := s.store.ArchiveProviderWithReplacement(ctx, sqlc.ArchiveProviderWithReplacementParams{
			ID: id, ReplacementChannelID: *replacementChannelID,
		})
		if err != nil {
			return StatusChangeResult{}, storeFailed(err, "replace and archive provider")
		}
		if affected == 0 {
			return StatusChangeResult{}, conflict("provider archive could not commit because the target or replacement changed")
		}
		return StatusChangeResult{}, nil
	}
	affectedRoutes, err := s.store.ListEnabledRoutesEmptiedByProvider(ctx, id)
	if err != nil {
		return StatusChangeResult{}, storeFailed(err, "check provider archive route impact")
	}
	if len(affectedRoutes) > 0 {
		return StatusChangeResult{}, conflict(fmt.Sprintf(
			"archiving provider would empty enabled route %q (%d); replace its channels or disable the route first",
			affectedRoutes[0].Name, affectedRoutes[0].ID,
		))
	}
	if s.fencer != nil {
		endpoints, err := s.listEndpoints(ctx, id)
		if err != nil {
			return StatusChangeResult{}, err
		}
		return s.fencer.publish(ctx, providerStatusChange{
			Current: current, NextName: current.Name, NextStatus: StatusArchived,
			Endpoints: endpoints, MaxBatch: s.batchMax(ctx), Archive: true,
		})
	}
	affected, err := s.store.ArchiveProviderCascade(ctx, id)
	if err != nil {
		return StatusChangeResult{}, storeFailed(err, "archive provider")
	}
	if affected == 0 {
		return StatusChangeResult{}, notFound("provider not found or already archived")
	}
	return StatusChangeResult{}, nil
}

// Restore 取消归档 provider：archived → disabled。名下渠道保持归档，需逐个恢复。
func (s *Service) Restore(ctx context.Context, id int64) (StatusChangeResult, error) {
	if id <= 0 {
		return StatusChangeResult{}, invalidArgument("id", "provider id must be positive")
	}
	if s.fencer != nil {
		current, err := s.store.GetProvider(ctx, id)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return StatusChangeResult{}, notFound("provider not found")
			}
			return StatusChangeResult{}, storeFailed(err, "get provider")
		}
		if current.Status != StatusArchived {
			return StatusChangeResult{}, notFound("provider not found or not archived")
		}
		endpoints, err := s.listEndpoints(ctx, id)
		if err != nil {
			return StatusChangeResult{}, err
		}
		return s.fencer.publish(ctx, providerStatusChange{
			Current: current, NextName: current.Name, NextStatus: StatusDisabled,
			Endpoints: endpoints, MaxBatch: s.batchMax(ctx), Restore: true,
		})
	}
	affected, err := s.store.RestoreProvider(ctx, id)
	if err != nil {
		return StatusChangeResult{}, storeFailed(err, "restore provider")
	}
	if affected == 0 {
		return StatusChangeResult{}, notFound("provider not found or not archived")
	}
	return StatusChangeResult{}, nil
}

type providerEndpointLister interface {
	ListProviderEndpointsByProvider(ctx context.Context, providerID int64) ([]sqlc.ProviderEndpoint, error)
}

func (s *Service) listEndpoints(ctx context.Context, providerID int64) ([]sqlc.ProviderEndpoint, error) {
	lister, ok := s.store.(providerEndpointLister)
	if !ok {
		return nil, storeFailed(errors.New("provider endpoint lister is not configured"), "list provider endpoints")
	}
	rows, err := lister.ListProviderEndpointsByProvider(ctx, providerID)
	if err != nil {
		return nil, storeFailed(err, "list provider endpoints")
	}
	return rows, nil
}

func toProvider(p sqlc.Provider) Provider {
	prov := Provider{
		ID:        p.ID,
		Slug:      p.Slug,
		Name:      p.Name,
		Status:    p.Status,
		CreatedAt: p.CreatedAt.Time,
		UpdatedAt: p.UpdatedAt.Time,
	}
	if p.ArchivedAt.Valid {
		t := p.ArchivedAt.Time
		prov.ArchivedAt = &t
	}
	return prov
}

// validateStatus 校验管理员可直接设置的状态：仅 enabled/disabled。archived 只能经 Archive 专用入口进入，
// 不允许通过 Create/Update 直接设置（否则绕过级联与护栏）。
func validateStatus(status string) error {
	switch status {
	case StatusEnabled, StatusDisabled:
		return nil
	default:
		return invalidArgument("status", fmt.Sprintf("status must be %q or %q", StatusEnabled, StatusDisabled))
	}
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
