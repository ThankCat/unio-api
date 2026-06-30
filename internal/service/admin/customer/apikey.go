package customer

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-api/internal/core/apikey"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
)

// API Key 对外状态：revoked > disabled > expired > active（按优先级判定）。
const (
	APIKeyStatusActive   = "active"
	APIKeyStatusDisabled = "disabled"
	APIKeyStatusRevoked  = "revoked"
	APIKeyStatusExpired  = "expired"
)

// APIKey 表示后台 API Key 视图；含完整明文 key 供多次复制（产品决策），绝不含 key_hash。
type APIKey struct {
	ID        int64
	UserID    int64
	Name      string
	KeyPrefix string
	// KeyPlaintext 是完整明文 key（产品决策：留存明文供多次复制）；nil 表示历史 key 不可回显。
	KeyPlaintext *string
	Status       string
	SpendLimit   *string // nil 表示不限额
	SpentTotal   string
	RouteID      *int64 // Key 绑定的线路 ID（线路必填，恒有值）
	// RPMLimit/TPMLimit/RPDLimit 是本把 Key 的令牌级限流上限（P2-8）：
	// nil 表示继承全局默认，0 表示显式不限，>0 表示具体上限。
	RPMLimit   *int64
	TPMLimit   *int64
	RPDLimit   *int64
	LastUsedAt *time.Time
	ExpiresAt  *time.Time
	DisabledAt *time.Time
	RevokedAt  *time.Time
	CreatedAt  pgtype.Timestamptz
	UpdatedAt  pgtype.Timestamptz
}

// CreatedAPIKey 表示创建成功的一次性结果：含只展示一次的明文。
type CreatedAPIKey struct {
	APIKey
	Plaintext string
}

// APIKeyListParams 表示某用户下 API Key 分页查询参数。
type APIKeyListParams struct {
	UserID int64
	Limit  int32
	Offset int32
}

// APIKeyCreateParams 表示创建 API Key 的业务参数。
type APIKeyCreateParams struct {
	UserID     int64
	Name       string
	ExpiresAt  *time.Time
	SpendLimit *string // nil/空串 表示不限额
	RouteID    *int64  // 必填：线路必须显式绑定（缺失/非正数 → route is required）
	// RateLimitsProvided=true 时按 RPM/TPM/RPD 设置令牌级限流（各值 nil=继承全局默认，0=不限，>0=具体上限）。
	RateLimitsProvided bool
	RPMLimit           *int64
	TPMLimit           *int64
	RPDLimit           *int64
}

// APIKeyUpdateParams 表示更新 API Key 的业务参数。
// 指针为 nil 表示该字段不变；SpendLimit 指向空串表示清除上限（改为不限额）。
// RouteProvided=true 时按 RouteID 设置线路（RouteID 为 nil 表示清除绑定）。
// RateLimitsProvided=true 时按 RPM/TPM/RPD 原子设置令牌级限流（各值 nil=继承全局默认，0=不限，>0=具体上限）。
type APIKeyUpdateParams struct {
	Disabled           *bool
	SpendLimit         *string
	RouteID            *int64
	RouteProvided      bool
	RateLimitsProvided bool
	RPMLimit           *int64
	TPMLimit           *int64
	RPDLimit           *int64
}

// APIKeyStore 定义 API Key 管理所需的存储能力。
type APIKeyStore interface {
	ListAPIKeysByUserPage(ctx context.Context, arg sqlc.ListAPIKeysByUserPageParams) ([]sqlc.ListAPIKeysByUserPageRow, error)
	CountAPIKeysByUser(ctx context.Context, userID int64) (int64, error)
	GetAPIKeyByID(ctx context.Context, id int64) (sqlc.GetAPIKeyByIDRow, error)
	GetUserByID(ctx context.Context, id int64) (sqlc.GetUserByIDRow, error)
	CreateAPIKey(ctx context.Context, arg sqlc.CreateAPIKeyParams) (sqlc.ApiKey, error)
	SetAPIKeyDisabled(ctx context.Context, arg sqlc.SetAPIKeyDisabledParams) (sqlc.SetAPIKeyDisabledRow, error)
	RevokeAPIKey(ctx context.Context, id int64) (sqlc.RevokeAPIKeyRow, error)
	SetAPIKeySpendLimit(ctx context.Context, arg sqlc.SetAPIKeySpendLimitParams) (sqlc.SetAPIKeySpendLimitRow, error)
	SetAPIKeyRoute(ctx context.Context, arg sqlc.SetAPIKeyRouteParams) (sqlc.SetAPIKeyRouteRow, error)
	SetAPIKeyRateLimits(ctx context.Context, arg sqlc.SetAPIKeyRateLimitsParams) (sqlc.SetAPIKeyRateLimitsRow, error)
}

