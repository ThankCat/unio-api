package lifecycle

import (
	"context"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

func upstreamErr(category adapter.UpstreamErrorCategory, status int, cause error) error {
	return adapter.NewUpstreamError(category, adapter.UpstreamMetadata{StatusCode: status}, cause)
}

func TestClassifyChannelScoringSampleUsesProtocolFailureOutcome(t *testing.T) {
	eligible, isError := classifyChannelScoringSample(breakerstore.FinishOutcome{
		ChannelOutcome: breakerstore.OutcomeEligibleFailure,
	}, nil)
	if !eligible || !isError {
		t.Fatalf("protocol failure sample = (%v,%v), want (true,true)", eligible, isError)
	}
}

func TestClassifyChannelSampleError(t *testing.T) {
	protocolCause := failure.New(failure.CodeAdapterInvalidResponse, failure.WithMessage("corrupt"))
	platformCause := failure.New(failure.CodeGatewayBreakerStoreUnavailable, failure.WithMessage("redis down"))

	cases := []struct {
		name         string
		err          error
		wantEligible bool
		wantIsError  bool
	}{
		{"success", nil, true, false},
		{"client_cancel", context.Canceled, false, false},
		{"upstream_canceled", upstreamErr(adapter.UpstreamErrorCanceled, 0, nil), false, false},
		{"rate_limit_429", upstreamErr(adapter.UpstreamErrorRateLimit, 429, nil), false, false},
		{"auth_401", upstreamErr(adapter.UpstreamErrorAuth, 401, nil), true, true},
		{"permission_403", upstreamErr(adapter.UpstreamErrorPermission, 403, nil), true, true},
		{"client_400", upstreamErr(adapter.UpstreamErrorBadRequest, 400, nil), false, false},
		{"client_404", upstreamErr(adapter.UpstreamErrorBadRequest, 404, nil), false, false},
		{"server_500", upstreamErr(adapter.UpstreamErrorServer, 500, nil), true, true},
		{"server_no_status", upstreamErr(adapter.UpstreamErrorServer, 0, nil), true, true},
		{"timeout", upstreamErr(adapter.UpstreamErrorTimeout, 0, nil), true, true},
		{"unknown_with_protocol_failure", upstreamErr(adapter.UpstreamErrorUnknown, 200, protocolCause), true, true},
		{"unknown_without_protocol_failure", upstreamErr(adapter.UpstreamErrorUnknown, 200, nil), false, false},
		{"no_category_protocol_failure", protocolCause, true, true},
		{"no_category_platform_error", platformCause, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eligible, isError := classifyChannelSampleError(tc.err)
			if eligible != tc.wantEligible || isError != tc.wantIsError {
				t.Fatalf("classify(%s) = (%v,%v), want (%v,%v)", tc.name, eligible, isError, tc.wantEligible, tc.wantIsError)
			}
		})
	}
}

func TestSampleTTFTMsNonStreamNever(t *testing.T) {
	start := time.Unix(1_000_000, 0)
	first := start.Add(800 * time.Millisecond)
	facts := AttemptTimingFacts{UpstreamStartedAt: &start, UpstreamFirstTokenAt: &first}
	if got := sampleTTFTMs(false, facts); got != nil {
		t.Fatalf("non-stream TTFT must be nil, got %v", *got)
	}
}

func TestSampleTTFTMsStreamFirstToken(t *testing.T) {
	start := time.Unix(1_000_000, 0)
	first := start.Add(850 * time.Millisecond)
	facts := AttemptTimingFacts{UpstreamStartedAt: &start, UpstreamFirstTokenAt: &first}
	got := sampleTTFTMs(true, facts)
	if got == nil || *got != 850 {
		t.Fatalf("stream first-token TTFT = %v, want 850", got)
	}
}

func TestSampleTTFTMsStreamFirstTokenTimeoutIsNotTTFT(t *testing.T) {
	start := time.Unix(1_000_000, 0)
	completed := start.Add(60 * time.Second)
	facts := AttemptTimingFacts{UpstreamStartedAt: &start, UpstreamCompletedAt: &completed}
	got := sampleTTFTMs(true, facts)
	if got != nil {
		t.Fatalf("first-token-timeout TTFT = %v, want nil", *got)
	}
}
