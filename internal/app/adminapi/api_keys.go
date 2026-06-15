package adminapi

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/httpx"
	"github.com/ThankCat/unio-api/internal/service/admin/customer"
)

// parseOptionalRouteID 解析 PATCH 的 route_id：字段缺省→(nil,false) 不变；null→(nil,true) 清除；数字→(&n,true) 设置。
func parseOptionalRouteID(raw json.RawMessage) (*int64, bool, error) {
	if len(raw) == 0 {
		return nil, false, nil
	}
	if string(raw) == "null" {
		return nil, true, nil
	}
	var id int64
	if err := json.Unmarshal(raw, &id); err != nil {
		return nil, false, failure.New(
			failure.CodeAdminInvalidArgument,
			failure.WithMessage("route_id must be an integer or null"),
			failure.WithField("field", "route_id"),
		)
	}
	return &id, true, nil
}

// APIKeyService 定义 adminapi 管理 API Key 所需的最小能力（M7 客户管理）。
type APIKeyService interface {
	List(ctx context.Context, params customer.APIKeyListParams) ([]customer.APIKey, int64, error)
	Get(ctx context.Context, id int64) (customer.APIKey, error)
	Create(ctx context.Context, params customer.APIKeyCreateParams) (customer.CreatedAPIKey, error)
	Update(ctx context.Context, id int64, params customer.APIKeyUpdateParams) (customer.APIKey, error)
	Revoke(ctx context.Context, id int64) (customer.APIKey, error)
}

// apiKeyDTO 是 API Key 响应体；绝不含 key_hash。
type apiKeyDTO struct {
	ID         int64   `json:"id"`
	ProjectID  int64   `json:"project_id"`
	Name       string  `json:"name"`
	KeyPrefix  string  `json:"key_prefix"`
	Status     string  `json:"status"`
	SpendLimit *string `json:"spend_limit"`
	SpentTotal string  `json:"spent_total"`
	RouteID    *int64  `json:"route_id"`
	LastUsedAt *string `json:"last_used_at"`
	ExpiresAt  *string `json:"expires_at"`
	DisabledAt *string `json:"disabled_at"`
	RevokedAt  *string `json:"revoked_at"`
	CreatedAt  string  `json:"created_at"`
	UpdatedAt  string  `json:"updated_at"`
}

// createdAPIKeyDTO 是创建结果响应体：含只展示一次的明文 plaintext。
type createdAPIKeyDTO struct {
	apiKeyDTO
	Plaintext string `json:"plaintext"`
}

type createAPIKeyRequest struct {
	Name       string  `json:"name"`
	ExpiresAt  *string `json:"expires_at"`  // RFC3339，可选
	SpendLimit *string `json:"spend_limit"` // 可选，不传/空串表示不限额
	RouteID    *int64  `json:"route_id"`    // 可选线路绑定；不传表示不绑（回落项目默认/内置经济）
}

// updateAPIKeyRequest 是 PATCH 请求体。
// disabled: 非空时启停；spend_limit: 不传=不变，空串=清除上限，否则设为该值。
// route_id: 字段缺省=不变，null=清除绑定，数字=设为该线路。
type updateAPIKeyRequest struct {
	Disabled   *bool           `json:"disabled"`
	SpendLimit *string         `json:"spend_limit"`
	RouteID    json.RawMessage `json:"route_id"`
}

type apiKeysHandler struct {
	service APIKeyService
}

// listByProject 列出某项目（路径 {id} 为 project id）下的 API Key。
func (h *apiKeysHandler) listByProject(w http.ResponseWriter, r *http.Request) {
	projectID, err := pathID(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	page := parsePage(r)
	items, total, err := h.service.List(r.Context(), customer.APIKeyListParams{
		ProjectID: projectID,
		Limit:     page.Limit(),
		Offset:    page.Offset(),
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}

	dtos := make([]apiKeyDTO, 0, len(items))
	for _, k := range items {
		dtos = append(dtos, toAPIKeyDTO(k))
	}
	writeList(w, http.StatusOK, dtos, page, total)
}

// create 在项目（路径 {id} 为 project id）下创建 API Key，返回一次性明文。
func (h *apiKeysHandler) create(w http.ResponseWriter, r *http.Request) {
	projectID, err := pathID(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	var req createAPIKeyRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		writeServiceError(w, err)
		return
	}

	expiresAt, err := parseOptionalRFC3339("expires_at", req.ExpiresAt)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	created, err := h.service.Create(r.Context(), customer.APIKeyCreateParams{
		ProjectID:  projectID,
		Name:       req.Name,
		ExpiresAt:  expiresAt,
		SpendLimit: req.SpendLimit,
		RouteID:    req.RouteID,
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}

	writeData(w, http.StatusCreated, createdAPIKeyDTO{
		apiKeyDTO: toAPIKeyDTO(created.APIKey),
		Plaintext: created.Plaintext,
	})
}

// get 读取单把 API Key（路径 {id} 为 api key id）。
func (h *apiKeysHandler) get(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	key, err := h.service.Get(r.Context(), id)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	writeData(w, http.StatusOK, toAPIKeyDTO(key))
}

// update 更新 API Key 的启停与费用上限。
func (h *apiKeysHandler) update(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	var req updateAPIKeyRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		writeServiceError(w, err)
		return
	}

	routeID, routeProvided, err := parseOptionalRouteID(req.RouteID)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	key, err := h.service.Update(r.Context(), id, customer.APIKeyUpdateParams{
		Disabled:      req.Disabled,
		SpendLimit:    req.SpendLimit,
		RouteID:       routeID,
		RouteProvided: routeProvided,
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}

	writeData(w, http.StatusOK, toAPIKeyDTO(key))
}

// revoke 永久吊销 API Key（不可逆）。
func (h *apiKeysHandler) revoke(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	key, err := h.service.Revoke(r.Context(), id)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	writeData(w, http.StatusOK, toAPIKeyDTO(key))
}

func toAPIKeyDTO(k customer.APIKey) apiKeyDTO {
	return apiKeyDTO{
		ID:         k.ID,
		ProjectID:  k.ProjectID,
		Name:       k.Name,
		KeyPrefix:  k.KeyPrefix,
		Status:     k.Status,
		SpendLimit: k.SpendLimit,
		SpentTotal: k.SpentTotal,
		RouteID:    k.RouteID,
		LastUsedAt: rfc3339Ptr(k.LastUsedAt),
		ExpiresAt:  rfc3339Ptr(k.ExpiresAt),
		DisabledAt: rfc3339Ptr(k.DisabledAt),
		RevokedAt:  rfc3339Ptr(k.RevokedAt),
		CreatedAt:  rfc3339(k.CreatedAt.Time),
		UpdatedAt:  rfc3339(k.UpdatedAt.Time),
	}
}
