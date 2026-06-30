package apikey

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
)

type fakeStore struct {
	arg    sqlc.CreateAPIKeyParams
	called bool
	err    error
}

func (s *fakeStore) CreateAPIKey(ctx context.Context, arg sqlc.CreateAPIKeyParams) (sqlc.ApiKey, error) {
	s.arg = arg
	s.called = true
	if s.err != nil {
		return sqlc.ApiKey{}, s.err
	}

	return sqlc.ApiKey{
		ID:        1,
		UserID:    arg.UserID,
		Name:      arg.Name,
		KeyPrefix: arg.KeyPrefix,
		ExpiresAt: arg.ExpiresAt,
	}, nil
}

func TestServiceCreateSuccess(t *testing.T) {
	store := &fakeStore{}
	service := NewService(store)

	expiresAt := time.Now().UTC()

	created, err := service.Create(context.Background(), CreateParams{
		UserID:    10,
		Name:      "test",
		ExpiresAt: &expiresAt,
		RouteID:   1,
	})
	if err != nil {
		t.Fatal(err)
	}

	if store.arg.RouteID != 1 {
		t.Fatalf("want stored route_id 1, got %d", store.arg.RouteID)
	}

	if !store.called {
		t.Fatal("store was not called")
	}

	if created.ID != 1 {
		t.Fatalf("want id 1, got %d", created.ID)
	}

	if created.UserID != 10 {
		t.Fatalf("want user_id 10, got %d", created.UserID)
	}

	if created.Name != "test" {
		t.Fatalf("want name test, got %q", created.Name)
	}

	if created.Plaintext == "" {
		t.Fatal("want plaintext to be returned")
	}

	if created.Prefix == "" {
		t.Fatal("want prefix to be returned")
	}

	if store.arg.UserID != 10 {
		t.Fatalf("want stored user_id 10, got %d", store.arg.UserID)
	}

	if store.arg.Name != "test" {
		t.Fatalf("want stored name test, got %q", store.arg.Name)
	}

	if store.arg.KeyHash == "" {
		t.Fatal("want key hash to be stored")
	}

	if store.arg.KeyHash == created.Plaintext {
		t.Fatal("stored key hash must not equal plaintext")
	}

	if store.arg.KeyPrefix != created.Prefix {
		t.Fatalf("want stored prefix %q, got %q", created.Prefix, store.arg.KeyPrefix)
	}

	if !store.arg.ExpiresAt.Valid {
		t.Fatal("want expires_at to be valid")
	}

	if created.ExpiresAt == nil {
		t.Fatal("want returned expires_at")
	}
}

func TestServiceCreateInvalidUserID(t *testing.T) {
	store := &fakeStore{}
	service := NewService(store)

	created, err := service.Create(context.Background(), CreateParams{
		UserID: -1,
		Name:   "test",
	})

	if created != nil {
		t.Fatal("want created key to be nil")
	}

	if !errors.Is(err, ErrInvalidUserID) {
		t.Fatalf("want ErrInvalidUserID, got %v", err)
	}

	if store.called {
		t.Fatal("want store not to be called")
	}
}

func TestServiceCreateInvalidName(t *testing.T) {
	store := &fakeStore{}
	service := NewService(store)

	created, err := service.Create(context.Background(), CreateParams{
		UserID: 10,
		Name:   "   ",
	})

	if created != nil {
		t.Fatal("want created key to be nil")
	}

	if !errors.Is(err, ErrInvalidName) {
		t.Fatalf("want ErrInvalidName, got %v", err)
	}

	if store.called {
		t.Fatal("want store not to be called")
	}
}

func TestServiceCreateInvalidRoute(t *testing.T) {
	store := &fakeStore{}
	service := NewService(store)

	// 线路必填：route_id 缺省（0）应被拒，且不落库。
	created, err := service.Create(context.Background(), CreateParams{
		UserID: 10,
		Name:   "test",
	})

	if created != nil {
		t.Fatal("want created key to be nil")
	}

	if !errors.Is(err, ErrInvalidRoute) {
		t.Fatalf("want ErrInvalidRoute, got %v", err)
	}

	if store.called {
		t.Fatal("want store not to be called")
	}
}

func TestServiceCreateStoreError(t *testing.T) {
	storeErr := errors.New("insert api key failed")
	store := &fakeStore{err: storeErr}
	service := NewService(store)

	created, err := service.Create(context.Background(), CreateParams{
		UserID:  10,
		Name:    "test",
		RouteID: 1,
	})

	if created != nil {
		t.Fatal("want created key to be nil")
	}

	if !errors.Is(err, storeErr) {
		t.Fatalf("want store error, got %v", err)
	}

	if !store.called {
		t.Fatal("want store to be called")
	}
}

func TestServiceCreateWithoutExpiresAt(t *testing.T) {
	store := &fakeStore{}
	service := NewService(store)

	created, err := service.Create(context.Background(), CreateParams{
		UserID:    10,
		Name:      "test",
		ExpiresAt: nil,
		RouteID:   1,
	})
	if err != nil {
		t.Fatalf("create api key: %v", err)
	}

	if store.arg.ExpiresAt.Valid {
		t.Fatal("want stored expires_at to be null")
	}

	if created.ExpiresAt != nil {
		t.Fatal("want returned expires_at to be nil")
	}
}
