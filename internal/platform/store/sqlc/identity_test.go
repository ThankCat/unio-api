package sqlc_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/core/apikey"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
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

	key, err := apikey.Generate()
	if err != nil {
		t.Fatalf("generate api key: %v", err)
	}

	// 线路必填：先建一条线路供 API Key 绑定（route_id 现为 NOT NULL）。
	var priceRatio pgtype.Numeric
	if err := priceRatio.Scan("1"); err != nil {
		t.Fatalf("scan price ratio: %v", err)
	}
	route, err := queries.CreateRoute(ctx, sqlc.CreateRouteParams{
		Name:       fmt.Sprintf("identity-route-%d", suffix),
		Mode:       "balanced",
		Status:     "enabled",
		PriceRatio: priceRatio,
	})
	if err != nil {
		t.Fatalf("create route: %v", err)
	}

	storedKey, err := queries.CreateAPIKey(ctx, sqlc.CreateAPIKeyParams{
		UserID:    user.ID,
		Name:      "test key",
		KeyPrefix: key.Prefix,
		KeyHash:   key.Hash,
		ExpiresAt: pgtype.Timestamptz{Valid: false},
		RouteID:   route.ID,
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

	if gotKey.UserID != user.ID {
		t.Fatalf("expected user id %d, got %d", user.ID, gotKey.UserID)
	}

	if !apikey.Verify(key.Plaintext, gotKey.KeyHash) {
		t.Fatal("expected plaintext key to verify against stored hash")
	}

}
