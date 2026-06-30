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

// apiKeyDTO 是 API Key 响应体；含完整明文 key 供多次复制（产品决策），绝不含 key_hash。
type apiKeyDTO struct {
	ID        int64  `json:"id"`
	UserID    int64  `json:"user_id"`
	Name      string `json:"name"`
	KeyPrefix string `json:"key_prefix"`
	// Plaintext 是完整明文 key（产品决策：留存供多次复制）；null 表示历史 key 不可回显。
	Plaintext  *string `json:"plaintext"`
	Status     string  `json:"status"`
	SpendLimit *string `json:"spend_limit"`
	SpentTotal string  `json:"spent_total"`
	RouteID    *int64  `json:"route_id"`
	// RPMLimit/TPMLimit/RPDLimit：令牌级限流上限（P2-8）。null=继承全局默认，0=不限，>0=具体上限。
	RPMLimit   *int64  `json:"rpm_limit"`
	TPMLimit   *int64  `json:"tpm_limit"`
	RPDLimit   *int64  `json:"rpd_limit"`
	LastUsedAt *string `json:"last_used_at"`
	ExpiresAt  *string `json:"expires_at"`
	DisabledAt *string `json:"disabled_at"`
	RevokedAt  *string `json:"revoked_at"`
	CreatedAt  string  `json:"created_at"`
	UpdatedAt  string  `json:"updated_at"`
}

// rateLimitsRequest 是令牌级限流的可选嵌套对象（P2-8）。
// 整个对象缺省=不变；存在即原子替换三维：各字段 null/缺省=继承全局默认(NULL)，数字（含 0）=显式设定（0=不限）。
type rateLimitsRequest struct {
	RPM *int64 `json:"rpm"`
	TPM *int64 `json:"tpm"`
	RPD *int64 `json:"rpd"`
}

type createAPIKeyRequest struct {
	Name       string             `json:"name"`
	ExpiresAt  *string            `json:"expires_at"`  // RFC3339，可选
	SpendLimit *string            `json:"spend_limit"` // 可选，不传/空串表示不限额
	RouteID    *int64             `json:"route_id"`    // 可选线路绑定；不传表示不绑（回落项目默认/内置经济）
	RateLimits *rateLimitsRequest `json:"rate_limits"` // 可选令牌级限流；不传表示不设（全继承全局默认）
}

// updateAPIKeyRequest 是 PATCH 请求体。
// disabled: 非空时启停；spend_limit: 不传=不变，空串=清除上限，否则设为该值。
// route_id: 字段缺省=不变，null=清除绑定，数字=设为该线路。
// rate_limits: 对象缺省=不变，存在即原子替换三维限流。
type updateAPIKeyRequest struct {
	Disabled   *bool              `json:"disabled"`
	SpendLimit *string            `json:"spend_limit"`
	RouteID    json.RawMessage    `json:"route_id"`
	RateLimits *rateLimitsRequest `json:"rate_limits"`
}

// validateRateLimits 校验限流值非负（限流上限不能为负数）。
func validateRateLimits(rl *rateLimitsRequest) error {
	if rl == nil {
		return nil
	}
	for field, v := range map[string]*int64{"rpm": rl.RPM, "tpm": rl.TPM, "rpd": rl.RPD} {
		if v != nil && *v < 0 {
			return failure.New(
				failure.CodeAdminInvalidArgument,
				failure.WithMessage("rate limit must be a non-negative integer (0 means unlimited)"),
				failure.WithField("field", "rate_limits."+field),
			)
		}
	}
	return nil
}

type apiKeysHandler struct {
	service APIKeyService
}

// listByUser 列出某用户（路径 {id} 为 user id）下的 API Key。
func (h *apiKeysHandler) listByUser(w http.ResponseWriter, r *http.Request) {
	userID, err := pathID(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	page := parsePage(r)
	items, total, err := h.service.List(r.Context(), customer.APIKeyListParams{
		UserID: userID,
		Limit:  page.Limit(),
		Offset: page.Offset(),
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

// create 在用户（路径 {id} 为 user id）下创建 API Key，返回一次性明文。
func (h *apiKeysHandler) create(w http.ResponseWriter, r *http.Request) {
	userID, err := pathID(r)
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

	if err := validateRateLimits(req.RateLimits); err != nil {
		writeServiceError(w, err)
		return
	}

	createParams := customer.APIKeyCreateParams{
		UserID:     userID,
		Name:       req.Name,
		ExpiresAt:  expiresAt,
		SpendLimit: req.SpendLimit,
		RouteID:    req.RouteID,
	}
	if req.RateLimits != nil {
		createParams.RateLimitsProvided = true
		createParams.RPMLimit = req.RateLimits.RPM
		createParams.TPMLimit = req.RateLimits.TPM
		createParams.RPDLimit = req.RateLimits.RPD
	}

	created, err := h.service.Create(r.Context(), createParams)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	dto := toAPIKeyDTO(created.APIKey)
	// 创建结果以服务返回的权威明文为准（也已落库供后续多次复制）。
	dto.Plaintext = &created.Plaintext
	writeData(w, http.StatusCreated, dto)
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

	if err := validateRateLimits(req.RateLimits); err != nil {
		writeServiceError(w, err)
		return
	}

	updateParams := customer.APIKeyUpdateParams{
		Disabled:      req.Disabled,
		SpendLimit:    req.SpendLimit,
		RouteID:       routeID,
		RouteProvided: routeProvided,
	}
	if req.RateLimits != nil {
		updateParams.RateLimitsProvided = true
		updateParams.RPMLimit = req.RateLimits.RPM
		updateParams.TPMLimit = req.RateLimits.TPM
		updateParams.RPDLimit = req.RateLimits.RPD
	}

	key, err := h.service.Update(r.Context(), id, updateParams)
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
		UserID:     k.UserID,
		Name:       k.Name,
		KeyPrefix:  k.KeyPrefix,
		Plaintext:  k.KeyPlaintext,
		Status:     k.Status,
		SpendLimit: k.SpendLimit,
		SpentTotal: k.SpentTotal,
		RouteID:    k.RouteID,
		RPMLimit:   k.RPMLimit,
		TPMLimit:   k.TPMLimit,
		RPDLimit:   k.RPDLimit,
		LastUsedAt: rfc3339Ptr(k.LastUsedAt),
		ExpiresAt:  rfc3339Ptr(k.ExpiresAt),
		DisabledAt: rfc3339Ptr(k.DisabledAt),
		RevokedAt:  rfc3339Ptr(k.RevokedAt),
		CreatedAt:  rfc3339(k.CreatedAt.Time),
		UpdatedAt:  rfc3339(k.UpdatedAt.Time),
	}
}
