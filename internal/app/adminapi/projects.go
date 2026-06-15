package adminapi

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/ThankCat/unio-api/internal/platform/httpx"
	"github.com/ThankCat/unio-api/internal/service/admin/customer"
)

// ProjectService 定义 adminapi 查询项目与设置默认线路所需的最小能力（M7 客户管理 + 阶段 15）。
type ProjectService interface {
	List(ctx context.Context, params customer.ProjectListParams) ([]customer.Project, int64, error)
	Get(ctx context.Context, id int64) (customer.Project, error)
	SetDefaultRoute(ctx context.Context, id int64, routeID *int64) (customer.Project, error)
}

// projectDTO 是项目（工作空间）响应体。
type projectDTO struct {
	ID             int64  `json:"id"`
	UserID         int64  `json:"user_id"`
	Name           string `json:"name"`
	DefaultRouteID *int64 `json:"default_route_id"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

// updateProjectRequest 是项目 PATCH 请求体；default_route_id：缺省=不变，null=清除，数字=设为该线路。
type updateProjectRequest struct {
	DefaultRouteID json.RawMessage `json:"default_route_id"`
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

// update 设置/清除项目默认线路（阶段 15）。
func (h *projectsHandler) update(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	var req updateProjectRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		writeServiceError(w, err)
		return
	}

	routeID, _, err := parseOptionalRouteID(req.DefaultRouteID)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	p, err := h.service.SetDefaultRoute(r.Context(), id, routeID)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	writeData(w, http.StatusOK, toProjectDTO(p))
}

func toProjectDTO(p customer.Project) projectDTO {
	return projectDTO{
		ID:             p.ID,
		UserID:         p.UserID,
		Name:           p.Name,
		DefaultRouteID: p.DefaultRouteID,
		CreatedAt:      rfc3339(p.CreatedAt.Time),
		UpdatedAt:      rfc3339(p.UpdatedAt.Time),
	}
}
