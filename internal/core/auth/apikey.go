package auth

import (
	"context"
	"errors"
	"time"

	"github.com/ThankCat/unio-api/internal/core/apikey"
	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

var (
	// ErrMissingAPIKey 表示请求没有提供 API Key。
	ErrMissingAPIKey = errors.New("missing api key")
	// ErrInvalidAPIKey 表示 API Key 不存在或无法匹配。
	ErrInvalidAPIKey = errors.New("invalid api key")
	// ErrAPIKeyRevoked 表示 API Key 已被永久吊销。
	ErrAPIKeyRevoked = errors.New("api key revoked")
	// ErrAPIKeyDisabled 表示 API Key 被临时禁用。
	ErrAPIKeyDisabled = errors.New("api key disabled")
	// ErrAPIKeyExpired 表示 API Key 已经过期。
	ErrAPIKeyExpired = errors.New("api key expired")
	// ErrAPIKeySpendLimitReached 表示 API Key 已达生命周期累计费用上限（M7）。
	ErrAPIKeySpendLimitReached = errors.New("api key spend limit reached")
)

// APIKeyPrincipal 表示 API Key 认证成功后的请求身份。
type APIKeyPrincipal struct {
	APIKeyID  int64
	UserID    int64
	KeyPrefix string

	// RouteID 是 Key 绑定的线路 ID（线路必填，恒有值）；运行时据此解析线路，无默认回落。
	RouteID *int64

	// RPMLimit/TPMLimit/RPDLimit 是本把 Key 的令牌级限流上限（P2-8）：
	// nil 表示「继承全局默认」，0 表示「显式不限」，>0 表示具体上限（每分钟请求/每分钟 token/每日请求）。
	RPMLimit *int64
	TPMLimit *int64
	RPDLimit *int64
}

// APIKeyStore 定义 API Key 认证所需的存储查询和更新能力。
type APIKeyStore interface {
	GetAPIKeyByHash(ctx context.Context, keyHash string) (sqlc.GetAPIKeyByHashRow, error)
	UpdateAPIKeyLastUsedAt(ctx context.Context, arg sqlc.UpdateAPIKeyLastUsedAtParams) error
}

// APIKeyAuthenticator 负责校验 API Key 并生成认证身份。
type APIKeyAuthenticator struct {
	store APIKeyStore
	now   func() time.Time
}

// NewAPIKeyAuthenticator 创建 APIKeyAuthenticator。
func NewAPIKeyAuthenticator(store APIKeyStore) *APIKeyAuthenticator {
	return &APIKeyAuthenticator{
		store: store,
		now:   time.Now,
	}
}

// AuthenticateAPIKey 校验明文 API Key，并返回认证后的请求身份。
func (a *APIKeyAuthenticator) AuthenticateAPIKey(ctx context.Context, plaintext string) (*APIKeyPrincipal, error) {
	if plaintext == "" {
		return nil, failure.Wrap(
			failure.CodeAuthMissingAPIKey,
			ErrMissingAPIKey,
			failure.WithMessage(ErrMissingAPIKey.Error()),
		)
	}

	keyHash := apikey.Hash(plaintext)

	key, err := a.store.GetAPIKeyByHash(ctx, keyHash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, failure.Wrap(
				failure.CodeAuthInvalidAPIKey,
				ErrInvalidAPIKey,
				failure.WithMessage(ErrInvalidAPIKey.Error()),
			)
		}
		return nil, failure.Wrap(
			failure.CodeAuthStoreFailed,
			err,
			failure.WithMessage("lookup api key"),
		)
	}

	if key.RevokedAt.Valid {
		return nil, failure.Wrap(
			failure.CodeAuthAPIKeyRevoked,
			ErrAPIKeyRevoked,
			failure.WithMessage(ErrAPIKeyRevoked.Error()),
		)
	}

	if key.DisabledAt.Valid {
		return nil, failure.Wrap(
			failure.CodeAuthAPIKeyDisabled,
			ErrAPIKeyDisabled,
			failure.WithMessage(ErrAPIKeyDisabled.Error()),
		)
	}

	if key.ExpiresAt.Valid && !key.ExpiresAt.Time.After(a.now()) {
		return nil, failure.Wrap(
			failure.CodeAuthAPIKeyExpired,
			ErrAPIKeyExpired,
			failure.WithMessage(ErrAPIKeyExpired.Error()),
		)
	}

	// 费用上限闸门（M7）：spend_limit_reached 由 SQL 层按 spent_total >= spend_limit 判定，
	// 这里只读结论，认证路径不做 NUMERIC 运算。计数器在 settlement capture 时累加，
	// 故近上限时的并发请求可能有轻微超额，符合「生命周期软上限」语义。
	if key.SpendLimitReached.Valid && key.SpendLimitReached.Bool {
		return nil, failure.Wrap(
			failure.CodeAuthAPIKeySpendLimitReached,
			ErrAPIKeySpendLimitReached,
			failure.WithMessage(ErrAPIKeySpendLimitReached.Error()),
		)
	}

	// TODO(阶段3/production): [GAP-3-001] 每次认证同步更新 last_used_at 会放大数据库写入；后续评估节流、异步或批量更新策略。
	// 更新最后使用时间
	usedAt := a.now()
	if err := a.store.UpdateAPIKeyLastUsedAt(ctx, sqlc.UpdateAPIKeyLastUsedAtParams{
		LastUsedAt: pgtype.Timestamptz{Time: usedAt, Valid: true},
		ID:         key.ID,
	}); err != nil {
		return nil, failure.Wrap(
			failure.CodeAuthStoreFailed,
			err,
			failure.WithMessage("update api key last used at"),
		)
	}

	// route_id 在 DB 层 NOT NULL（线路必填），恒有值；取地址供下游统一以 *int64 消费。
	routeID := key.RouteID
	return &APIKeyPrincipal{
		APIKeyID:  key.ID,
		UserID:    key.UserID,
		KeyPrefix: key.KeyPrefix,
		RouteID:   &routeID,
		RPMLimit:  int4Ptr(key.RpmLimit),
		TPMLimit:  int4Ptr(key.TpmLimit),
		RPDLimit:  int4Ptr(key.RpdLimit),
	}, nil
}

// int4Ptr 把可空 pgtype.Int4 转成 *int64（限流上限可空，nil=继承全局默认）。
func int4Ptr(v pgtype.Int4) *int64 {
	if !v.Valid {
		return nil
	}
	out := int64(v.Int32)
	return &out
}

// TODO(阶段3/production): [GAP-3-002] 补齐 API Key revoke、disable、list 和审计日志能力，确保后台能安全管理 customer API key。