// APIKeyService 提供 admin API Key 管理。
type APIKeyService struct {
	store APIKeyStore
	now   func() time.Time
}

// NewAPIKeyService 创建 API Key 管理 service。
func NewAPIKeyService(store APIKeyStore) *APIKeyService {
	if store == nil {
		panic("customer: api key store is required")
	}
	return &APIKeyService{store: store, now: time.Now}
}

// List 列出某用户下的 API Key（倒序），并返回总数。
func (s *APIKeyService) List(ctx context.Context, params APIKeyListParams) ([]APIKey, int64, error) {
	rows, err := s.store.ListAPIKeysByUserPage(ctx, sqlc.ListAPIKeysByUserPageParams{
		UserID:     params.UserID,
		PageLimit:  params.Limit,
		PageOffset: params.Offset,
	})
	if err != nil {
		return nil, 0, storeFailed(err, "list api keys")
	}

	total, err := s.store.CountAPIKeysByUser(ctx, params.UserID)
	if err != nil {
		return nil, 0, storeFailed(err, "count api keys")
	}

	keys := make([]APIKey, 0, len(rows))
	for _, row := range rows {
		keys = append(keys, s.buildAPIKey(row.ID, row.UserID, row.Name, row.KeyPrefix, row.KeyPlaintext, row.LastUsedAt, row.ExpiresAt, row.DisabledAt, row.RevokedAt, row.SpendLimit, row.SpentTotal, row.RouteID, row.RpmLimit, row.TpmLimit, row.RpdLimit, row.CreatedAt, row.UpdatedAt))
	}

	return keys, total, nil
}

// Get 读取单把 API Key。
func (s *APIKeyService) Get(ctx context.Context, id int64) (APIKey, error) {
	row, err := s.store.GetAPIKeyByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return APIKey{}, notFound("api key not found")
		}
		return APIKey{}, storeFailed(err, "get api key")
	}
	return s.buildAPIKey(row.ID, row.UserID, row.Name, row.KeyPrefix, row.KeyPlaintext, row.LastUsedAt, row.ExpiresAt, row.DisabledAt, row.RevokedAt, row.SpendLimit, row.SpentTotal, row.RouteID, row.RpmLimit, row.TpmLimit, row.RpdLimit, row.CreatedAt, row.UpdatedAt), nil
}

