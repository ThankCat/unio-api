//go:build blackbox

package sdkfixture

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	adminquery "github.com/ThankCat/unio-gateway/internal/service/admin/query"
)

// RequestFactsExpectation 是真实上游 smoke 共用的最小请求、TTFT、usage 与落账断言。
type RequestFactsExpectation struct {
	IngressProtocol string
	Operation       string
	Stream          bool
}

// AssertLatestRequestFacts 等待同步 settlement 及尽力写入的 timing audit 收口，然后验证
// 当前 fixture 最新请求的基础事实。TTFT 只认 request_attempts.upstream_first_token_at：
// 非流式必须为 NULL，流式必须有协议定义的 FirstToken 时间。
func (f *Fixture) AssertLatestRequestFacts(t *testing.T, want RequestFactsExpectation) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var (
		facts       latestRequestFacts
		lastFacts   latestRequestFacts
		err         error
		lastLoadErr error
	)
	for {
		facts, err = f.loadLatestRequestFacts(ctx)
		if facts.requestID != 0 {
			lastFacts = facts
		}
		if err != nil {
			lastLoadErr = err
		}
		if err == nil && facts.complete(want.Stream) {
			break
		}
		if ctx.Err() != nil {
			if lastFacts.requestID != 0 {
				facts = lastFacts
			}
			if lastLoadErr != nil {
				t.Fatalf("wait for latest request facts: %v (status=%q attempt=%q timing=%v/%v/%v usage=%d debit=%d price=%d cost=%d trace_route=%d trace_candidates=%d)",
					lastLoadErr, facts.requestStatus, facts.attemptStatus,
					facts.upstreamStartedAt.Valid, facts.upstreamFirstTokenAt.Valid, facts.upstreamCompletedAt.Valid,
					facts.usageCount, facts.debitCount, facts.priceSnapshotCount, facts.costSnapshotCount,
					facts.traceRouteID, facts.traceCandidateCount)
			}
			t.Fatalf("latest request facts did not settle before timeout: status=%q attempt=%q usage=%d debit=%d price=%d cost=%d",
				facts.requestStatus, facts.attemptStatus, facts.usageCount, facts.debitCount,
				facts.priceSnapshotCount, facts.costSnapshotCount)
		}
		time.Sleep(50 * time.Millisecond)
	}

	if facts.requestStatus != "succeeded" {
		t.Errorf("request_records.status = %q, want succeeded", facts.requestStatus)
	}
	if facts.ingressProtocol != want.IngressProtocol {
		t.Errorf("request_records.ingress_protocol = %q, want %q", facts.ingressProtocol, want.IngressProtocol)
	}
	if facts.operation != want.Operation {
		t.Errorf("request_records.operation = %q, want %q", facts.operation, want.Operation)
	}
	if facts.stream != want.Stream {
		t.Errorf("request_records.stream = %v, want %v", facts.stream, want.Stream)
	}
	if !facts.finalChannelID.Valid || facts.finalChannelID.Int64 != f.ChannelID {
		t.Errorf("request_records.final_channel_id = %v, want %d", facts.finalChannelID, f.ChannelID)
	}

	if facts.attemptStatus != "succeeded" {
		t.Errorf("request_attempts.status = %q, want succeeded", facts.attemptStatus)
	}
	if facts.upstreamProtocol != want.IngressProtocol {
		t.Errorf("request_attempts.upstream_protocol = %q, want %q", facts.upstreamProtocol, want.IngressProtocol)
	}
	if !facts.upstreamStatusCode.Valid || facts.upstreamStatusCode.Int32 != 200 {
		t.Errorf("request_attempts.upstream_status_code = %v, want 200", facts.upstreamStatusCode)
	}
	if !facts.upstreamStartedAt.Valid || !facts.upstreamCompletedAt.Valid {
		t.Errorf("request_attempts transport timing is incomplete: started=%v completed=%v",
			facts.upstreamStartedAt.Valid, facts.upstreamCompletedAt.Valid)
	} else if facts.upstreamCompletedAt.Time.Before(facts.upstreamStartedAt.Time) {
		t.Errorf("request_attempts upstream_completed_at precedes upstream_started_at")
	}
	if want.Stream {
		if !facts.upstreamFirstTokenAt.Valid {
			t.Error("stream request_attempts.upstream_first_token_at is NULL")
		} else if facts.upstreamFirstTokenAt.Time.Before(facts.upstreamStartedAt.Time) ||
			facts.upstreamFirstTokenAt.Time.After(facts.upstreamCompletedAt.Time) {
			t.Error("stream request_attempts FirstToken timing is outside transport bounds")
		}
	} else if facts.upstreamFirstTokenAt.Valid {
		t.Error("non-stream request_attempts.upstream_first_token_at must be NULL")
	}

	if facts.usageCount != 1 || facts.inputTokens <= 0 || facts.outputTokens <= 0 || facts.usageMappingVersion == "" {
		t.Errorf("usage_records basic facts invalid: count=%d input=%d output=%d mapping_version_empty=%v",
			facts.usageCount, facts.inputTokens, facts.outputTokens, facts.usageMappingVersion == "")
	}
	if facts.debitCount < 1 {
		t.Errorf("ledger_entries debit count = %d, want >= 1", facts.debitCount)
	}
	if facts.priceSnapshotCount != 1 || facts.costSnapshotCount != 1 {
		t.Errorf("billing snapshots = price:%d cost:%d, want 1 each",
			facts.priceSnapshotCount, facts.costSnapshotCount)
	}
	if facts.traceRouteID != f.RouteID || facts.traceProtocol != want.IngressProtocol || facts.traceOperation != want.Operation {
		t.Errorf("routing trace identity = route:%d protocol:%q operation:%q, want route:%d protocol:%q operation:%q",
			facts.traceRouteID, facts.traceProtocol, facts.traceOperation,
			f.RouteID, want.IngressProtocol, want.Operation)
	}
	if facts.traceCandidateCount < 1 || !slices.Contains(facts.traceSelectedOrder, f.ChannelID) || facts.traceAlgorithmVersion == "" {
		t.Errorf("routing trace plan is incomplete: candidates=%d selected=%v algorithm_empty=%v",
			facts.traceCandidateCount, facts.traceSelectedOrder, facts.traceAlgorithmVersion == "")
	}

	f.assertAdminRequestFacts(t, ctx, facts, want.Stream)
}

