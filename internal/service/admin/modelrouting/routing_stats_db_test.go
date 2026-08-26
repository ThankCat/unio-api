// 选路统计的 DB 级测试（需 DATABASE_URL，缺省跳过）。
//
// 这些统计是 Route 移除后唯一能回答「流量为什么这么分」的东西，所以守两件事：
// 聚合口径必须只算完整 trace，以及时间窗必须被截断——trace_payload 里装的是全池渠道，
// 窗口一放开，展开的元素数会随保留期线性膨胀。
package modelrouting_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/admin/modelrouting"
)

type statsFixture struct {
	t        *testing.T
	ctx      context.Context
	pool     *pgxpool.Pool
	service  *modelrouting.Service
	modelID  int64
	publicID string
	userID   int64
	apiKeyID int64
	channels map[string]int64
	provider int64
	seq      int
}

func newStatsFixture(t *testing.T) *statsFixture {
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
	f := &statsFixture{
		t:    t,
		ctx:  ctx,
		pool: pool,
		// 统计类接口不碰 Redis，运行态依赖传 nil 即可。
		service:  modelrouting.NewService(sqlc.New(pool), nil, nil, nil),
		channels: map[string]int64{},
	}
	suffix := time.Now().UnixNano()
	f.publicID = fmt.Sprintf("openai/routing-stats-%d", suffix)

	scan(t, pool, ctx, &f.modelID, `
		INSERT INTO models (model_id, display_name, owned_by, status)
		VALUES ($1, $1, 'test', 'enabled') RETURNING id`, f.publicID)
	scan(t, pool, ctx, &f.userID, `
		INSERT INTO users (uid, email, password_hash, display_name)
		VALUES (gen_random_uuid(), $1, 'x', 'routing stats') RETURNING id`,
		fmt.Sprintf("routing-stats-%d@test.local", suffix))
	scan(t, pool, ctx, &f.apiKeyID, `
		INSERT INTO api_keys (user_id, name, key_prefix, key_hash)
		VALUES ($1, 'routing stats key', 'sk-test', $2) RETURNING id`,
		f.userID, fmt.Sprintf("routing-stats-hash-%d", suffix))
	scan(t, pool, ctx, &f.provider, `
		INSERT INTO providers (slug, name, origin, status)
		VALUES ($1, $1, 'https://routing.example.test', 'enabled') RETURNING id`,
		fmt.Sprintf("routing-stats-%d", suffix))

	for _, name := range []string{"primary", "backup"} {
		var id int64
		scan(t, pool, ctx, &id, `
			INSERT INTO channels (provider_id, name, protocols, adapter_key, credential, status, priority)
			VALUES ($1, $2, ARRAY['openai']::text[], ARRAY['openai']::text[], 'sk-x', 'enabled', 10)
			RETURNING id`, f.provider, fmt.Sprintf("routing-stats-%s-%d", name, suffix))
		f.channels[name] = id
	}

	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM request_records WHERE user_id = $1`, f.userID)
		_, _ = pool.Exec(ctx, `DELETE FROM api_keys WHERE user_id = $1`, f.userID)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, f.userID)
		for _, id := range f.channels {
			_, _ = pool.Exec(ctx, `DELETE FROM channels WHERE id = $1`, id)
		}
		_, _ = pool.Exec(ctx, `DELETE FROM providers WHERE id = $1`, f.provider)
		_, _ = pool.Exec(ctx, `DELETE FROM models WHERE id = $1`, f.modelID)
		pool.Close()
		cancel()
	})
	return f
}

func scan(t *testing.T, pool *pgxpool.Pool, ctx context.Context, dest *int64, sql string, args ...any) {
	t.Helper()
	if err := pool.QueryRow(ctx, sql, args...).Scan(dest); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

type traceSpec struct {
	status     string
	finalized  string
	selected   string
	ageMinutes int
	// candidates 是 channel 名 → 排除原因；空串表示该候选有资格。
	candidates map[string]string
}

func (f *statsFixture) trace(spec traceSpec) {
	f.t.Helper()
	f.seq++
	requestID := fmt.Sprintf("routing-req-%d-%d", time.Now().UnixNano(), f.seq)

	var recordID int64
	scan(f.t, f.pool, f.ctx, &recordID, `
		INSERT INTO request_records (
			request_id, user_id, api_key_id, requested_model_id,
			ingress_protocol, endpoint, stream, status, started_at, created_at
		)
		VALUES ($1, $2, $3, $4, 'openai', 'chat_completions', false, 'succeeded',
			now() - make_interval(mins => $5::int), now() - make_interval(mins => $5::int))
		RETURNING id`, requestID, f.userID, f.apiKeyID, f.publicID, spec.ageMinutes)

	candidates := make([]map[string]any, 0, len(spec.candidates))
	for name, reason := range spec.candidates {
		candidate := map[string]any{
			"channel_id": f.channels[name],
			"eligible":   reason == "",
		}
		if reason != "" {
			candidate["excluded_reason"] = reason
		}
		candidates = append(candidates, candidate)
	}
	payload, err := json.Marshal(map[string]any{"candidates": candidates})
	if err != nil {
		f.t.Fatalf("marshal payload: %v", err)
	}

	var selected any
	if spec.selected != "" {
		selected = f.channels[spec.selected]
	}
	var finalResult any
	if spec.finalized != "" {
		finalResult = spec.finalized
	}

	if _, err := f.pool.Exec(f.ctx, `
		INSERT INTO routing_decision_traces (
			request_record_id, mode, requested_model_id, protocol, endpoint,
			trace_status, pool_size, eligible_count, selected_channel_id,
			final_result, trace_payload, created_at
		)
		VALUES ($1, 'balanced', $2, 'openai', 'chat_completions',
			$3, $4, $5, $6, $7, $8::jsonb, now() - make_interval(mins => $9::int))
	`, recordID, f.publicID, spec.status, len(spec.candidates),
		countEligible(spec.candidates), selected, finalResult, string(payload), spec.ageMinutes); err != nil {
		f.t.Fatalf("insert trace: %v", err)
	}
}

func countEligible(candidates map[string]string) int {
	total := 0
	for _, reason := range candidates {
		if reason == "" {
			total++
		}
	}
	return total
}

func TestRoutingStatsAggregatesSelectionAndExclusion(t *testing.T) {
	f := newStatsFixture(t)
	// 两次落到 primary，backup 每次都因熔断被排除。
	f.trace(traceSpec{
		status: "complete", finalized: "success", selected: "primary", ageMinutes: 10,
		candidates: map[string]string{"primary": "", "backup": "open"},
	})
	f.trace(traceSpec{
		status: "complete", finalized: "success", selected: "primary", ageMinutes: 20,
		candidates: map[string]string{"primary": "", "backup": "open"},
	})
	// 一次两条都不可用，没有选中任何渠道。
	f.trace(traceSpec{
		status: "complete", finalized: "no_available_channel", ageMinutes: 30,
		candidates: map[string]string{"primary": "rate_limited", "backup": "open"},
	})

	stats, err := f.service.Stats(f.ctx, f.modelID, time.Now().Add(-2*time.Hour), time.Now().Add(time.Minute))
	if err != nil {
		t.Fatalf("routing stats: %v", err)
	}

	if got := selectionFor(stats.Selections, f.channels["primary"]); got != 2 {
		t.Fatalf("primary selections = %d, want 2", got)
	}
	// 没选出渠道的那次要单独可见，不能被丢掉。
	if got := selectionFor(stats.Selections, 0); got != 1 {
		t.Fatalf("unselected count = %d, want 1", got)
	}
	if got := outcomeFor(stats.Outcomes, "no_available_channel"); got != 1 {
		t.Fatalf("no_available_channel = %d, want 1", got)
	}
	if got := exclusionFor(stats.Exclusions, "open"); got != 3 {
		t.Fatalf("breaker exclusions = %d, want 3", got)
	}
	if got := exclusionFor(stats.Exclusions, "rate_limited"); got != 1 {
		t.Fatalf("rate limited exclusions = %d, want 1", got)
	}
	if stats.TotalExclusions != 4 {
		t.Fatalf("total exclusions = %d, want 4", stats.TotalExclusions)
	}
}

// partial 是进行中或崩溃遗留，payload 不完整，计入会让占比失真。
func TestRoutingStatsSkipsIncompleteTraces(t *testing.T) {
	f := newStatsFixture(t)
	f.trace(traceSpec{
		status: "complete", finalized: "success", selected: "primary", ageMinutes: 5,
		candidates: map[string]string{"primary": "", "backup": "open"},
	})
	f.trace(traceSpec{
		status: "partial", ageMinutes: 6,
		candidates: map[string]string{"primary": "", "backup": "open"},
	})

	stats, err := f.service.Stats(f.ctx, f.modelID, time.Now().Add(-time.Hour), time.Now().Add(time.Minute))
	if err != nil {
		t.Fatalf("routing stats: %v", err)
	}
	if stats.TotalSelections != 1 {
		t.Fatalf("total selections = %d, want 1 (partial must be skipped)", stats.TotalSelections)
	}
	if stats.TotalExclusions != 1 {
		t.Fatalf("total exclusions = %d, want 1", stats.TotalExclusions)
	}
}

// 窗口必须被截断到 24 小时：payload 里是全池渠道，放开窗口会让展开量随保留期膨胀。
func TestRoutingStatsTruncatesWindow(t *testing.T) {
	f := newStatsFixture(t)
	f.trace(traceSpec{
		status: "complete", finalized: "success", selected: "primary", ageMinutes: 30,
		candidates: map[string]string{"primary": ""},
	})
	// 三天前的那条落在截断窗口之外。
	f.trace(traceSpec{
		status: "complete", finalized: "success", selected: "backup", ageMinutes: 3 * 24 * 60,
		candidates: map[string]string{"backup": ""},
	})

	requestedFrom := time.Now().Add(-7 * 24 * time.Hour)
	stats, err := f.service.Stats(f.ctx, f.modelID, requestedFrom, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatalf("routing stats: %v", err)
	}
	if !stats.WindowTruncated {
		t.Fatal("a 7-day request must report the window as truncated")
	}
	if stats.From.Before(time.Now().Add(-25 * time.Hour)) {
		t.Fatalf("effective window start = %v, want within the last 24h", stats.From)
	}
	if stats.TotalSelections != 1 {
		t.Fatalf("total selections = %d, want 1 (the 3-day-old trace is outside the window)", stats.TotalSelections)
	}
}

func TestRoutingStatsKeepsShortWindowIntact(t *testing.T) {
	f := newStatsFixture(t)
	requestedFrom := time.Now().Add(-2 * time.Hour)

	stats, err := f.service.Stats(f.ctx, f.modelID, requestedFrom, time.Now())
	if err != nil {
		t.Fatalf("routing stats: %v", err)
	}
	if stats.WindowTruncated {
		t.Fatal("a 2-hour window is inside the cap and must not be truncated")
	}
}

func selectionFor(rows []modelrouting.SelectionStat, channelID int64) int64 {
	for _, row := range rows {
		if row.ChannelID == channelID {
			return row.Selections
		}
	}
	return 0
}

func outcomeFor(rows []modelrouting.OutcomeStat, result string) int64 {
	for _, row := range rows {
		if row.FinalResult == result {
			return row.Occurrences
		}
	}
	return 0
}

func exclusionFor(rows []modelrouting.ExclusionStat, reason string) int64 {
	for _, row := range rows {
		if row.Reason == reason {
			return row.Occurrences
		}
	}
	return 0
}
