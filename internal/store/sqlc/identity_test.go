package sqlc_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ThankCat/unio-api/internal/apikey"
	"github.com/ThankCat/unio-api/internal/store/sqlc"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIdentityQueries(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("create postgres pool: %v", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping postgres: %v", err)
	}

	queries := sqlc.New(pool)

	suffix := time.Now().UnixNano()
	email := fmt.Sprintf("user-%d@example.com", suffix)
	displayName := fmt.Sprintf("Test User-%d", suffix)

	user, err := queries.CreateUser(ctx, sqlc.CreateUserParams{
		Email:        email,
		PasswordHash: "test-password-hash",
		DisplayName:  displayName,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	gotUser, err := queries.GetUserByEmail(ctx, strings.ToUpper(email))
	if err != nil {
		t.Fatalf("get user by email: %v", err)
	}
	if gotUser.ID != user.ID {
		t.Fatalf("expected user id %d, got %d", user.ID, gotUser.ID)
	}

	project, err := queries.CreateProject(ctx, sqlc.CreateProjectParams{
		UserID: user.ID,
		Name:   "default",
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	key, err := apikey.Generate()
	if err != nil {
		t.Fatalf("generate api key: %v", err)
	}

	storedKey, err := queries.CreateAPIKey(ctx, sqlc.CreateAPIKeyParams{
		ProjectID: project.ID,
		Name:      "test key",
		KeyPrefix: key.Prefix,
		KeyHash:   key.Hash,
		ExpiresAt: pgtype.Timestamptz{Valid: false},
	})
	if err != nil {
		t.Fatalf("create api key: %v", err)
	}

	gotKey, err := queries.GetAPIKeyByHash(ctx, apikey.Hash(key.Plaintext))
	if err != nil {
		t.Fatalf("get api key by hash: %v", err)
	}

	if gotKey.ID != storedKey.ID {
		t.Fatalf("expected api key id %d, got %d", storedKey.ID, gotKey.ID)
	}

	if gotKey.ProjectID != project.ID {
		t.Fatalf("expected project id %d, got %d", project.ID, gotKey.ProjectID)
	}

	if !apikey.Verify(key.Plaintext, gotKey.KeyHash) {
		t.Fatal("expected plaintext key to verify against stored hash")
	}

}
