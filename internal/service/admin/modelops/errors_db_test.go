// 模型错误聚合的 DB 级测试（需 DATABASE_URL，缺省跳过）。
//
// 聚合与请求明细互补：明细回答「这一笔怎么了」，聚合回答「主要错在哪」。
// 因此这里守的是排序（最高频在前）、归一（空错误码不能散成多行）和取样（样例要是最近一条）。
package modelops_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/admin/modelops"
)

type errFixture struct {
	t        *testing.T
	ctx      context.Context
	pool     *pgxpool.Pool
	service  *modelops.Service
	modelID  int64
	publicID string
	userID   int64
	apiKeyID int64
}

func newErrFixture(t *testing.T) *errFixture {
	t.Helper()
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		cancel()
		t.Fatalf("create postgres pool: %v", err)
	}
	f := &errFixture{
		t:       t,
		ctx:     ctx,
		pool:    pool,
		service: modelops.NewService(sqlc.New(pool)),
	}
	suffix := time.Now().UnixNano()
	f.publicID = fmt.Sprintf("openai/ops-errors-%d", suffix)

	if err := pool.QueryRow(ctx, `
		INSERT INTO models (model_id, display_name, owned_by, status)
		VALUES ($1, $1, 'test', 'enabled') RETURNING id
	`, f.publicID).Scan(&f.modelID); err != nil {
		t.Fatalf("insert model: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (uid, email, password_hash, display_name)
		VALUES (gen_random_uuid(), $1, 'x', 'ops errors test') RETURNING id
	`, fmt.Sprintf("ops-errors-%d@test.local", suffix)).Scan(&f.userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO api_keys (user_id, name, key_prefix, key_hash)
		VALUES ($1, 'ops errors key', 'sk-test', $2) RETURNING id
	`, f.userID, fmt.Sprintf("ops-errors-hash-%d", suffix)).Scan(&f.apiKeyID); err != nil {
		t.Fatalf("insert api key: %v", err)
	}

	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM request_records WHERE user_id = $1`, f.userID)
		_, _ = pool.Exec(ctx, `DELETE FROM api_keys WHERE user_id = $1`, f.userID)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, f.userID)
		_, _ = pool.Exec(ctx, `DELETE FROM models WHERE id = $1`, f.modelID)
		pool.Close()
		cancel()
	})
	return f
}

// request 写入一条请求记录；errorCode 传空串表示该列为 NULL。
func (f *errFixture) request(status, errorCode string, ageMinutes int) string {
	f.t.Helper()
	requestID := fmt.Sprintf("req-%d-%d", time.Now().UnixNano(), ageMinutes)
	var code any
	if errorCode != "" {
		code = errorCode
	}
	_, err := f.pool.Exec(f.ctx, `
		INSERT INTO request_records (
			request_id, user_id, api_key_id, requested_model_id,
			ingress_protocol, endpoint, stream, status, error_code,
			started_at, created_at
		)
		VALUES ($1, $2, $3, $4, 'openai', 'chat_completions', false, $5, $6,
			now() - make_interval(mins => $7::int), now() - make_interval(mins => $7::int))
	`, requestID, f.userID, f.apiKeyID, f.publicID, status, code, ageMinutes)
	if err != nil {
		f.t.Fatalf("insert request record: %v", err)
	}
	return requestID
}

func TestModelOpsErrorsAggregatesByCode(t *testing.T) {
	f := newErrFixture(t)
	f.request("failed", "upstream_timeout", 30)
	f.request("failed", "upstream_timeout", 20)
	newestTimeout := f.request("failed", "upstream_timeout", 5)
	f.request("failed", "rate_limited", 25)
	// 成功请求不该进错误聚合。
	f.request("succeeded", "", 10)

	from := time.Now().Add(-2 * time.Hour)
	to := time.Now().Add(time.Minute)
	rows, err := f.service.Errors(f.ctx, f.modelID, from, to)
	if err != nil {
		t.Fatalf("model ops errors: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 error codes, got %d: %+v", len(rows), rows)
	}

	// 最高频排在最前，这是这张表存在的意义。
	if rows[0].ErrorCode != "upstream_timeout" || rows[0].Occurrences != 3 {
		t.Fatalf("top row = %+v, want upstream_timeout x3", rows[0])
	}
	if rows[0].SampleRequestID != newestTimeout {
		t.Fatalf("sample request = %q, want the most recent one %q", rows[0].SampleRequestID, newestTimeout)
	}
	if rows[1].ErrorCode != "rate_limited" || rows[1].Occurrences != 1 {
		t.Fatalf("second row = %+v, want rate_limited x1", rows[1])
	}
}

// 错误码为空的失败请求要归到一行，否则每条都是自己一组，聚合表退化成明细表。
func TestModelOpsErrorsGroupsMissingCode(t *testing.T) {
	f := newErrFixture(t)
	f.request("failed", "", 30)
	f.request("failed", "", 10)

	rows, err := f.service.Errors(f.ctx, f.modelID, time.Now().Add(-2*time.Hour), time.Now().Add(time.Minute))
	if err != nil {
		t.Fatalf("model ops errors: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected a single grouped row, got %d: %+v", len(rows), rows)
	}
	if rows[0].ErrorCode != "unknown" || rows[0].Occurrences != 2 {
		t.Fatalf("row = %+v, want unknown x2", rows[0])
	}
}

// 时间窗之外的失败不计入，否则页头区间与错误表对不上。
func TestModelOpsErrorsRespectsRange(t *testing.T) {
	f := newErrFixture(t)
	f.request("failed", "upstream_timeout", 5)
	f.request("failed", "upstream_timeout", 600)

	rows, err := f.service.Errors(f.ctx, f.modelID, time.Now().Add(-time.Hour), time.Now().Add(time.Minute))
	if err != nil {
		t.Fatalf("model ops errors: %v", err)
	}
	if len(rows) != 1 || rows[0].Occurrences != 1 {
		t.Fatalf("rows = %+v, want a single occurrence inside the window", rows)
	}
}
