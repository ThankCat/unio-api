package chatcompletions

import (
	"errors"
	"testing"

	"github.com/ThankCat/unio-api/internal/core/adapter"
	"github.com/ThankCat/unio-api/internal/platform/failure"
)

// TestProviderErrorClassifierIsRetryable 验证 retry 决策只对瞬时上游故障放行。
func TestProviderErrorClassifierIsRetryable(t *testing.T) {
	tests := []struct {
		name     string
		category adapter.UpstreamErrorCategory
		want     bool
	}{
		{"rate_limit retryable", adapter.UpstreamErrorRateLimit, true},
		{"timeout retryable", adapter.UpstreamErrorTimeout, true},
		{"server_error retryable", adapter.UpstreamErrorServer, true},
		{"auth not retryable", adapter.UpstreamErrorAuth, false},
		{"permission not retryable", adapter.UpstreamErrorPermission, false},
		{"bad_request not retryable", adapter.UpstreamErrorBadRequest, false},
		{"canceled not retryable", adapter.UpstreamErrorCanceled, false},
		{"unknown not retryable", adapter.UpstreamErrorUnknown, false},
	}

	classifier := ProviderErrorClassifier{}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := adapter.NewUpstreamError(
				tc.category,
				adapter.UpstreamMetadata{},
				failure.New(failure.CodeAdapterUpstreamStatus),
			)

			if got := classifier.IsRetryable(err); got != tc.want {
				t.Fatalf("IsRetryable(%q): got %v, want %v", tc.category, got, tc.want)
			}
		})
	}
}

// TestProviderErrorClassifierUnclassifiedError 验证链上没有上游分类时保守地不重试。
func TestProviderErrorClassifierUnclassifiedError(t *testing.T) {
	classifier := ProviderErrorClassifier{}

	if classifier.IsRetryable(errors.New("some unclassified error")) {
		t.Fatal("unclassified error should not be retryable")
	}

	// 即使是携带 failure code 的错误，只要没有上游分类，也不应重试。
	if classifier.IsRetryable(failure.New(failure.CodeGatewayAdapterNotRegistered)) {
		t.Fatal("non-upstream failure should not be retryable")
	}
}
