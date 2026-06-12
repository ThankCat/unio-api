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

// APIKey 表示后台 API Key 视图；绝不含 key_hash。
type APIKey struct {
	ID         int64
	ProjectID  int64
	Name       string
	KeyPrefix  string
	Status     string
	SpendLimit *string // nil 表示不限额
	SpentTotal string
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

// APIKeyListParams 表示某项目下 API Key 分页查询参数。
type APIKeyListParams struct {
	ProjectID int64
	Limit     int32
	Offset    int32
}

// APIKeyCreateParams 表示创建 API Key 的业务参数。
type APIKeyCreateParams struct {
	ProjectID  int64
	Name       string
	ExpiresAt  *time.Time
	SpendLimit *string // nil/空串 表示不限额
}

// APIKeyUpdateParams 表示更新 API Key 的业务参数。
// 指针为 nil 表示该字段不变；SpendLimit 指向空串表示清除上限（改为不限额）。
type APIKeyUpdateParams struct {
	Disabled   *bool
	SpendLimit *string
}

// APIKeyStore 定义 API Key 管理所需的存储能力。
type APIKeyStore interface {
	ListAPIKeysByProjectPage(ctx context.Context, arg sqlc.ListAPIKeysByProjectPageParams) ([]sqlc.ListAPIKeysByProjectPageRow, error)
	CountAPIKeysByProject(ctx context.Context, projectID int64) (int64, error)
	GetAPIKeyByID(ctx context.Context, id int64) (sqlc.GetAPIKeyByIDRow, error)
	GetProjectByID(ctx context.Context, id int64) (sqlc.Project, error)
	CreateAPIKey(ctx context.Context, arg sqlc.CreateAPIKeyParams) (sqlc.ApiKey, error)
	SetAPIKeyDisabled(ctx context.Context, arg sqlc.SetAPIKeyDisabledParams) (sqlc.SetAPIKeyDisabledRow, error)
	RevokeAPIKey(ctx context.Context, id int64) (sqlc.RevokeAPIKeyRow, error)
	SetAPIKeySpendLimit(ctx context.Context, arg sqlc.SetAPIKeySpendLimitParams) (sqlc.SetAPIKeySpendLimitRow, error)
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

// List 列出某项目下的 API Key（倒序），并返回总数。
func (s *APIKeyService) List(ctx context.Context, params APIKeyListParams) ([]APIKey, int64, error) {
	rows, err := s.store.ListAPIKeysByProjectPage(ctx, sqlc.ListAPIKeysByProjectPageParams{
		ProjectID:  params.ProjectID,
		PageLimit:  params.Limit,
		PageOffset: params.Offset,
	})
	if err != nil {
		return nil, 0, storeFailed(err, "list api keys")
	}

	total, err := s.store.CountAPIKeysByProject(ctx, params.ProjectID)
	if err != nil {
		return nil, 0, storeFailed(err, "count api keys")
	}

	keys := make([]APIKey, 0, len(rows))
	for _, row := range rows {
		keys = append(keys, s.buildAPIKey(row.ID, row.ProjectID, row.Name, row.KeyPrefix, row.LastUsedAt, row.ExpiresAt, row.DisabledAt, row.RevokedAt, row.SpendLimit, row.SpentTotal, row.CreatedAt, row.UpdatedAt))
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
	return s.buildAPIKey(row.ID, row.ProjectID, row.Name, row.KeyPrefix, row.LastUsedAt, row.ExpiresAt, row.DisabledAt, row.RevokedAt, row.SpendLimit, row.SpentTotal, row.CreatedAt, row.UpdatedAt), nil
}

// Create 在项目下创建 API Key，并返回只展示一次的明文。
func (s *APIKeyService) Create(ctx context.Context, params APIKeyCreateParams) (CreatedAPIKey, error) {
	name := strings.TrimSpace(params.Name)
	if name == "" {
		return CreatedAPIKey{}, invalidArgument("name", "name must not be empty")
	}

	if _, err := s.store.GetProjectByID(ctx, params.ProjectID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return CreatedAPIKey{}, notFound("project not found")
		}
		return CreatedAPIKey{}, storeFailed(err, "lookup project for api key")
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
		ProjectID: params.ProjectID,
		Name:      name,
		KeyPrefix: generated.Prefix,
		KeyHash:   generated.Hash,
		ExpiresAt: expiresAt,
	})
	if err != nil {
		return CreatedAPIKey{}, storeFailed(err, "create api key")
	}

	view := s.buildAPIKey(created.ID, created.ProjectID, created.Name, created.KeyPrefix, created.LastUsedAt, created.ExpiresAt, created.DisabledAt, created.RevokedAt, created.SpendLimit, created.SpentTotal, created.CreatedAt, created.UpdatedAt)

	// 上限作为独立 UPDATE：CreateAPIKey 不接收 spend_limit，创建后按需补设。
	if spendLimit.Valid {
		updated, err := s.store.SetAPIKeySpendLimit(ctx, sqlc.SetAPIKeySpendLimitParams{
			ID:         created.ID,
			SpendLimit: spendLimit,
		})
		if err != nil {
			return CreatedAPIKey{}, storeFailed(err, "set api key spend limit")
		}
		view = s.buildAPIKey(updated.ID, updated.ProjectID, updated.Name, updated.KeyPrefix, updated.LastUsedAt, updated.ExpiresAt, updated.DisabledAt, updated.RevokedAt, updated.SpendLimit, updated.SpentTotal, updated.CreatedAt, updated.UpdatedAt)
	}

	return CreatedAPIKey{APIKey: view, Plaintext: generated.Plaintext}, nil
}

// Update 更新 API Key 的启停状态与费用上限（按需各自应用）。
func (s *APIKeyService) Update(ctx context.Context, id int64, params APIKeyUpdateParams) (APIKey, error) {
	if params.Disabled == nil && params.SpendLimit == nil {
		return APIKey{}, invalidArgument("body", "at least one of disabled or spend_limit must be provided")
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
		latest = s.buildAPIKey(row.ID, row.ProjectID, row.Name, row.KeyPrefix, row.LastUsedAt, row.ExpiresAt, row.DisabledAt, row.RevokedAt, row.SpendLimit, row.SpentTotal, row.CreatedAt, row.UpdatedAt)
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
		latest = s.buildAPIKey(row.ID, row.ProjectID, row.Name, row.KeyPrefix, row.LastUsedAt, row.ExpiresAt, row.DisabledAt, row.RevokedAt, row.SpendLimit, row.SpentTotal, row.CreatedAt, row.UpdatedAt)
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
	return s.buildAPIKey(row.ID, row.ProjectID, row.Name, row.KeyPrefix, row.LastUsedAt, row.ExpiresAt, row.DisabledAt, row.RevokedAt, row.SpendLimit, row.SpentTotal, row.CreatedAt, row.UpdatedAt), nil
}

// buildAPIKey 把各 sqlc row 的公共字段组装为对外 APIKey 视图，并计算状态。
func (s *APIKeyService) buildAPIKey(
	id, projectID int64,
	name, keyPrefix string,
	lastUsedAt, expiresAt, disabledAt, revokedAt pgtype.Timestamptz,
	spendLimit, spentTotal pgtype.Numeric,
	createdAt, updatedAt pgtype.Timestamptz,
) APIKey {
	return APIKey{
		ID:         id,
		ProjectID:  projectID,
		Name:       name,
		KeyPrefix:  keyPrefix,
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
