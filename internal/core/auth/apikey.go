package auth

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/core/apikey"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

// defaultLastUsedAtWriteInterval 是 last_used_at 的最小写入间隔。该字段只是观测值（Console/Admin 展示
// 「最近使用」），认证热路径上每请求同步写会形成热点行写放大；同一 Key 在窗口内的重复认证跳过写入，
// 展示精度退化为分钟级。窗口判定用行上已有的 last_used_at，多实例之间天然一致，无需进程内状态。
const defaultLastUsedAtWriteInterval = time.Minute

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

	// RPMLimit/RPDLimit/ConcurrencyLimit 是限流上限，取自 Key 所属的用户，按用户计数：
	// nil 表示「继承全局默认限流」，0 表示「显式不限」，>0 表示具体上限。
	// 配额挂在用户上而非 Key 上：同一用户的多把 Key 共享同一份额度，
	// 否则多开几把 Key 就能绕过限流。api_keys 自身的旧限流列已废弃、不再参与认证。
	RPMLimit         *int64
	RPDLimit         *int64
	ConcurrencyLimit *int64
}

// APIKeyStore 定义 API Key 认证所需的存储查询和更新能力。
type APIKeyStore interface {
	GetAPIKeyByHash(ctx context.Context, keyHash string) (sqlc.GetAPIKeyByHashRow, error)
	UpdateAPIKeyLastUsedAt(ctx context.Context, arg sqlc.UpdateAPIKeyLastUsedAtParams) error
}

// APIKeyAuthenticator 负责校验 API Key 并生成认证身份。
type APIKeyAuthenticator struct {
	store                   APIKeyStore
	now                     func() time.Time
	logger                  *zap.Logger
	lastUsedAtWriteInterval time.Duration
}

// NewAPIKeyAuthenticator 创建 APIKeyAuthenticator。
func NewAPIKeyAuthenticator(store APIKeyStore) *APIKeyAuthenticator {
	return &APIKeyAuthenticator{
		store:                   store,
		now:                     time.Now,
		logger:                  zap.NewNop(),
		lastUsedAtWriteInterval: defaultLastUsedAtWriteInterval,
	}
}

// WithLogger 注入日志器，用于记录 last_used_at 降级放行等非致命事件。
func (a *APIKeyAuthenticator) WithLogger(logger *zap.Logger) *APIKeyAuthenticator {
	if a != nil && logger != nil {
		a.logger = logger
	}
	return a
}

// WithLastUsedAtWriteInterval 覆盖 last_used_at 最小写入间隔；<= 0 表示每次认证都写。
func (a *APIKeyAuthenticator) WithLastUsedAtWriteInterval(interval time.Duration) *APIKeyAuthenticator {
	if a != nil {
		a.lastUsedAtWriteInterval = interval
	}
	return a
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

	// 更新最后使用时间：按写入间隔节流；写失败只记日志不拒绝请求——
	// 一个纯观测字段的写失败不应把本可正常服务的请求变成 500（数据库只读降级时尤其如此）。
	usedAt := a.now()
	if a.shouldWriteLastUsedAt(key.LastUsedAt, usedAt) {
		if err := a.store.UpdateAPIKeyLastUsedAt(ctx, sqlc.UpdateAPIKeyLastUsedAtParams{
			LastUsedAt: pgtype.Timestamptz{Time: usedAt, Valid: true},
			ID:         key.ID,
		}); err != nil {
			a.logger.Warn("api key last_used_at update skipped",
				append([]zap.Field{zap.Int64("api_key_id", key.ID)}, failure.LogFields(err)...)...)
		}
	}

	return &APIKeyPrincipal{
		APIKeyID:         key.ID,
		UserID:           key.UserID,
		KeyPrefix:        key.KeyPrefix,
		RPMLimit:         int4Ptr(key.UserRpmLimit),
		RPDLimit:         int4Ptr(key.UserRpdLimit),
		ConcurrencyLimit: int4Ptr(key.UserConcurrencyLimit),
	}, nil
}

// shouldWriteLastUsedAt 判断本次认证是否需要刷新 last_used_at：从未记录、或距上次记录已超过写入间隔。
func (a *APIKeyAuthenticator) shouldWriteLastUsedAt(last pgtype.Timestamptz, now time.Time) bool {
	if a.lastUsedAtWriteInterval <= 0 || !last.Valid {
		return true
	}
	return now.Sub(last.Time) >= a.lastUsedAtWriteInterval
}

// int4Ptr 把可空用户限流上限转成 *int64（nil=继承全局默认限流）。
func int4Ptr(v pgtype.Int4) *int64 {
	if !v.Valid {
		return nil
	}
	out := int64(v.Int32)
	return &out
}

// TODO(阶段3/production): [GAP-3-002] 补齐 API Key revoke、disable、list 和审计日志能力，确保后台能安全管理 customer API key。
