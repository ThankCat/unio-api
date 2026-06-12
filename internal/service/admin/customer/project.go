package customer

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
)

// Project 表示后台项目（工作空间）视图。
type Project struct {
	ID        int64
	UserID    int64
	Name      string
	CreatedAt pgtype.Timestamptz
	UpdatedAt pgtype.Timestamptz
}

// ProjectListParams 表示项目分页查询参数；UserID 为 nil 时列全部。
type ProjectListParams struct {
	UserID *int64
	Limit  int32
	Offset int32
}

// ProjectStore 定义项目读取所需的存储能力。
type ProjectStore interface {
	ListProjectsPage(ctx context.Context, arg sqlc.ListProjectsPageParams) ([]sqlc.Project, error)
	CountProjects(ctx context.Context, userID pgtype.Int8) (int64, error)
	GetProjectByID(ctx context.Context, id int64) (sqlc.Project, error)
}

// ProjectService 提供 admin 项目只读查询。
type ProjectService struct {
	store ProjectStore
}

// NewProjectService 创建项目查询 service。
func NewProjectService(store ProjectStore) *ProjectService {
	if store == nil {
		panic("customer: project store is required")
	}
	return &ProjectService{store: store}
}

// List 分页倒序列出项目，并返回满足过滤条件的总数。
func (s *ProjectService) List(ctx context.Context, params ProjectListParams) ([]Project, int64, error) {
	userID := int8Narg(params.UserID)

	rows, err := s.store.ListProjectsPage(ctx, sqlc.ListProjectsPageParams{
		UserID:     userID,
		PageLimit:  params.Limit,
		PageOffset: params.Offset,
	})
	if err != nil {
		return nil, 0, storeFailed(err, "list projects")
	}

	total, err := s.store.CountProjects(ctx, userID)
	if err != nil {
		return nil, 0, storeFailed(err, "count projects")
	}

	projects := make([]Project, 0, len(rows))
	for _, row := range rows {
		projects = append(projects, projectFromSQLC(row))
	}

	return projects, total, nil
}

// Get 读取单个项目。
func (s *ProjectService) Get(ctx context.Context, id int64) (Project, error) {
	row, err := s.store.GetProjectByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Project{}, notFound("project not found")
		}
		return Project{}, storeFailed(err, "get project")
	}
	return projectFromSQLC(row), nil
}

func projectFromSQLC(row sqlc.Project) Project {
	return Project{
		ID:        row.ID,
		UserID:    row.UserID,
		Name:      row.Name,
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	}
}

// int8Narg 把可选 int64 过滤值转成 pgtype.Int8：nil → SQL NULL。
func int8Narg(v *int64) pgtype.Int8 {
	if v == nil {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: *v, Valid: true}
}
