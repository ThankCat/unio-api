//go:build blackbox

package sdkfixture

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// failureAuditSnapshot intentionally excludes credentials, request/response bodies,
// upstream error snippets, and internal_error_detail. It is safe to print when a
// real-upstream blackbox test fails and fixture teardown is about to delete its rows.
type failureAuditSnapshot struct {
	requestID              int64
	requestStatus          string
	requestErrorCode       string
	deliveryStatus         string
	requestResponseStarted bool

	attemptID            pgtype.Int8
	attemptStatus        string
	attemptErrorCode     string
	faultParty           string
	upstreamStatusCode   pgtype.Int4
	upstreamStartedAt    pgtype.Timestamptz
	upstreamFirstTokenAt pgtype.Timestamptz
	upstreamCompletedAt  pgtype.Timestamptz
	endpointDisposition  string
	channelDisposition   string
}

// registerFailureAuditDiagnostics preserves the last useful, sanitized audit facts
// in test output before teardown removes the fixture rows.
func (f *Fixture) registerFailureAuditDiagnostics(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		snapshot, err := f.loadFailureAuditSnapshot(ctx)
		if err != nil {
			t.Logf("blackbox failure audit unavailable: %v", err)
			return
		}
		t.Logf("blackbox failure audit: %s", formatFailureAuditSnapshot(snapshot))
	})
}

func (f *Fixture) loadFailureAuditSnapshot(ctx context.Context) (failureAuditSnapshot, error) {
	var snapshot failureAuditSnapshot
	err := f.Pool.QueryRow(ctx, `
		SELECT
			rr.id,
			rr.status,
			COALESCE(rr.error_code, ''),
			rr.delivery_status,
			rr.response_started_at IS NOT NULL,
			ra.id,
			COALESCE(ra.status, ''),
			COALESCE(ra.error_code, ''),
			COALESCE(ra.fault_party, ''),
			ra.upstream_status_code,
			ra.upstream_started_at,
			ra.upstream_first_token_at,
			ra.upstream_completed_at,
			COALESCE(ra.breaker_endpoint_disposition, ''),
			COALESCE(ra.breaker_channel_disposition, '')
		FROM request_records rr
		LEFT JOIN LATERAL (
			SELECT *
			FROM request_attempts
			WHERE request_record_id = rr.id
			ORDER BY attempt_index DESC
			LIMIT 1
		) ra ON true
		WHERE rr.user_id = $1
		ORDER BY rr.id DESC
		LIMIT 1
	`, f.UserID).Scan(
		&snapshot.requestID,
		&snapshot.requestStatus,
		&snapshot.requestErrorCode,
		&snapshot.deliveryStatus,
		&snapshot.requestResponseStarted,
		&snapshot.attemptID,
		&snapshot.attemptStatus,
		&snapshot.attemptErrorCode,
		&snapshot.faultParty,
		&snapshot.upstreamStatusCode,
		&snapshot.upstreamStartedAt,
		&snapshot.upstreamFirstTokenAt,
		&snapshot.upstreamCompletedAt,
		&snapshot.endpointDisposition,
		&snapshot.channelDisposition,
	)
	return snapshot, err
}

func formatFailureAuditSnapshot(snapshot failureAuditSnapshot) string {
	return fmt.Sprintf(
		"request_record_id=%d request_status=%q request_error_code=%q delivery_status=%q request_response_started=%t "+
			"attempt_id=%s attempt_status=%q attempt_error_code=%q fault_party=%q upstream_status_code=%s "+
			"failure_stage=%q transport_started=%t first_token=%t transport_completed=%t transport_duration_ms=%s "+
			"endpoint_disposition=%q channel_disposition=%q",
		snapshot.requestID,
		snapshot.requestStatus,
		snapshot.requestErrorCode,
		snapshot.deliveryStatus,
		snapshot.requestResponseStarted,
		formatNullableInt64(snapshot.attemptID),
		snapshot.attemptStatus,
		snapshot.attemptErrorCode,
		snapshot.faultParty,
		formatNullableInt32(snapshot.upstreamStatusCode),
		failureStage(snapshot),
		snapshot.upstreamStartedAt.Valid,
		snapshot.upstreamFirstTokenAt.Valid,
		snapshot.upstreamCompletedAt.Valid,
		formatTransportDurationMS(snapshot),
		snapshot.endpointDisposition,
		snapshot.channelDisposition,
	)
}

func failureStage(snapshot failureAuditSnapshot) string {
	switch {
	case !snapshot.attemptID.Valid:
		return "before_attempt"
	case !snapshot.upstreamStartedAt.Valid:
		return "before_transport"
	case snapshot.upstreamStatusCode.Valid && snapshot.upstreamStatusCode.Int32 >= 400:
		return "upstream_http_status"
	case snapshot.upstreamFirstTokenAt.Valid:
		return "after_first_token"
	case snapshot.attemptErrorCode == "adapter_stream_idle_timeout":
		return "stream_idle_before_first_token"
	case snapshot.attemptErrorCode == "adapter_read_stream_failed":
		return "stream_read_before_first_token"
	case snapshot.attemptErrorCode == "adapter_send_request_failed":
		return "before_response_headers"
	case snapshot.upstreamCompletedAt.Valid:
		return "transport_completed_before_first_token"
	default:
		return "transport_started_before_first_token"
	}
}

func formatTransportDurationMS(snapshot failureAuditSnapshot) string {
	if !snapshot.upstreamStartedAt.Valid || !snapshot.upstreamCompletedAt.Valid {
		return "null"
	}
	duration := snapshot.upstreamCompletedAt.Time.Sub(snapshot.upstreamStartedAt.Time)
	if duration < 0 {
		duration = 0
	}
	return fmt.Sprintf("%d", duration.Milliseconds())
}

func formatNullableInt64(value pgtype.Int8) string {
	if !value.Valid {
		return "null"
	}
	return fmt.Sprintf("%d", value.Int64)
}

func formatNullableInt32(value pgtype.Int4) string {
	if !value.Valid {
		return "null"
	}
	return fmt.Sprintf("%d", value.Int32)
}