type latestRequestFacts struct {
	requestID             int64
	requestStatus         string
	ingressProtocol       string
	operation             string
	stream                bool
	finalChannelID        pgtype.Int8
	attemptStatus         string
	upstreamProtocol      string
	upstreamStatusCode    pgtype.Int4
	upstreamStartedAt     pgtype.Timestamptz
	upstreamFirstTokenAt  pgtype.Timestamptz
	upstreamCompletedAt   pgtype.Timestamptz
	usageCount            int64
	inputTokens           int64
	outputTokens          int64
	usageMappingVersion   string
	debitCount            int64
	priceSnapshotCount    int64
	costSnapshotCount     int64
	traceRouteID          int64
	traceProtocol         string
	traceOperation        string
	traceCandidateCount   int32
	traceSelectedOrder    []int64
	traceAlgorithmVersion string
}

func (f latestRequestFacts) complete(stream bool) bool {
	return f.requestStatus == "succeeded" && f.attemptStatus == "succeeded" &&
		f.upstreamStartedAt.Valid && f.upstreamCompletedAt.Valid && (!stream || f.upstreamFirstTokenAt.Valid) &&
		f.usageCount == 1 && f.debitCount >= 1 && f.priceSnapshotCount == 1 && f.costSnapshotCount == 1 &&
		f.traceRouteID > 0 && f.traceCandidateCount > 0 && len(f.traceSelectedOrder) > 0
}

