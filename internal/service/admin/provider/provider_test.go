package provider_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-api/internal/service/admin/provider"
)

type fakeProviderStore struct {
	providers   []sqlc.Provider
	getRow      sqlc.Provider
	getErr      error
	createRow   sqlc.Provider
	createErr   error
	createParam sqlc.CreateProviderParams
	createCalls int
	updateRow   sqlc.Provider
	updateErr   error
}

func (s *fakeProviderStore) ListProviders(context.Context) ([]sqlc.Provider, error) {
	return s.providers, nil
}

func (s *fakeProviderStore) GetProvider(_ context.Context, _ int64) (sqlc.Provider, error) {
	return s.getRow, s.getErr
}

func (s *fakeProviderStore) CreateProvider(_ context.Context, arg sqlc.CreateProviderParams) (sqlc.Provider, error) {
	s.createParam = arg
	s.createCalls++
	return s.createRow, s.createErr
}

func (s *fakeProviderStore) UpdateProvider(_ context.Context, _ sqlc.UpdateProviderParams) (sqlc.Provider, error) {
	return s.updateRow, s.updateErr
}

func TestCreateRejectsInvalidArguments(t *testing.T) {
	cases := []struct {
		name string
		in   provider.CreateInput
	}{
		{"bad slug", provider.CreateInput{Slug: "Bad Slug", Name: "x", Status: provider.StatusEnabled}},
		{"empty name", provider.CreateInput{Slug: "openai", Name: "  ", Status: provider.StatusEnabled}},
		{"bad status", provider.CreateInput{Slug: "openai", Name: "x", Status: "paused"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeProviderStore{}
			_, err := provider.NewService(store).Create(context.Background(), tc.in)
			if got := failure.CodeOf(err); got != failure.CodeAdminInvalidArgument {
				t.Fatalf("expected %q, got %q", failure.CodeAdminInvalidArgument, got)
			}
			if store.createCalls != 0 {
				t.Fatalf("store should not be called on invalid argument")
			}
		})
	}
}

func TestCreateConflictOnUniqueViolation(t *testing.T) {
	store := &fakeProviderStore{createErr: &pgconn.PgError{Code: "23505"}}
	_, err := provider.NewService(store).Create(context.Background(), provider.CreateInput{
		Slug: "openai", Name: "OpenAI", Status: provider.StatusEnabled,
	})
	if got := failure.CodeOf(err); got != failure.CodeAdminConflict {
		t.Fatalf("expected %q, got %q", failure.CodeAdminConflict, got)
	}
}

func TestCreateSuccessTrimsAndMaps(t *testing.T) {
	now := time.Now()
	store := &fakeProviderStore{createRow: sqlc.Provider{
		ID: 7, Slug: "openai", Name: "OpenAI", Status: provider.StatusEnabled,
		CreatedAt: pgtype.Timestamptz{Time: now, Valid: true},
		UpdatedAt: pgtype.Timestamptz{Time: now, Valid: true},
	}}

	got, err := provider.NewService(store).Create(context.Background(), provider.CreateInput{
		Slug: "  openai  ", Name: "  OpenAI  ", Status: provider.StatusEnabled,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if store.createParam.Slug != "openai" || store.createParam.Name != "OpenAI" {
		t.Fatalf("expected trimmed params, got %+v", store.createParam)
	}
	if got.ID != 7 || got.Slug != "openai" || got.Status != provider.StatusEnabled {
		t.Fatalf("unexpected mapped provider: %+v", got)
	}
}

func TestGetNotFound(t *testing.T) {
	store := &fakeProviderStore{getErr: pgx.ErrNoRows}
	_, err := provider.NewService(store).Get(context.Background(), 5)
	if got := failure.CodeOf(err); got != failure.CodeAdminNotFound {
		t.Fatalf("expected %q, got %q", failure.CodeAdminNotFound, got)
	}
}

func TestUpdateNotFound(t *testing.T) {
	store := &fakeProviderStore{updateErr: pgx.ErrNoRows}
	_, err := provider.NewService(store).Update(context.Background(), provider.UpdateInput{
		ID: 5, Name: "OpenAI", Status: provider.StatusDisabled,
	})
	if got := failure.CodeOf(err); got != failure.CodeAdminNotFound {
		t.Fatalf("expected %q, got %q", failure.CodeAdminNotFound, got)
	}
}