// Create 在用户下创建 API Key，并返回只展示一次的明文。
func (s *APIKeyService) Create(ctx context.Context, params APIKeyCreateParams) (CreatedAPIKey, error) {
	name := strings.TrimSpace(params.Name)
	if name == "" {
		return CreatedAPIKey{}, invalidArgument("name", "name must not be empty")
	}

	// 线路必填：API Key 必须显式绑定一条线路（无默认线路回落）。DB NOT NULL 是最终兜底。
	if params.RouteID == nil || *params.RouteID <= 0 {
		return CreatedAPIKey{}, invalidArgument("route_id", "route is required")
	}

	if _, err := s.store.GetUserByID(ctx, params.UserID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return CreatedAPIKey{}, notFound("user not found")
		}
		return CreatedAPIKey{}, storeFailed(err, "lookup user for api key")
	}

	spendLimit, err := parseOptionalMoney("spend_limit", params.SpendLimit)
	if err != nil {
		return CreatedAPIKey{}, err
	}

	generated, err := apikey.Generate()
	if err != nil {
		return CreatedAPIKey{}, storeFailed(err, "generate api key")
	}

	expiresAt := pgtype.Timestamptz{}
	if params.ExpiresAt != nil {
		expiresAt = pgtype.Timestamptz{Time: *params.ExpiresAt, Valid: true}
	}

	created, err := s.store.CreateAPIKey(ctx, sqlc.CreateAPIKeyParams{
		UserID:       params.UserID,
		Name:         name,
		KeyPrefix:    generated.Prefix,
		KeyHash:      generated.Hash,
		KeyPlaintext: pgtype.Text{String: generated.Plaintext, Valid: true},
		ExpiresAt:    expiresAt,
		RouteID:      *params.RouteID,
	})
	if err != nil {
		return CreatedAPIKey{}, storeFailed(err, "create api key")
	}

	view := s.buildAPIKey(created.ID, created.UserID, created.Name, created.KeyPrefix, created.KeyPlaintext, created.LastUsedAt, created.ExpiresAt, created.DisabledAt, created.RevokedAt, created.SpendLimit, created.SpentTotal, created.RouteID, created.RpmLimit, created.TpmLimit, created.RpdLimit, created.CreatedAt, created.UpdatedAt)

	// 上限作为独立 UPDATE：CreateAPIKey 不接收 spend_limit，创建后按需补设。
	if spendLimit.Valid {
		updated, err := s.store.SetAPIKeySpendLimit(ctx, sqlc.SetAPIKeySpendLimitParams{
			ID:         created.ID,
			SpendLimit: spendLimit,
		})
		if err != nil {
			return CreatedAPIKey{}, storeFailed(err, "set api key spend limit")
		}
		view = s.buildAPIKey(updated.ID, updated.UserID, updated.Name, updated.KeyPrefix, updated.KeyPlaintext, updated.LastUsedAt, updated.ExpiresAt, updated.DisabledAt, updated.RevokedAt, updated.SpendLimit, updated.SpentTotal, updated.RouteID, updated.RpmLimit, updated.TpmLimit, updated.RpdLimit, updated.CreatedAt, updated.UpdatedAt)
	}

	// 令牌级限流同样作为独立 UPDATE（CreateAPIKey 不接收 rpm/tpm/rpd），创建后按需补设（P2-8）。
	if params.RateLimitsProvided {
		limited, err := s.store.SetAPIKeyRateLimits(ctx, sqlc.SetAPIKeyRateLimitsParams{
			ID:       created.ID,
			RpmLimit: int4Narg(params.RPMLimit),
			TpmLimit: int4Narg(params.TPMLimit),
			RpdLimit: int4Narg(params.RPDLimit),
		})
		if err != nil {
			return CreatedAPIKey{}, storeFailed(err, "set api key rate limits")
		}
		view = s.buildAPIKey(limited.ID, limited.UserID, limited.Name, limited.KeyPrefix, limited.KeyPlaintext, limited.LastUsedAt, limited.ExpiresAt, limited.DisabledAt, limited.RevokedAt, limited.SpendLimit, limited.SpentTotal, limited.RouteID, limited.RpmLimit, limited.TpmLimit, limited.RpdLimit, limited.CreatedAt, limited.UpdatedAt)
	}

	return CreatedAPIKey{APIKey: view, Plaintext: generated.Plaintext}, nil
}

