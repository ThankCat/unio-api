package lifecycle

import (
	"errors"
	"net/http"
	"testing"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

// TestProviderErrorClassifierIsRetryable 验证 retry 决策只对可安全切换候选的上游故障放行。
func TestProviderErrorClassifierIsRetryable(t *testing.T) {
	tests := []struct {
		name     string
		category adapter.UpstreamErrorCategory
		status   int
		want     bool
	}{
		{"rate_limit retryable", adapter.UpstreamErrorRateLimit, 0, true},
		{"timeout retryable", adapter.UpstreamErrorTimeout, 0, true},
		{"server_error retryable", adapter.UpstreamErrorServer, 0, true},
		{"auth retryable (fallback to another key)", adapter.UpstreamErrorAuth, 0, true},
		{"explicit permission 403 retryable", adapter.UpstreamErrorPermission, http.StatusForbidden, true},
		{"permission without status not retryable", adapter.UpstreamErrorPermission, 0, false},
		{"permission with non-403 status not retryable", adapter.UpstreamErrorPermission, 200, false},
		{"bad_request not retryable", adapter.UpstreamErrorBadRequest, 0, false},
		{"canceled not retryable", adapter.UpstreamErrorCanceled, 0, false},
		{"unknown not retryable", adapter.UpstreamErrorUnknown, 0, false},
	}

	classifier := ProviderErrorClassifier{}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := adapter.NewUpstreamError(
				tc.category,
				adapter.UpstreamMetadata{StatusCode: tc.status},
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

// TestNeverRetryClassifier 验证保守分类器对任何错误都不重试。
func TestNeverRetryClassifier(t *testing.T) {
	classifier := NeverRetryClassifier{}

	if classifier.IsRetryable(errors.New("any error")) {
		t.Fatal("NeverRetryClassifier must never retry")
	}
	if classifier.IsRetryable(nil) {
		t.Fatal("NeverRetryClassifier must never retry nil")
	}
}
