package customer

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/core/apikey"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

// API Key 对外状态：revoked > disabled > expired > active（按优先级判定）。
const (
	APIKeyStatusActive   = "active"
	APIKeyStatusDisabled = "disabled"
	APIKeyStatusRevoked  = "revoked"
	APIKeyStatusExpired  = "expired"
)

// APIKey 表示后台 API Key 视图。
// 既不含明文也不含 key_hash：明文只在 CreatedAPIKey 里出现一次，之后连 admin 也取不回。
type APIKey struct {
	ID        int64
	UserID    int64
	Name      string
	KeyPrefix string
	// KeySuffix 为 nil 表示这把 key 建于本列之前，展示层退化成只有前缀的掩码。
	KeySuffix  *string
	Status     string
	SpendLimit *string // nil 表示不限额
	SpentTotal string
	// 这里没有 Key 级限流：DEC-027 之后限流全部归线路，按 (线路, 用户) 计数。
	LastUsedAt *time.Time
	ExpiresAt  *time.Time
	DisabledAt *time.Time
	RevokedAt  *time.Time
	CreatedAt  pgtype.Timestamptz
	UpdatedAt  pgtype.Timestamptz
}

// CreatedAPIKey 表示创建成功的一次性结果。
// Plaintext 是明文唯一一次露面的地方：不落库、不写日志，响应发出去就再也拿不回来。
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
// 限流已归线路（DEC-027），创建 Key 不再配置令牌级限流。
type APIKeyCreateParams struct {
	UserID     int64
	Name       string
	ExpiresAt  *time.Time
	SpendLimit *string // nil/空串 表示不限额
}

// APIKeyUpdateParams 表示更新 API Key 的业务参数。
// 指针为 nil 表示该字段不变；SpendLimit 指向空串表示清除上限（改为不限额）。
// ExpiresProvided=true 时按 ExpiresAt 设置过期（ExpiresAt 为 nil 表示永不过期）。
// 限流已归用户，更新 Key 不再配置令牌级限流。
type APIKeyUpdateParams struct {
	Disabled        *bool
	SpendLimit      *string
	Name            *string
	ExpiresAt       *time.Time
	ExpiresProvided bool
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
	DeleteAPIKey(ctx context.Context, id int64) (int64, error)
	SetAPIKeySpendLimit(ctx context.Context, arg sqlc.SetAPIKeySpendLimitParams) (sqlc.SetAPIKeySpendLimitRow, error)
	SetAPIKeyName(ctx context.Context, arg sqlc.SetAPIKeyNameParams) (sqlc.SetAPIKeyNameRow, error)
	SetAPIKeyExpiresAt(ctx context.Context, arg sqlc.SetAPIKeyExpiresAtParams) (sqlc.SetAPIKeyExpiresAtRow, error)
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
		keys = append(keys, s.buildAPIKey(row.ID, row.UserID, row.Name, row.KeyPrefix, row.KeySuffix, row.LastUsedAt, row.ExpiresAt, row.DisabledAt, row.RevokedAt, row.SpendLimit, row.SpentTotal, row.CreatedAt, row.UpdatedAt))
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
	return s.buildAPIKey(row.ID, row.UserID, row.Name, row.KeyPrefix, row.KeySuffix, row.LastUsedAt, row.ExpiresAt, row.DisabledAt, row.RevokedAt, row.SpendLimit, row.SpentTotal, row.CreatedAt, row.UpdatedAt), nil
}

// Create 在用户下创建 API Key，并返回只展示一次的明文。
func (s *APIKeyService) Create(ctx context.Context, params APIKeyCreateParams) (CreatedAPIKey, error) {
	name := strings.TrimSpace(params.Name)
	if name == "" {
		return CreatedAPIKey{}, invalidArgument("name", "name must not be empty")
	}

	// 线路必填：API Key 必须显式绑定一条线路（无默认线路回落）。DB NOT NULL 是最终兜底。

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
		UserID:    params.UserID,
		Name:      name,
		KeyPrefix: generated.Prefix,
		KeySuffix: pgtype.Text{String: generated.Suffix, Valid: true},
		KeyHash:   generated.Hash,
		ExpiresAt: expiresAt,
	})
	if err != nil {
		return CreatedAPIKey{}, storeFailed(err, "create api key")
	}

	view := s.buildAPIKey(created.ID, created.UserID, created.Name, created.KeyPrefix, created.KeySuffix, created.LastUsedAt, created.ExpiresAt, created.DisabledAt, created.RevokedAt, created.SpendLimit, created.SpentTotal, created.CreatedAt, created.UpdatedAt)

	// 上限作为独立 UPDATE：CreateAPIKey 不接收 spend_limit，创建后按需补设。
	if spendLimit.Valid {
		updated, err := s.store.SetAPIKeySpendLimit(ctx, sqlc.SetAPIKeySpendLimitParams{
			ID:         created.ID,
			SpendLimit: spendLimit,
		})
		if err != nil {
			return CreatedAPIKey{}, storeFailed(err, "set api key spend limit")
		}
		view = s.buildAPIKey(updated.ID, updated.UserID, updated.Name, updated.KeyPrefix, updated.KeySuffix, updated.LastUsedAt, updated.ExpiresAt, updated.DisabledAt, updated.RevokedAt, updated.SpendLimit, updated.SpentTotal, updated.CreatedAt, updated.UpdatedAt)
	}

	return CreatedAPIKey{APIKey: view, Plaintext: generated.Plaintext}, nil
}