// Update 更新 API Key 的启停状态与费用上限（按需各自应用）。
func (s *APIKeyService) Update(ctx context.Context, id int64, params APIKeyUpdateParams) (APIKey, error) {
	if params.Disabled == nil && params.SpendLimit == nil && !params.RouteProvided && !params.RateLimitsProvided {
		return APIKey{}, invalidArgument("body", "at least one of disabled, spend_limit, route_id or rate limits must be provided")
	}

	current, err := s.store.GetAPIKeyByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return APIKey{}, notFound("api key not found")
		}
		return APIKey{}, storeFailed(err, "get api key")
	}
	// 已吊销不可逆，禁止再改。
	if current.RevokedAt.Valid {
		return APIKey{}, invalidArgument("id", "api key is revoked and cannot be updated")
	}

	var latest APIKey
	applied := false

	if params.Disabled != nil {
		disabledAt := pgtype.Timestamptz{}
		if *params.Disabled {
			disabledAt = pgtype.Timestamptz{Time: s.now(), Valid: true}
		}
		row, err := s.store.SetAPIKeyDisabled(ctx, sqlc.SetAPIKeyDisabledParams{
			ID:         id,
			DisabledAt: disabledAt,
		})
		if err != nil {
			return APIKey{}, storeFailed(err, "set api key disabled")
		}
		latest = s.buildAPIKey(row.ID, row.UserID, row.Name, row.KeyPrefix, row.KeyPlaintext, row.LastUsedAt, row.ExpiresAt, row.DisabledAt, row.RevokedAt, row.SpendLimit, row.SpentTotal, row.RouteID, row.RpmLimit, row.TpmLimit, row.RpdLimit, row.CreatedAt, row.UpdatedAt)
		applied = true
	}

	if params.SpendLimit != nil {
		spendLimit, err := parseOptionalMoney("spend_limit", params.SpendLimit)
		if err != nil {
			return APIKey{}, err
		}
		row, err := s.store.SetAPIKeySpendLimit(ctx, sqlc.SetAPIKeySpendLimitParams{
			ID:         id,
			SpendLimit: spendLimit,
		})
		if err != nil {
			return APIKey{}, storeFailed(err, "set api key spend limit")
		}
		latest = s.buildAPIKey(row.ID, row.UserID, row.Name, row.KeyPrefix, row.KeyPlaintext, row.LastUsedAt, row.ExpiresAt, row.DisabledAt, row.RevokedAt, row.SpendLimit, row.SpentTotal, row.RouteID, row.RpmLimit, row.TpmLimit, row.RpdLimit, row.CreatedAt, row.UpdatedAt)
		applied = true
	}

	if params.RouteProvided {
		// 线路必填：禁止清空绑定（route_id=null），只能改绑到另一条线路。
		if params.RouteID == nil || *params.RouteID <= 0 {
			return APIKey{}, invalidArgument("route_id", "route is required")
		}
		row, err := s.store.SetAPIKeyRoute(ctx, sqlc.SetAPIKeyRouteParams{
			ID:      id,
			RouteID: *params.RouteID,
		})
		if err != nil {
			return APIKey{}, storeFailed(err, "set api key route")
		}
		latest = s.buildAPIKey(row.ID, row.UserID, row.Name, row.KeyPrefix, row.KeyPlaintext, row.LastUsedAt, row.ExpiresAt, row.DisabledAt, row.RevokedAt, row.SpendLimit, row.SpentTotal, row.RouteID, row.RpmLimit, row.TpmLimit, row.RpdLimit, row.CreatedAt, row.UpdatedAt)
		applied = true
	}

	if params.RateLimitsProvided {
		row, err := s.store.SetAPIKeyRateLimits(ctx, sqlc.SetAPIKeyRateLimitsParams{
			ID:       id,
			RpmLimit: int4Narg(params.RPMLimit),
			TpmLimit: int4Narg(params.TPMLimit),
			RpdLimit: int4Narg(params.RPDLimit),
		})
		if err != nil {
			return APIKey{}, storeFailed(err, "set api key rate limits")
		}
		latest = s.buildAPIKey(row.ID, row.UserID, row.Name, row.KeyPrefix, row.KeyPlaintext, row.LastUsedAt, row.ExpiresAt, row.DisabledAt, row.RevokedAt, row.SpendLimit, row.SpentTotal, row.RouteID, row.RpmLimit, row.TpmLimit, row.RpdLimit, row.CreatedAt, row.UpdatedAt)
		applied = true
	}

	if !applied {
		return s.Get(ctx, id)
	}
	return latest, nil
}

