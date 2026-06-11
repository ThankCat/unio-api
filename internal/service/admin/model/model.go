// Package model 编排 admin 管理端的 model 读写。
//
// 只做校验、存储编排与 sqlc row → 领域事实映射；不暴露 sqlc row 给上层。
// admin 手工创建的模型固定 source=manual，models.dev 同步永不覆盖（见 sql/queries/models.sql）。
package model

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
	// StatusEnabled 表示 model 启用（对外可见、可路由）。
	StatusEnabled = "enabled"
	// StatusDisabled 表示 model 停用。
	StatusDisabled = "disabled"
)

// modelIDPattern 限定对外 model_id：字母数字开头，允许字母、数字、`.`、`_`、`:`、`-`，长度 1..128。
var modelIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// Store 定义 model 管理所需的存储能力。
type Store interface {
	ListModelsPage(ctx context.Context, arg sqlc.ListModelsPageParams) ([]sqlc.Model, error)
	CountModels(ctx context.Context, arg sqlc.CountModelsParams) (int64, error)
	LookupModelByID(ctx context.Context, id int64) (sqlc.Model, error)
	CreateModel(ctx context.Context, arg sqlc.CreateModelParams) (sqlc.Model, error)
	UpdateModel(ctx context.Context, arg sqlc.UpdateModelParams) (sqlc.Model, error)
}

// ListParams 是分页/过滤列出 model 的入参；Status、Query 为空表示不过滤。
type ListParams struct {
	Status string
	Query  string
	Limit  int32
	Offset int32
}

// ListResult 是分页列表结果：当前页条目 + 过滤后总数。
type ListResult struct {
	Items []Model
	Total int64
}

// Model 是 admin 视角的 model 业务事实。
type Model struct {
	ID              int64
	ModelID         string
	DisplayName     string
	OwnedBy         string
	Status          string
	Lab             *string
	MaxOutputTokens *int64
	Source          string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// CreateInput 是创建 model 的入参；source 由服务层固定为 manual。
type CreateInput struct {
	ModelID         string
	DisplayName     string
	OwnedBy         string
	Status          string
	Lab             *string
	MaxOutputTokens *int64
}

// UpdateInput 是更新 model 的入参；model_id 作为对外稳定标识不可变，不在此修改。
type UpdateInput struct {
	ID              int64
	DisplayName     string
	OwnedBy         string
	Status          string
	Lab             *string
	MaxOutputTokens *int64
}

// Service 编排 model 管理读写。
type Service struct {
	store Store
}

// NewService 创建 model 管理服务。
func NewService(store Store) *Service {
	return &Service{store: store}
}

// List 按 params 过滤分页列出 model，并返回过滤后的总数。
func (s *Service) List(ctx context.Context, params ListParams) (ListResult, error) {
	status := textParam(params.Status)
	q := textParam(params.Query)

	rows, err := s.store.ListModelsPage(ctx, sqlc.ListModelsPageParams{
		Status:     status,
		Q:          q,
		PageLimit:  params.Limit,
		PageOffset: params.Offset,
	})
	if err != nil {
		return ListResult{}, storeFailed(err, "list models")
	}

	total, err := s.store.CountModels(ctx, sqlc.CountModelsParams{
		Status: status,
		Q:      q,
	})
	if err != nil {
		return ListResult{}, storeFailed(err, "count models")
	}

	items := make([]Model, 0, len(rows))
	for _, row := range rows {
		items = append(items, toModel(row))
	}

	return ListResult{Items: items, Total: total}, nil
}

// Get 按内部主键读取单个 model。
func (s *Service) Get(ctx context.Context, id int64) (Model, error) {
	if id <= 0 {
		return Model{}, invalidArgument("id", "model id must be positive")
	}

	row, err := s.store.LookupModelByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Model{}, notFound("model not found")
		}
		return Model{}, storeFailed(err, "get model")
	}

	return toModel(row), nil
}

