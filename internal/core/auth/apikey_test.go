package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/core/apikey"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// fakeAPIKeyStore 是认证测试使用的存储替身，用来避免连接真实数据库。
type fakeAPIKeyStore struct {
	key          sqlc.GetAPIKeyByHashRow
	err          error
	expectedHash string
	updatedArg   sqlc.UpdateAPIKeyLastUsedAtParams
	updated      bool
	updateErr    error
}

// GetAPIKeyByHash 返回测试预设的 API Key 记录或错误。
func (s *fakeAPIKeyStore) GetAPIKeyByHash(ctx context.Context, keyHash string) (sqlc.GetAPIKeyByHashRow, error) {
	if s.expectedHash != "" && keyHash != s.expectedHash {
		return sqlc.GetAPIKeyByHashRow{}, pgx.ErrNoRows
	}
	return s.key, s.err
}

// UpdateAPIKeyLastUsedAt 记录认证服务传入的 last_used_at 更新参数。
func (s *fakeAPIKeyStore) UpdateAPIKeyLastUsedAt(ctx context.Context, arg sqlc.UpdateAPIKeyLastUsedAtParams) error {
	s.updated = true
	s.updatedArg = arg
	return s.updateErr
}

// validAPIKey 返回一条默认有效的测试 API Key 记录。
func validAPIKey() sqlc.GetAPIKeyByHashRow {
	return sqlc.GetAPIKeyByHashRow{
		ID:        1,
		UserID:    10,
		KeyPrefix: "unio_sk_test",
	}
}

func TestAuthenticateAPIKeyMissing(t *testing.T) {
	authenticator := NewAPIKeyAuthenticator(&fakeAPIKeyStore{})
	_, err := authenticator.AuthenticateAPIKey(context.Background(), "")
	if !errors.Is(err, ErrMissingAPIKey) {
		t.Fatalf("expected ErrMissingAPIKey, got %v", err)
	}
}

func TestAuthenticateAPIKeyInvalid(t *testing.T) {
	authenticator := NewAPIKeyAuthenticator(&fakeAPIKeyStore{
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

	authenticator := NewAPIKeyAuthenticator(&fakeAPIKeyStore{
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

	authenticator := NewAPIKeyAuthenticator(&fakeAPIKeyStore{key: key})

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

	authenticator := NewAPIKeyAuthenticator(&fakeAPIKeyStore{
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

func TestAuthenticateAPIKeySpendLimitReached(t *testing.T) {
	key := validAPIKey()
	key.SpendLimitReached = pgtype.Bool{Bool: true, Valid: true}

	store := &fakeAPIKeyStore{key: key}
	authenticator := NewAPIKeyAuthenticator(store)

	_, err := authenticator.AuthenticateAPIKey(context.Background(), "test")
	if !errors.Is(err, ErrAPIKeySpendLimitReached) {
		t.Fatalf("expected ErrAPIKeySpendLimitReached, got %v", err)
	}
	// 超额是终态拒绝：不应再更新 last_used_at。
	if store.updated {
		t.Fatal("expected last_used_at not to be updated when spend limit reached")
	}
}

func TestAuthenticateAPIKeyValid(t *testing.T) {
	key := validAPIKey()
	key.RouteRpmLimit = pgtype.Int4{Int32: 10, Valid: true}
	key.RouteTpmLimit = pgtype.Int4{Int32: 1000, Valid: true}
	key.RouteRpdLimit = pgtype.Int4{Int32: 100, Valid: true}
	key.RouteConcurrencyLimit = pgtype.Int4{Int32: 4, Valid: true}
	authenticator := NewAPIKeyAuthenticator(&fakeAPIKeyStore{
		key: key,
	})

	principal, err := authenticator.AuthenticateAPIKey(context.Background(), "valid-key")
	if err != nil {
		t.Fatalf("authenticate api key: %v", err)
	}
	if principal.APIKeyID != key.ID {
		t.Fatalf("expected api key id %d, got %d", key.ID, principal.APIKeyID)
	}
	if principal.UserID != key.UserID {
		t.Fatalf("expected user id %d, got %d", key.UserID, principal.UserID)
	}
	if principal.KeyPrefix != key.KeyPrefix {
		t.Fatalf("expected key prefix %q, got %q", key.KeyPrefix, principal.KeyPrefix)
	}
	if principal.RPMLimit == nil || *principal.RPMLimit != 10 ||
		principal.TPMLimit == nil || *principal.TPMLimit != 1000 ||
		principal.RPDLimit == nil || *principal.RPDLimit != 100 ||
		principal.ConcurrencyLimit == nil || *principal.ConcurrencyLimit != 4 {
		t.Fatalf("unexpected route limit snapshot: %+v", principal)
	}
}

func TestAuthenticateAPIKeyAcceptsIssuedPrefixes(t *testing.T) {
	for _, tt := range []struct {
		name      string
		plaintext string
	}{
		{name: "unio", plaintext: "unio_sk_existing"},
		{name: "anthropic-compatible", plaintext: "sk-ant-api03-unio_existing"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeAPIKeyStore{
				key:          validAPIKey(),
				expectedHash: apikey.Hash(tt.plaintext),
			}
			authenticator := NewAPIKeyAuthenticator(store)

			if _, err := authenticator.AuthenticateAPIKey(context.Background(), tt.plaintext); err != nil {
				t.Fatalf("authenticate issued api key: %v", err)
			}
		})
	}
}

func TestAuthenticateAPIKeyValidUpdatesLastUsedAt(t *testing.T) {
	now := time.Date(2026, 5, 7, 10, 30, 0, 0, time.UTC)
	key := validAPIKey()
	store := &fakeAPIKeyStore{key: key}
	authenticator := NewAPIKeyAuthenticator(store)
	authenticator.now = func() time.Time {
		return now
	}

	_, err := authenticator.AuthenticateAPIKey(context.Background(), "valid-key")
	if err != nil {
		t.Fatalf("authenticate api key: %v", err)
	}

	if !store.updated {
		t.Fatal("expected last_used_at to be updated")
	}

	if store.updatedArg.ID != key.ID {
		t.Fatalf("expected updated api key id %d, got %d", key.ID, store.updatedArg.ID)
	}

	if !store.updatedArg.LastUsedAt.Valid {
		t.Fatal("expected last_used_at to be valid")
	}

	if !store.updatedArg.LastUsedAt.Time.Equal(now) {
		t.Fatalf("expected last_used_at %v, got %v", now, store.updatedArg.LastUsedAt.Time)
	}
}

func TestAuthenticateAPIKeyInvalidDoesNotUpdateLastUsedAt(t *testing.T) {
	store := &fakeAPIKeyStore{
		err: pgx.ErrNoRows,
	}
	authenticator := NewAPIKeyAuthenticator(store)

	_, err := authenticator.AuthenticateAPIKey(context.Background(), "wrong")
	if !errors.Is(err, ErrInvalidAPIKey) {
		t.Fatalf("expected ErrInvalidAPIKey, got %v", err)
	}

	if store.updated {
		t.Fatal("expected last_used_at not to be updated")
	}
}
