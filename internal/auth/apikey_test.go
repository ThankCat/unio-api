package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ThankCat/unio-api/internal/store/sqlc"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// fakeAPIKeyStore 是认证测试使用的存储替身，用来避免连接真实数据库。
type fakeAPIKeyStore struct {
	key sqlc.ApiKey
	err error
}

// GetAPIKeyByHash 返回测试预设的 API Key 记录或错误。
func (s fakeAPIKeyStore) GetAPIKeyByHash(ctx context.Context, keyHash string) (sqlc.ApiKey, error) {
	return s.key, s.err
}

// validAPIKey 返回一条默认有效的测试 API Key 记录。
func validAPIKey() sqlc.ApiKey {
	return sqlc.ApiKey{
		ID:        1,
		ProjectID: 100,
		KeyPrefix: "unio_sk_test",
	}
}

func TestAuthenticateAPIKeyMissing(t *testing.T) {
	authenticator := NewAPIKeyAuthenticator(fakeAPIKeyStore{})
	_, err := authenticator.AuthenticateAPIKey(context.Background(), "")
	if !errors.Is(err, ErrMissingAPIKey) {
		t.Fatalf("expected ErrMissingAPIKey, got %v", err)
	}
}

func TestAuthenticateAPIKeyInvalid(t *testing.T) {
	authenticator := NewAPIKeyAuthenticator(fakeAPIKeyStore{
		err: pgx.ErrNoRows,
	})

	_, err := authenticator.AuthenticateAPIKey(context.Background(), "wrong")
	if !errors.Is(err, ErrInvalidAPIKey) {
		t.Fatalf("expected ErrInvalidAPIKey, got %v", err)
	}
}

func TestAuthenticateAPIKeyRevoked(t *testing.T) {
	key := validAPIKey()
	key.RevokedAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}

	authenticator := NewAPIKeyAuthenticator(fakeAPIKeyStore{
		key: key,
	})

	_, err := authenticator.AuthenticateAPIKey(context.Background(), "test")
	if !errors.Is(err, ErrAPIKeyRevoked) {
		t.Fatalf("expected ErrAPIKeyRevoked, got %v", err)
	}
}

func TestAuthenticateAPIKeyDisabled(t *testing.T) {
	key := validAPIKey()
	key.DisabledAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}

	authenticator := NewAPIKeyAuthenticator(fakeAPIKeyStore{key: key})

	_, err := authenticator.AuthenticateAPIKey(context.Background(), "test")
	if !errors.Is(err, ErrAPIKeyDisabled) {
		t.Fatalf("expected ErrAPIKeyDisabled, got %v", err)
	}
}

func TestAuthenticateAPIKeyExpired(t *testing.T) {
	now := time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC)

	key := validAPIKey()
	key.ExpiresAt = pgtype.Timestamptz{
		Time:  now.Add(-time.Second),
		Valid: true,
	}

	authenticator := NewAPIKeyAuthenticator(fakeAPIKeyStore{
		key: key,
	})
	authenticator.now = func() time.Time {
		return now
	}

	_, err := authenticator.AuthenticateAPIKey(context.Background(), "test")
	if !errors.Is(err, ErrAPIKeyExpired) {
		t.Fatalf("expected ErrAPIKeyExpired, got %v", err)
	}
}

func TestAuthenticateAPIKeyValid(t *testing.T) {
	key := validAPIKey()
	authenticator := NewAPIKeyAuthenticator(fakeAPIKeyStore{
		key: key,
	})

	principal, err := authenticator.AuthenticateAPIKey(context.Background(), "valid-key")
	if err != nil {
		t.Fatalf("authenticate api key: %v", err)
	}
	if principal.APIKeyID != key.ID {
		t.Fatalf("expected api key id %d, got %d", key.ID, principal.APIKeyID)
	}
	if principal.KeyPrefix != key.KeyPrefix {
		t.Fatalf("expected key prefix %q, got %q", key.KeyPrefix, principal.KeyPrefix)
	}
	if principal.ProjectID != key.ProjectID {
		t.Fatalf("expected project id %d, got %d", key.ProjectID, principal.ProjectID)
	}
}
