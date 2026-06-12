package sqlc_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
)

// TestSystemQueriesAgainstSchema 仅校验 M8 系统/任务/健康 SQL 在真实 schema 上 well-formed。
// 不依赖具体数据：空区间/空库下 :many 返回 0 行、:one 返回零值，均不应报错。
func TestSystemQueriesAgainstSchema(t *testing.T) {
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

	// settlement recovery jobs 列表/计数：覆盖「无过滤」与「全过滤」两条路径。
	if _, err := q.ListSettlementRecoveryJobsPage(ctx, sqlc.ListSettlementRecoveryJobsPageParams{PageLimit: 20}); err != nil {
		t.Fatalf("ListSettlementRecoveryJobsPage(no filter): %v", err)
	}
	if _, err := q.CountSettlementRecoveryJobs(ctx, sqlc.CountSettlementRecoveryJobsParams{}); err != nil {
		t.Fatalf("CountSettlementRecoveryJobs(no filter): %v", err)
	}

	now := time.Now().UTC()
	from := pgtype.Timestamptz{Time: now.Add(-24 * time.Hour), Valid: true}
	to := pgtype.Timestamptz{Time: now, Valid: true}
	filtered := sqlc.ListSettlementRecoveryJobsPageParams{
		Status:     pgtype.Text{String: "dead", Valid: true},
		UserID:     pgtype.Int8{Int64: 1, Valid: true},
		FromTime:   from,
		ToTime:     to,
		PageLimit:  20,
		PageOffset: 0,
	}
	if _, err := q.ListSettlementRecoveryJobsPage(ctx, filtered); err != nil {
		t.Fatalf("ListSettlementRecoveryJobsPage(filtered): %v", err)
	}
	if _, err := q.CountSettlementRecoveryJobs(ctx, sqlc.CountSettlementRecoveryJobsParams{
		Status:   filtered.Status,
		UserID:   filtered.UserID,
		FromTime: filtered.FromTime,
		ToTime:   filtered.ToTime,
	}); err != nil {
		t.Fatalf("CountSettlementRecoveryJobs(filtered): %v", err)
	}

	// 系统级 channel 健康：无区间与带区间两条路径。
	if _, err := q.SystemChannelHealth(ctx, sqlc.SystemChannelHealthParams{}); err != nil {
		t.Fatalf("SystemChannelHealth(no range): %v", err)
	}
	if _, err := q.SystemChannelHealth(ctx, sqlc.SystemChannelHealthParams{FromTime: from, ToTime: to}); err != nil {
		t.Fatalf("SystemChannelHealth(range): %v", err)
	}
}