// Revoke 永久吊销 API Key（不可逆）。
func (s *APIKeyService) Revoke(ctx context.Context, id int64) (APIKey, error) {
	row, err := s.store.RevokeAPIKey(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// 不存在或已吊销（query 带 revoked_at IS NULL 条件）。
			return APIKey{}, notFound("api key not found or already revoked")
		}
		return APIKey{}, storeFailed(err, "revoke api key")
	}
	return s.buildAPIKey(row.ID, row.UserID, row.Name, row.KeyPrefix, row.KeyPlaintext, row.LastUsedAt, row.ExpiresAt, row.DisabledAt, row.RevokedAt, row.SpendLimit, row.SpentTotal, row.RouteID, row.RpmLimit, row.TpmLimit, row.RpdLimit, row.CreatedAt, row.UpdatedAt), nil
}

// buildAPIKey 把各 sqlc row 的公共字段组装为对外 APIKey 视图，并计算状态。
func (s *APIKeyService) buildAPIKey(
	id, userID int64,
	name, keyPrefix string,
	keyPlaintext pgtype.Text,
	lastUsedAt, expiresAt, disabledAt, revokedAt pgtype.Timestamptz,
	spendLimit, spentTotal pgtype.Numeric,
	routeID int64,
	rpmLimit, tpmLimit, rpdLimit pgtype.Int4,
	createdAt, updatedAt pgtype.Timestamptz,
) APIKey {
	// route_id 在 DB 层 NOT NULL（线路必填），恒有值；取地址以 *int64 对外表达。
	boundRouteID := routeID
	return APIKey{
		ID:           id,
		UserID:       userID,
		Name:         name,
		KeyPrefix:    keyPrefix,
		KeyPlaintext: textPtr(keyPlaintext),
		Status:       s.computeStatus(disabledAt, revokedAt, expiresAt),
		SpendLimit:   numericPtr(spendLimit),
		SpentTotal:   numericString(spentTotal),
		RouteID:      &boundRouteID,
		RPMLimit:     int4ToPtr(rpmLimit),
		TPMLimit:     int4ToPtr(tpmLimit),
		RPDLimit:     int4ToPtr(rpdLimit),
		LastUsedAt:   timePtr(lastUsedAt),
		ExpiresAt:    timePtr(expiresAt),
		DisabledAt:   timePtr(disabledAt),
		RevokedAt:    timePtr(revokedAt),
		CreatedAt:    createdAt,
		UpdatedAt:    updatedAt,
	}
}

// textPtr 把可空 pgtype.Text 转成 *string（nil=历史 key 无明文，不可回显）。
func textPtr(v pgtype.Text) *string {
	if !v.Valid {
		return nil
	}
	out := v.String
	return &out
}

// int4ToPtr 把可空 pgtype.Int4 转成 *int64（限流上限可空，nil=继承全局默认）。
func int4ToPtr(v pgtype.Int4) *int64 {
	if !v.Valid {
		return nil
	}
	out := int64(v.Int32)
	return &out
}

// int4Narg 把 *int64 转成可空 pgtype.Int4 写入参数（nil=NULL 继承全局默认；含 0=显式不限）。
func int4Narg(v *int64) pgtype.Int4 {
	if v == nil {
		return pgtype.Int4{Valid: false}
	}
	return pgtype.Int4{Int32: int32(*v), Valid: true}
}

func (s *APIKeyService) computeStatus(disabledAt, revokedAt, expiresAt pgtype.Timestamptz) string {
	switch {
	case revokedAt.Valid:
		return APIKeyStatusRevoked
	case disabledAt.Valid:
		return APIKeyStatusDisabled
	case expiresAt.Valid && !expiresAt.Time.After(s.now()):
		return APIKeyStatusExpired
	default:
		return APIKeyStatusActive
	}
}

// parseOptionalMoney 解析可选金额：nil/空串 → SQL NULL（不限额）；否则按非负十进制解析。
func parseOptionalMoney(field string, raw *string) (pgtype.Numeric, error) {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return pgtype.Numeric{Valid: false}, nil
	}
	return parseMoney(field, *raw)
}
