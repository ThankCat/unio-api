package sqlc_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
)

// TestDashboardQueriesAgainstSchema 仅校验 M9 看板聚合 SQL 在真实 schema 上可执行（well-formed）。
// 不依赖具体数据：空区间/空库下 :many 返回 0 行、:one 返回零值，均不应报错。
func TestDashboardQueriesAgainstSchema(t *testing.T) {
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

	if _, err := q.DashboardRevenueByCurrency(ctx, sqlc.DashboardRevenueByCurrencyParams{}); err != nil {
		t.Fatalf("DashboardRevenueByCurrency: %v", err)
	}
	if _, err := q.DashboardCostByCurrency(ctx, sqlc.DashboardCostByCurrencyParams{}); err != nil {
		t.Fatalf("DashboardCostByCurrency: %v", err)
	}
	if _, err := q.DashboardBillingExceptionSummary(ctx, sqlc.DashboardBillingExceptionSummaryParams{}); err != nil {
		t.Fatalf("DashboardBillingExceptionSummary: %v", err)
	}

	for _, unit := range []string{"hour", "day"} {
		if _, err := q.DashboardRequestsTimeseries(ctx, sqlc.DashboardRequestsTimeseriesParams{Unit: unit}); err != nil {
			t.Fatalf("DashboardRequestsTimeseries(%s): %v", unit, err)
		}
		if _, err := q.DashboardTokensTimeseries(ctx, sqlc.DashboardTokensTimeseriesParams{Unit: unit}); err != nil {
			t.Fatalf("DashboardTokensTimeseries(%s): %v", unit, err)
		}
		if _, err := q.DashboardSpendTimeseries(ctx, sqlc.DashboardSpendTimeseriesParams{Unit: unit}); err != nil {
			t.Fatalf("DashboardSpendTimeseries(%s): %v", unit, err)
		}
	}
}
