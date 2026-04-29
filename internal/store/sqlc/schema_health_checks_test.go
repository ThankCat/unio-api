package sqlc

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSchemaHealthChecksQueries(t *testing.T) {
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

	queries := New(pool)

	name := "sqlc-test"

	created, err := queries.CreateSchemaHealthCheck(ctx, name)
	if err != nil {
		t.Fatalf("create schema health check: %v", err)
	}

	got, err := queries.GetSchemaHealthCheckByName(ctx, name)
	if err != nil {
		t.Fatalf("get schema health check by name: %v", err)
	}

	if got.ID != created.ID {
		t.Fatalf("expected id %d, got %d", created.ID, got.ID)
	}

	if got.Name != name {
		t.Fatalf("expected name %q, got %q", name, got.Name)
	}
}
