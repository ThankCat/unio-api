package auth

import (
	"context"
	"errors"
	"time"

	"github.com/ThankCat/unio-api/internal/apikey"
	"github.com/ThankCat/unio-api/internal/store/sqlc"
	"github.com/jackc/pgx/v5"
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
)

// APIKeyPrincipal 表示 API Key 认证成功后的请求身份。
type APIKeyPrincipal struct {
	APIKeyID  int64
	ProjectID int64
	KeyPrefix string
}

// APIKeyStore 定义 API Key 认证所需的存储查询能力。
type APIKeyStore interface {
	GetAPIKeyByHash(ctx context.Context, keyHash string) (sqlc.ApiKey, error)
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
		return nil, ErrMissingAPIKey
	}

	keyHash := apikey.Hash(plaintext)

	key, err := a.store.GetAPIKeyByHash(ctx, keyHash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrInvalidAPIKey
		}
		return nil, err
	}

	if key.RevokedAt.Valid {
		return nil, ErrAPIKeyRevoked
	}

	if key.DisabledAt.Valid {
		return nil, ErrAPIKeyDisabled
	}

	if key.ExpiresAt.Valid && !key.ExpiresAt.Time.After(a.now()) {
		return nil, ErrAPIKeyExpired
	}

	return &APIKeyPrincipal{
		APIKeyID:  key.ID,
		ProjectID: key.ProjectID,
		KeyPrefix: key.KeyPrefix,
	}, nil
}