func (f *Fixture) loadLatestRequestFacts(ctx context.Context) (latestRequestFacts, error) {
	var facts latestRequestFacts
	if err := f.Pool.QueryRow(ctx, `
		SELECT id, status, ingress_protocol, operation, stream, final_channel_id
		FROM request_records
		WHERE user_id = $1
		ORDER BY id DESC
		LIMIT 1
	`, f.UserID).Scan(
		&facts.requestID, &facts.requestStatus, &facts.ingressProtocol, &facts.operation,
		&facts.stream, &facts.finalChannelID,
	); err != nil {
		return facts, err
	}

	if err := f.Pool.QueryRow(ctx, `
		SELECT status, upstream_protocol, upstream_status_code,
		       upstream_started_at, upstream_first_token_at, upstream_completed_at
		FROM request_attempts
		WHERE request_record_id = $1
		ORDER BY attempt_index DESC
		LIMIT 1
	`, facts.requestID).Scan(
		&facts.attemptStatus, &facts.upstreamProtocol, &facts.upstreamStatusCode,
		&facts.upstreamStartedAt, &facts.upstreamFirstTokenAt, &facts.upstreamCompletedAt,
	); err != nil {
		return facts, err
	}

	var usageMappingVersion *string
	err := f.Pool.QueryRow(ctx, `
		SELECT COUNT(*),
		       COALESCE(SUM(uncached_input_tokens + cache_read_input_tokens +
		                    cache_write_5m_input_tokens + cache_write_30m_input_tokens +
		                    cache_write_1h_input_tokens), 0),
		       COALESCE(SUM(output_tokens_total), 0),
		       MAX(usage_mapping_version)
		FROM usage_records
		WHERE request_record_id = $1
	`, facts.requestID).Scan(
		&facts.usageCount, &facts.inputTokens, &facts.outputTokens, &usageMappingVersion,
	)
	if err != nil {
		return facts, err
	}
	if usageMappingVersion != nil {
		facts.usageMappingVersion = *usageMappingVersion
	}

	if err := f.Pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM ledger_entries WHERE request_record_id = $1 AND entry_type = 'debit'),
			(SELECT COUNT(*) FROM price_snapshots WHERE request_record_id = $1),
			(SELECT COUNT(*) FROM cost_snapshots WHERE request_record_id = $1)
	`, facts.requestID).Scan(
		&facts.debitCount, &facts.priceSnapshotCount, &facts.costSnapshotCount,
	); err != nil {
		return facts, err
	}

	if err := f.Pool.QueryRow(ctx, `
		SELECT route_id, protocol, operation, candidate_count, selected_order, algorithm_version
		FROM routing_decision_traces
		WHERE request_record_id = $1
	`, facts.requestID).Scan(
		&facts.traceRouteID, &facts.traceProtocol, &facts.traceOperation,
		&facts.traceCandidateCount, &facts.traceSelectedOrder, &facts.traceAlgorithmVersion,
	); err != nil {
		return facts, err
	}

	return facts, nil
}

func (f *Fixture) assertAdminRequestFacts(t *testing.T, ctx context.Context, facts latestRequestFacts, stream bool) {
	t.Helper()

	userID := f.UserID
	items, total, err := adminquery.NewRequestService(f.Queries).List(ctx, adminquery.RequestListParams{
		UserID:   &userID,
		SortDesc: true,
		Limit:    20,
	})
	if err != nil {
		t.Fatalf("load request through Admin query service: %v", err)
	}
	if total < 1 {
		t.Fatal("Admin query service returned no request rows")
	}
	for _, item := range items {
		if item.ID != facts.requestID {
			continue
		}
		if item.Status != "succeeded" || item.FinalChannelID == nil || *item.FinalChannelID != f.ChannelID {
			t.Errorf("Admin request facts = status:%q channel:%v, want succeeded/%d",
				item.Status, item.FinalChannelID, f.ChannelID)
		}
		if item.LatencyMs == nil || *item.LatencyMs < 0 {
			t.Error("Admin request total latency is missing")
		}
		if stream && item.TtftMs == nil {
			t.Error("Admin stream request TTFT is missing")
		}
		if !stream && item.TtftMs != nil {
			t.Errorf("Admin non-stream request TTFT = %d, want nil", *item.TtftMs)
		}
		if item.UncachedInputTokens+item.CacheReadInputTokens+item.CacheWrite5mInputTokens+
			item.CacheWrite30mInputTokens+item.CacheWrite1hInputTokens <= 0 || item.OutputTokens <= 0 {
			t.Error("Admin request usage facts are missing")
		}
		if item.UserChargeUSD == nil || item.TotalCostUSD == nil {
			t.Error("Admin request billing facts are missing")
		}
		return
	}
	t.Fatalf("Admin query service did not return request_record_id=%d", facts.requestID)
}
