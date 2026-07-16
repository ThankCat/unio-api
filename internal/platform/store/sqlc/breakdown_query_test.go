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

func TestBreakdownChannelAndRouteQueries(t *testing.T) {
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

	q := sqlc.New(pool)
	from := pgtype.Timestamptz{Time: time.Now().Add(-24 * time.Hour), Valid: true}
	to := pgtype.Timestamptz{Time: time.Now(), Valid: true}

	if _, err := q.DashboardBreakdownChannel(ctx, sqlc.DashboardBreakdownChannelParams{FromTime: from, ToTime: to}); err != nil {
		t.Fatalf("DashboardBreakdownChannel: %v", err)
	}
	if _, err := q.DashboardBreakdownRoute(ctx, sqlc.DashboardBreakdownRouteParams{FromTime: from, ToTime: to}); err != nil {
		t.Fatalf("DashboardBreakdownRoute: %v", err)
	}
}
