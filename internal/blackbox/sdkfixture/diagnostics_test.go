//go:build blackbox

package sdkfixture

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestFailureAuditSnapshotCapturesAnthropicStreamUpstreamStatus(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"api_error","message":"mock upstream failure"}}`))
	}))
	t.Cleanup(upstream.Close)

	f := Setup(t, SetupOptions{
		Mode:            UpstreamMock,
		UpstreamBaseURL: upstream.URL,
		Protocol:        "anthropic",
		AdapterKey:      "anthropic",
	})
	req, err := http.NewRequest(
		http.MethodPost,
		f.Server.URL+"/v1/messages",
		bytes.NewBufferString(`{"model":"deepseek-v4-flash","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hello"}]}`),
	)
	if err != nil {
		t.Fatalf("create Anthropic stream request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("x-api-key", f.APIKey)

	resp, err := f.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("send Anthropic stream request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("gateway status = %d, want %d", resp.StatusCode, http.StatusBadGateway)
	}

	snapshot, err := f.loadFailureAuditSnapshot(context.Background())
	if err != nil {
		t.Fatalf("load failure audit snapshot: %v", err)
	}
	if snapshot.attemptErrorCode != "adapter_upstream_status" {
		t.Fatalf("attempt error code = %q, want adapter_upstream_status", snapshot.attemptErrorCode)
	}
	if !snapshot.upstreamStatusCode.Valid || snapshot.upstreamStatusCode.Int32 != http.StatusBadGateway {
		t.Fatalf("upstream status = %+v, want 502", snapshot.upstreamStatusCode)
	}
	if got := failureStage(snapshot); got != "upstream_http_status" {
		t.Fatalf("failure stage = %q, want upstream_http_status", got)
	}
}

func TestFormatFailureAuditSnapshotClassifiesFailureStage(t *testing.T) {
	start := time.Unix(100, 0)
	complete := start.Add(5 * time.Second)
	tests := []struct {
		name     string
		snapshot failureAuditSnapshot
		want     string
	}{
		{
			name: "upstream HTTP status",
			snapshot: failureAuditSnapshot{
				attemptID:          pgtype.Int8{Int64: 11, Valid: true},
				attemptErrorCode:   "adapter_upstream_status",
				upstreamStatusCode: pgtype.Int4{Int32: 502, Valid: true},
				upstreamStartedAt:  pgtype.Timestamptz{Time: start, Valid: true},
				upstreamCompletedAt: pgtype.Timestamptz{
					Time: complete, Valid: true,
				},
			},
			want: `failure_stage="upstream_http_status"`,
		},
		{
			name: "response header timeout",
			snapshot: failureAuditSnapshot{
				attemptID:           pgtype.Int8{Int64: 12, Valid: true},
				attemptErrorCode:    "adapter_send_request_failed",
				upstreamStartedAt:   pgtype.Timestamptz{Time: start, Valid: true},
				upstreamCompletedAt: pgtype.Timestamptz{Time: complete, Valid: true},
			},
			want: `failure_stage="before_response_headers"`,
		},
		{
			name: "stream read after first token",
			snapshot: failureAuditSnapshot{
				attemptID:            pgtype.Int8{Int64: 13, Valid: true},
				attemptErrorCode:     "adapter_read_stream_failed",
				upstreamStartedAt:    pgtype.Timestamptz{Time: start, Valid: true},
				upstreamFirstTokenAt: pgtype.Timestamptz{Time: start.Add(time.Second), Valid: true},
				upstreamCompletedAt:  pgtype.Timestamptz{Time: complete, Valid: true},
			},
			want: `failure_stage="after_first_token"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatFailureAuditSnapshot(tt.snapshot)
			if !strings.Contains(got, tt.want) {
				t.Fatalf("diagnostic = %q, want substring %q", got, tt.want)
			}
			if strings.Contains(got, "internal_error_detail") || strings.Contains(got, "response_snippet") {
				t.Fatalf("diagnostic exposes forbidden detail field: %q", got)
			}
		})
	}
}

func TestFormatFailureAuditSnapshotIncludesOnlySanitizedFacts(t *testing.T) {
	start := time.Unix(200, 0)
	got := formatFailureAuditSnapshot(failureAuditSnapshot{
		requestID:              21,
		requestStatus:          "failed",
		requestErrorCode:       "adapter_upstream_status",
		deliveryStatus:         "not_started",
		requestResponseStarted: false,
		attemptID:              pgtype.Int8{Int64: 22, Valid: true},
		attemptStatus:          "failed",
		attemptErrorCode:       "adapter_upstream_status",
		faultParty:             "upstream",
		upstreamStatusCode:     pgtype.Int4{Int32: 429, Valid: true},
		upstreamStartedAt:      pgtype.Timestamptz{Time: start, Valid: true},
		upstreamCompletedAt:    pgtype.Timestamptz{Time: start.Add(1250 * time.Millisecond), Valid: true},
		endpointDisposition:    "applied",
		channelDisposition:     "applied",
	})

	for _, want := range []string{
		"request_record_id=21",
		`request_error_code="adapter_upstream_status"`,
		"upstream_status_code=429",
		"transport_duration_ms=1250",
		`endpoint_disposition="applied"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("diagnostic = %q, want substring %q", got, want)
		}
	}
}
