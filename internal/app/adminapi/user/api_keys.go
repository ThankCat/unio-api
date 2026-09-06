package user

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi/adminhttp"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
	"github.com/ThankCat/unio-gateway/internal/service/admin/customer"
)

func parseOptionalExpiresAt(raw json.RawMessage) (*time.Time, bool, error) {
	if len(raw) == 0 {
		return nil, false, nil
	}
	if string(raw) == "null" {
		return nil, true, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, false, failure.New(
			failure.CodeAdminInvalidArgument,
			failure.WithMessage("expires_at must be RFC3339 timestamp or null"),
			failure.WithField("field", "expires_at"),
		)
	}
	if strings.TrimSpace(s) == "" {
		return nil, true, nil
	}
	t, err := adminhttp.ParseOptionalRFC3339("expires_at", &s)
	if err != nil {
		return nil, false, err
	}
	return t, true, nil
}

// APIKeyService 定义 adminapi 管理 API Key 所需的最小能力（M7 客户管理）。
type APIKeyService interface {
	List(ctx context.Context, params customer.APIKeyListParams) ([]customer.APIKey, int64, error)
	Get(ctx context.Context, id int64) (customer.APIKey, error)
	Create(ctx context.Context, params customer.APIKeyCreateParams) (customer.CreatedAPIKey, error)
	Update(ctx context.Context, id int64, params customer.APIKeyUpdateParams) (customer.APIKey, error)
	Revoke(ctx context.Context, id int64) (customer.APIKey, error)
	Delete(ctx context.Context, id int64) error
}

// apiKeyDTO 是 API Key 响应体，既不含明文也不含 key_hash。
// 明文只出现在 createdAPIKeyDTO——即创建接口的 201 响应，此后任何接口都取不回。
type apiKeyDTO struct {
	ID         int64   `json:"id"`
	UserID     int64   `json:"user_id"`
	Name       string  `json:"name"`
	KeyPrefix  string  `json:"key_prefix"`
	KeySuffix  *string `json:"key_suffix"`
	Status     string  `json:"status"`
	SpendLimit *string `json:"spend_limit"`
	SpentTotal string  `json:"spent_total"`
	// 这里没有 Key 级限流字段：DEC-027 之后限流全部归线路，按 (线路, 用户) 计数。
	LastUsedAt *string `json:"last_used_at"`
	ExpiresAt  *string `json:"expires_at"`
	DisabledAt *string `json:"disabled_at"`
	RevokedAt  *string `json:"revoked_at"`
	CreatedAt  string  `json:"created_at"`
	UpdatedAt  string  `json:"updated_at"`
}

// createdAPIKeyDTO 只用于创建接口的 201 响应。
// plaintext 单独挂在这里而不是放进 apiKeyDTO，是为了让契约自己说清楚：
// 明文只有这一次机会，客户端在别处看到的永远是 key_prefix。
type createdAPIKeyDTO struct {
	apiKeyDTO
	Plaintext string `json:"plaintext"`
}

type createAPIKeyRequest struct {
	Name       string  `json:"name"`
	ExpiresAt  *string `json:"expires_at"`  // RFC3339，可选
	SpendLimit *string `json:"spend_limit"` // 可选，不传/空串表示不限额
}

// updateAPIKeyRequest 是 PATCH 请求体。
// disabled: 非空时启停；spend_limit: 不传=不变，空串=清除上限，否则设为该值。
// name: 非空时更新名称；expires_at: 字段缺省=不变，null=永不过期，RFC3339=设为该时间。
// 限流已归线路（DEC-027），此处不再接收 rate_limits。
type updateAPIKeyRequest struct {
	Disabled   *bool           `json:"disabled"`
	SpendLimit *string         `json:"spend_limit"`
	Name       *string         `json:"name"`
	ExpiresAt  json.RawMessage `json:"expires_at"`
}

type apiKeysHandler struct {
	service APIKeyService
}

// create 在用户（路径 {id} 为 user id）下创建 API Key，返回一次性明文。
func (h *apiKeysHandler) create(w http.ResponseWriter, r *http.Request) {
	userID, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	var req createAPIKeyRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	expiresAt, err := adminhttp.ParseOptionalRFC3339("expires_at", req.ExpiresAt)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	createParams := customer.APIKeyCreateParams{
		UserID:     userID,
		Name:       req.Name,
		ExpiresAt:  expiresAt,
		SpendLimit: req.SpendLimit,
	}

	created, err := h.service.Create(r.Context(), createParams)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	// 明文不落库，这个响应是客户端唯一一次拿到它的机会。
	adminhttp.WriteData(w, http.StatusCreated, createdAPIKeyDTO{
		apiKeyDTO: toAPIKeyDTO(created.APIKey),
		Plaintext: created.Plaintext,
	})
}

// update 更新 API Key 的启停与费用上限。
func (h *apiKeysHandler) update(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	var req updateAPIKeyRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	expiresAt, expiresProvided, err := parseOptionalExpiresAt(req.ExpiresAt)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	updateParams := customer.APIKeyUpdateParams{
		Disabled:        req.Disabled,
		SpendLimit:      req.SpendLimit,
		Name:            req.Name,
		ExpiresAt:       expiresAt,
		ExpiresProvided: expiresProvided,
	}

	key, err := h.service.Update(r.Context(), id, updateParams)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	adminhttp.WriteData(w, http.StatusOK, toAPIKeyDTO(key))
}

// revoke 永久吊销 API Key（软失效、保留行与调用历史，不可逆）；子资源 POST。
func (h *apiKeysHandler) revoke(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	key, err := h.service.Revoke(r.Context(), id)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	adminhttp.WriteData(w, http.StatusOK, toAPIKeyDTO(key))
}

// delete 物理删除 API Key，用于清理误建/未使用的 Key；已产生调用历史时返回 409，提示改用吊销。
func (h *apiKeysHandler) delete(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	if err := h.service.Delete(r.Context(), id); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func toAPIKeyDTO(k customer.APIKey) apiKeyDTO {
	return apiKeyDTO{
		ID:         k.ID,
		UserID:     k.UserID,
		Name:       k.Name,
		KeyPrefix:  k.KeyPrefix,
		KeySuffix:  k.KeySuffix,
		Status:     k.Status,
		SpendLimit: k.SpendLimit,
		SpentTotal: k.SpentTotal,
		LastUsedAt: adminhttp.RFC3339Ptr(k.LastUsedAt),
		ExpiresAt:  adminhttp.RFC3339Ptr(k.ExpiresAt),
		DisabledAt: adminhttp.RFC3339Ptr(k.DisabledAt),
		RevokedAt:  adminhttp.RFC3339Ptr(k.RevokedAt),
		CreatedAt:  adminhttp.RFC3339(k.CreatedAt.Time),
		UpdatedAt:  adminhttp.RFC3339(k.UpdatedAt.Time),
	}
}
