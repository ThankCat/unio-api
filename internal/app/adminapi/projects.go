package adminapi

import (
	"context"
	"net/http"

	"github.com/ThankCat/unio-api/internal/service/admin/customer"
)

// ProjectService 定义 adminapi 查询项目所需的最小能力（M7 客户管理）。
type ProjectService interface {
	List(ctx context.Context, params customer.ProjectListParams) ([]customer.Project, int64, error)
	Get(ctx context.Context, id int64) (customer.Project, error)
}

// projectDTO 是项目（工作空间）响应体。
type projectDTO struct {
	ID        int64  `json:"id"`
	UserID    int64  `json:"user_id"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type projectsHandler struct {
	service ProjectService
}

func (h *projectsHandler) list(w http.ResponseWriter, r *http.Request) {
	userID, err := optionalInt64Query(r, "user_id")
	if err != nil {
		writeServiceError(w, err)
		return
	}

	page := parsePage(r)
	items, total, err := h.service.List(r.Context(), customer.ProjectListParams{
		UserID: userID,
		Limit:  page.Limit(),
		Offset: page.Offset(),
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}

	dtos := make([]projectDTO, 0, len(items))
	for _, p := range items {
		dtos = append(dtos, toProjectDTO(p))
	}
	writeList(w, http.StatusOK, dtos, page, total)
}

func (h *projectsHandler) get(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	p, err := h.service.Get(r.Context(), id)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	writeData(w, http.StatusOK, toProjectDTO(p))
}

func toProjectDTO(p customer.Project) projectDTO {
	return projectDTO{
		ID:        p.ID,
		UserID:    p.UserID,
		Name:      p.Name,
		CreatedAt: rfc3339(p.CreatedAt.Time),
		UpdatedAt: rfc3339(p.UpdatedAt.Time),
	}
}
