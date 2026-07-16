package sqlc_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

// TestChannelsOpsQueriesAgainstSchema 校验渠道作战台抽屉 SQL 在真实 schema 上 well-formed。
func TestChannelsOpsQueriesAgainstSchema(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("create postgres pool: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping postgres: %v", err)
	}

	q := sqlc.New(pool)
	now := time.Now().UTC()
	from := pgtype.Timestamptz{Time: now.Add(-24 * time.Hour), Valid: true}
	to := pgtype.Timestamptz{Time: now, Valid: true}

	if _, err := q.ChannelOpsModels(ctx, sqlc.ChannelOpsModelsParams{
		FromTime:  from,
		ToTime:    to,
		ChannelID: 1,
	}); err != nil {
		t.Fatalf("ChannelOpsModels: %v", err)
	}
	if _, err := q.ChannelOpsRoutes(ctx, 1); err != nil {
		t.Fatalf("ChannelOpsRoutes: %v", err)
	}
}
