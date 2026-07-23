package lifecycle

import (
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

func TestProvableRetryAfter(t *testing.T) {
	upstream := adapter.NewUpstreamError(
		adapter.UpstreamErrorRateLimit,
		adapter.UpstreamMetadata{StatusCode: 429, RetryAfter: 1500 * time.Millisecond},
		failure.New(failure.CodeAdapterUpstreamStatus),
	)
	if got := ProvableRetryAfter(upstream); got != 1500*time.Millisecond {
		t.Fatalf("upstream retry after = %v", got)
	}

	cooldown := failure.New(
		failure.CodeGatewayChannelRateLimited,
		failure.WithField("retry_after_ms", int64(2001)),
	)
	if got := ProvableRetryAfter(cooldown); got != 2001*time.Millisecond {
		t.Fatalf("cooldown retry after = %v", got)
	}

	if got := ProvableRetryAfter(failure.New(failure.CodeGatewayChannelRateLimited)); got != 0 {
		t.Fatalf("unproven retry after = %v", got)
	}
}