// Update 更新 API Key 的启停、费用上限、线路、名称与过期时间（按需各自应用）。
func (s *APIKeyService) Update(ctx context.Context, id int64, params APIKeyUpdateParams) (APIKey, error) {
	if params.Disabled == nil && params.SpendLimit == nil && params.Name == nil && !params.ExpiresProvided {
		return APIKey{}, invalidArgument("body", "at least one updatable field must be provided")
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
		latest = s.buildAPIKey(row.ID, row.UserID, row.Name, row.KeyPrefix, row.KeySuffix, row.LastUsedAt, row.ExpiresAt, row.DisabledAt, row.RevokedAt, row.SpendLimit, row.SpentTotal, row.CreatedAt, row.UpdatedAt)
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
		latest = s.buildAPIKey(row.ID, row.UserID, row.Name, row.KeyPrefix, row.KeySuffix, row.LastUsedAt, row.ExpiresAt, row.DisabledAt, row.RevokedAt, row.SpendLimit, row.SpentTotal, row.CreatedAt, row.UpdatedAt)
		applied = true
	}

	if params.Name != nil {
		name := strings.TrimSpace(*params.Name)
		if name == "" {
			return APIKey{}, invalidArgument("name", "name must not be empty")
		}
		row, err := s.store.SetAPIKeyName(ctx, sqlc.SetAPIKeyNameParams{
			ID:   id,
			Name: name,
		})
		if err != nil {
			return APIKey{}, storeFailed(err, "set api key name")
		}
		latest = s.buildAPIKey(row.ID, row.UserID, row.Name, row.KeyPrefix, row.KeySuffix, row.LastUsedAt, row.ExpiresAt, row.DisabledAt, row.RevokedAt, row.SpendLimit, row.SpentTotal, row.CreatedAt, row.UpdatedAt)
		applied = true
	}

	if params.ExpiresProvided {
		expiresAt := pgtype.Timestamptz{}
		if params.ExpiresAt != nil {
			expiresAt = pgtype.Timestamptz{Time: *params.ExpiresAt, Valid: true}
		}
		row, err := s.store.SetAPIKeyExpiresAt(ctx, sqlc.SetAPIKeyExpiresAtParams{
			ID:        id,
			ExpiresAt: expiresAt,
		})
		if err != nil {
			return APIKey{}, storeFailed(err, "set api key expires at")
		}
		latest = s.buildAPIKey(row.ID, row.UserID, row.Name, row.KeyPrefix, row.KeySuffix, row.LastUsedAt, row.ExpiresAt, row.DisabledAt, row.RevokedAt, row.SpendLimit, row.SpentTotal, row.CreatedAt, row.UpdatedAt)
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
	return s.buildAPIKey(row.ID, row.UserID, row.Name, row.KeyPrefix, row.KeySuffix, row.LastUsedAt, row.ExpiresAt, row.DisabledAt, row.RevokedAt, row.SpendLimit, row.SpentTotal, row.CreatedAt, row.UpdatedAt), nil
}

// Delete 物理删除 API Key，用于清理误建/未使用的 Key（与 channel/model/provider/route 的删除语义对齐）。
// 一旦该 Key 已产生调用历史（request_records NO ACTION 外键引用），DB 拒绝删除（23503），
// 降级为 conflict，提示改用吊销——保住计费/审计链路。目标不存在返回 not_found。
func (s *APIKeyService) Delete(ctx context.Context, id int64) error {
	if id <= 0 {
		return invalidArgument("id", "api key id must be positive")
	}

	affected, err := s.store.DeleteAPIKey(ctx, id)
	if err != nil {
		if isForeignKeyViolation(err) {
			return conflict("api key has usage history; revoke it instead of deleting")
		}
		return storeFailed(err, "delete api key")
	}
	if affected == 0 {
		return notFound("api key not found")
	}

	return nil
}

// buildAPIKey 把各 sqlc row 的公共字段组装为对外 APIKey 视图，并计算状态。
func (s *APIKeyService) buildAPIKey(
	id, userID int64,
	name, keyPrefix string,
	keySuffix pgtype.Text,
	lastUsedAt, expiresAt, disabledAt, revokedAt pgtype.Timestamptz,
	spendLimit, spentTotal pgtype.Numeric,
	createdAt, updatedAt pgtype.Timestamptz,
) APIKey {
	return APIKey{
		ID:         id,
		UserID:     userID,
		Name:       name,
		KeyPrefix:  keyPrefix,
		KeySuffix:  textPtr(keySuffix),
		Status:     s.computeStatus(disabledAt, revokedAt, expiresAt),
		SpendLimit: numericPtr(spendLimit),
		SpentTotal: numericString(spentTotal),
		LastUsedAt: timePtr(lastUsedAt),
		ExpiresAt:  timePtr(expiresAt),
		DisabledAt: timePtr(disabledAt),
		RevokedAt:  timePtr(revokedAt),
		CreatedAt:  createdAt,
		UpdatedAt:  updatedAt,
	}
}

// int4ToPtr 把可空 pgtype.Int4 转成 *int64（限流上限可空，nil=继承全局默认）。
func int4ToPtr(v pgtype.Int4) *int64 {
	if !v.Valid {
		return nil
	}
	out := int64(v.Int32)
	return &out
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
