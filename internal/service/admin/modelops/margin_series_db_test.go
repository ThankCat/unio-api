// 概览与时序的金额一致性测试（需 DATABASE_URL，缺省跳过）。
//
// 页头概览和概览分区的趋势图放在同一屏，读的是同一笔钱。两条 SQL 路径不同：
// 概览是整段窗口的两个子查询，时序是分桶后再聚合。口径一旦漂移，
// 页面就会自相矛盾——图表求和对不上上面的大数字，而且没人知道该信哪个。
// 这个测试把「两者必须相等」钉死。
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

type marginFixture struct {
	t        *testing.T
	ctx      context.Context
	pool     *pgxpool.Pool
	service  *modelops.Service
	modelID  int64
	publicID string
	userID   int64
	apiKeyID int64
	provider int64
	channel  int64
	seq      int
}

func newMarginFixture(t *testing.T) *marginFixture {
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
	f := &marginFixture{
		t:       t,
		ctx:     ctx,
		pool:    pool,
		service: modelops.NewService(sqlc.New(pool)),
	}
	suffix := time.Now().UnixNano()
	f.publicID = fmt.Sprintf("openai/margin-series-%d", suffix)

	mustScan(t, pool, ctx, &f.modelID, `
		INSERT INTO models (model_id, display_name, owned_by, status)
		VALUES ($1, $1, 'test', 'enabled') RETURNING id`, f.publicID)
	mustScan(t, pool, ctx, &f.userID, `
		INSERT INTO users (uid, email, password_hash, display_name)
		VALUES (gen_random_uuid(), $1, 'x', 'margin series test') RETURNING id`,
		fmt.Sprintf("margin-series-%d@test.local", suffix))
	mustScan(t, pool, ctx, &f.apiKeyID, `
		INSERT INTO api_keys (user_id, name, key_prefix, key_hash)
		VALUES ($1, 'margin series key', 'sk-test', $2) RETURNING id`,
		f.userID, fmt.Sprintf("margin-series-hash-%d", suffix))
	mustScan(t, pool, ctx, &f.provider, `
		INSERT INTO providers (slug, name, origin, status)
		VALUES ($1, $1, 'https://margin.example.test', 'enabled') RETURNING id`,
		fmt.Sprintf("margin-series-%d", suffix))
	mustScan(t, pool, ctx, &f.channel, `
		INSERT INTO channels (provider_id, name, protocols, adapter_key, credential, status, priority)
		VALUES ($1, $2, ARRAY['openai']::text[], ARRAY['openai']::text[], 'sk-x', 'enabled', 10)
		RETURNING id`, f.provider, fmt.Sprintf("margin-series-ch-%d", suffix))
	// cost_snapshots 外键指向 (channel_id, model_id, upstream_model) 绑定，先把绑定建出来。
	if _, err := pool.Exec(ctx, `
		INSERT INTO channel_models (channel_id, model_id, upstream_model, status)
		VALUES ($1, $2, 'upstream-model', 'enabled')
	`, f.channel, f.modelID); err != nil {
		t.Fatalf("insert channel model binding: %v", err)
	}

	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM cost_snapshots WHERE model_id = $1`, f.modelID)
		_, _ = pool.Exec(ctx, `DELETE FROM channel_models WHERE model_id = $1`, f.modelID)
		_, _ = pool.Exec(ctx, `DELETE FROM ledger_entries WHERE user_id = $1`, f.userID)
		_, _ = pool.Exec(ctx, `DELETE FROM request_records WHERE user_id = $1`, f.userID)
		_, _ = pool.Exec(ctx, `DELETE FROM api_keys WHERE user_id = $1`, f.userID)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, f.userID)
		_, _ = pool.Exec(ctx, `DELETE FROM channels WHERE id = $1`, f.channel)
		_, _ = pool.Exec(ctx, `DELETE FROM providers WHERE id = $1`, f.provider)
		_, _ = pool.Exec(ctx, `DELETE FROM models WHERE id = $1`, f.modelID)
		pool.Close()
		cancel()
	})
	return f
}

func mustScan(t *testing.T, pool *pgxpool.Pool, ctx context.Context, dest *int64, sql string, args ...any) {
	t.Helper()
	if err := pool.QueryRow(ctx, sql, args...).Scan(dest); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

// billedRequest 写一条成功请求，并挂上收入与成本。ageMinutes 控制它落在哪个小时桶。
func (f *marginFixture) billedRequest(revenue, cost string, ageMinutes int) {
	f.t.Helper()
	f.seq++
	requestID := fmt.Sprintf("margin-req-%d-%d", time.Now().UnixNano(), f.seq)

	var recordID int64
	mustScan(f.t, f.pool, f.ctx, &recordID, `
		INSERT INTO request_records (
			request_id, user_id, api_key_id, requested_model_id,
			ingress_protocol, endpoint, stream, status, started_at, completed_at, created_at
		)
		VALUES ($1, $2, $3, $4, 'openai', 'chat_completions', false, 'succeeded',
			now() - make_interval(mins => $5::int),
			now() - make_interval(mins => $5::int),
			now() - make_interval(mins => $5::int))
		RETURNING id`, requestID, f.userID, f.apiKeyID, f.publicID, ageMinutes)

	if _, err := f.pool.Exec(f.ctx, `
		INSERT INTO ledger_entries (
			user_id, request_record_id, entry_type, amount, currency,
			balance_before, balance_after, idempotency_key, reason, created_at
		)
		VALUES ($1, $2, 'debit', $3::numeric, 'USD', 1000, 1000 - $3::numeric, $4, 'settlement',
			now() - make_interval(mins => $5::int))
	`, f.userID, recordID, revenue, requestID+"-debit", ageMinutes); err != nil {
		f.t.Fatalf("insert ledger entry: %v", err)
	}

	if _, err := f.pool.Exec(f.ctx, `
		INSERT INTO cost_snapshots (
			request_record_id, provider_id, channel_id, model_id, upstream_model,
			currency, pricing_unit, uncached_input_cost, output_cost,
			uncached_input_cost_amount, cache_read_input_cost_amount,
			cache_write_5m_input_cost_amount, cache_write_1h_input_cost_amount,
			cache_write_30m_input_cost_amount, output_cost_amount,
			reasoning_output_cost_amount, total_cost_amount, formula_version, created_at
		)
		VALUES ($1, $2, $3, $4, 'upstream-model', 'USD', 'per_1m_tokens', 1, 2,
			$5::numeric, 0, 0, 0, 0, 0, 0, $5::numeric, 'v1',
			now() - make_interval(mins => $6::int))
	`, recordID, f.provider, f.channel, f.modelID, cost, ageMinutes); err != nil {
		f.t.Fatalf("insert cost snapshot: %v", err)
	}
}

func TestModelOpsMarginSeriesMatchesDetail(t *testing.T) {
	f := newMarginFixture(t)
	// 铺在不同小时桶上，确保时序真的分了桶而不是只有一行。
	f.billedRequest("1.5", "0.4", 10)
	f.billedRequest("2.5", "0.6", 70)
	f.billedRequest("3", "1", 130)

	from := time.Now().Add(-6 * time.Hour)
	to := time.Now().Add(time.Minute)

	detail, err := f.service.Detail(f.ctx, f.modelID, from, to)
	if err != nil {
		t.Fatalf("model ops detail: %v", err)
	}
	points, err := f.service.PerformanceTimeseries(f.ctx, f.modelID, "hour", from, to)
	if err != nil {
		t.Fatalf("model ops performance timeseries: %v", err)
	}
	if len(points) < 3 {
		t.Fatalf("expected at least 3 hourly buckets, got %d", len(points))
	}

	var revenue, cost, margin float64
	var requests int64
	for _, p := range points {
		revenue += parseAmount(t, p.RevenueUSD)
		cost += parseAmount(t, p.CostUSD)
		margin += parseAmount(t, p.MarginUSD)
		requests += p.RequestTotal
	}

	assertAmountEqual(t, "revenue", revenue, parseAmount(t, detail.RevenueUSD))
	assertAmountEqual(t, "cost", cost, parseAmount(t, detail.CostUSD))
	assertAmountEqual(t, "margin", margin, parseAmount(t, detail.MarginUSD))
	if requests != detail.RequestTotal {
		t.Fatalf("timeseries request total = %d, detail = %d", requests, detail.RequestTotal)
	}
}

func parseAmount(t *testing.T, raw string) float64 {
	t.Helper()
	if raw == "" {
		return 0
	}
	var out float64
	if _, err := fmt.Sscanf(raw, "%g", &out); err != nil {
		t.Fatalf("parse amount %q: %v", raw, err)
	}
	return out
}

func assertAmountEqual(t *testing.T, label string, got, want float64) {
	t.Helper()
	if diff := got - want; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("%s: timeseries sum = %v, detail = %v", label, got, want)
	}
}
