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

	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
)

const (
	// StatusEnabled 表示 provider 启用。
	StatusEnabled = "enabled"
	// StatusDisabled 表示 provider 停用。
	StatusDisabled = "disabled"
)

// slugPattern 限定 provider slug：小写字母数字开头，允许小写字母、数字与连字符，长度 1..64。
var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// Store 定义 provider 管理所需的存储能力。
type Store interface {
	ListProvidersPage(ctx context.Context, arg sqlc.ListProvidersPageParams) ([]sqlc.Provider, error)
	CountProviders(ctx context.Context, arg sqlc.CountProvidersParams) (int64, error)
	GetProvider(ctx context.Context, id int64) (sqlc.Provider, error)
	CreateProvider(ctx context.Context, arg sqlc.CreateProviderParams) (sqlc.Provider, error)
	UpdateProvider(ctx context.Context, arg sqlc.UpdateProviderParams) (sqlc.Provider, error)
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
	ID        int64
	Slug      string
	Name      string
	Status    string
	CreatedAt time.Time
	UpdatedAt time.Time
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
	store Store
}

// NewService 创建 provider 管理服务。
func NewService(store Store) *Service {
	return &Service{store: store}
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

func toProvider(p sqlc.Provider) Provider {
	return Provider{
		ID:        p.ID,
		Slug:      p.Slug,
		Name:      p.Name,
		Status:    p.Status,
		CreatedAt: p.CreatedAt.Time,
		UpdatedAt: p.UpdatedAt.Time,
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