// Create 创建 model；model_id 重复返回 conflict。
func (s *Service) Create(ctx context.Context, in CreateInput) (Model, error) {
	modelID := strings.TrimSpace(in.ModelID)
	displayName := strings.TrimSpace(in.DisplayName)
	ownedBy := strings.TrimSpace(in.OwnedBy)
	status := strings.TrimSpace(in.Status)

	if !modelIDPattern.MatchString(modelID) {
		return Model{}, invalidArgument("model_id", "model_id must match ^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$")
	}
	if displayName == "" {
		return Model{}, invalidArgument("display_name", "display_name is required")
	}
	if ownedBy == "" {
		return Model{}, invalidArgument("owned_by", "owned_by is required")
	}
	if err := validateStatus(status); err != nil {
		return Model{}, err
	}
	if err := validateMaxOutputTokens(in.MaxOutputTokens); err != nil {
		return Model{}, err
	}

	row, err := s.store.CreateModel(ctx, sqlc.CreateModelParams{
		ModelID:         modelID,
		DisplayName:     displayName,
		OwnedBy:         ownedBy,
		Status:          status,
		Lab:             textParam(trimPtr(in.Lab)),
		MaxOutputTokens: int8Param(in.MaxOutputTokens),
	})
	if err != nil {
		if isUniqueViolation(err) {
			return Model{}, conflict("model_id already exists")
		}
		return Model{}, storeFailed(err, "create model")
	}

	return toModel(row), nil
}

// Update 更新 model 的展示元数据与状态；目标不存在返回 not_found。
func (s *Service) Update(ctx context.Context, in UpdateInput) (Model, error) {
	if in.ID <= 0 {
		return Model{}, invalidArgument("id", "model id must be positive")
	}
	displayName := strings.TrimSpace(in.DisplayName)
	ownedBy := strings.TrimSpace(in.OwnedBy)
	status := strings.TrimSpace(in.Status)

	if displayName == "" {
		return Model{}, invalidArgument("display_name", "display_name is required")
	}
	if ownedBy == "" {
		return Model{}, invalidArgument("owned_by", "owned_by is required")
	}
	if err := validateStatus(status); err != nil {
		return Model{}, err
	}
	if err := validateMaxOutputTokens(in.MaxOutputTokens); err != nil {
		return Model{}, err
	}

	row, err := s.store.UpdateModel(ctx, sqlc.UpdateModelParams{
		ID:              in.ID,
		DisplayName:     displayName,
		OwnedBy:         ownedBy,
		Status:          status,
		Lab:             textParam(trimPtr(in.Lab)),
		MaxOutputTokens: int8Param(in.MaxOutputTokens),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Model{}, notFound("model not found")
		}
		return Model{}, storeFailed(err, "update model")
	}

	return toModel(row), nil
}

func toModel(m sqlc.Model) Model {
	out := Model{
		ID:          m.ID,
		ModelID:     m.ModelID,
		DisplayName: m.DisplayName,
		OwnedBy:     m.OwnedBy,
		Status:      m.Status,
		Source:      m.Source,
		CreatedAt:   m.CreatedAt.Time,
		UpdatedAt:   m.UpdatedAt.Time,
	}
	if m.Lab.Valid {
		lab := m.Lab.String
		out.Lab = &lab
	}
	if m.MaxOutputTokens.Valid {
		v := m.MaxOutputTokens.Int64
		out.MaxOutputTokens = &v
	}
	return out
}

func validateStatus(status string) error {
	switch status {
	case StatusEnabled, StatusDisabled:
		return nil
	default:
		return invalidArgument("status", fmt.Sprintf("status must be %q or %q", StatusEnabled, StatusDisabled))
	}
}

func validateMaxOutputTokens(v *int64) error {
	if v != nil && *v <= 0 {
		return invalidArgument("max_output_tokens", "max_output_tokens must be > 0 when set")
	}
	return nil
}

// trimPtr 取指针指向的字符串并 TrimSpace；nil 返回空串（textParam 会转成 NULL）。
func trimPtr(s *string) string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(*s)
}

// textParam 把空串转成 NULL（不写值），非空转成有值 pgtype.Text。
func textParam(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}

// int8Param 把 nil 转成 NULL，非 nil 转成有值 pgtype.Int8。
func int8Param(v *int64) pgtype.Int8 {
	if v == nil {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: *v, Valid: true}
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
