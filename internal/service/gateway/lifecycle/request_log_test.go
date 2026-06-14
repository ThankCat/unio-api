package lifecycle

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ThankCat/unio-api/internal/core/routing"
	"github.com/ThankCat/unio-api/internal/platform/failure"
)

func TestRoutingFailureCodeClassifiesRoutingErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "model not found",
			err:  routing.ErrModelNotFound,
			want: "model_not_found",
		},
		{
			name: "model not available",
			err:  routing.ErrModelNotAvailable,
			want: "model_not_available",
		},
		{
			name: "no available channel",
			err:  routing.ErrNoAvailableChannel,
			want: "no_available_channel",
		},
		{
			name: "unknown routing error",
			err:  errors.New("routing database failed"),
			want: "routing_error",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RoutingFailureCode(tc.err); got != tc.want {
				t.Fatalf("expected code %q, got %q", tc.want, got)
			}
		})
	}
}

func TestInternalErrorDetailIsTruncated(t *testing.T) {
	rawErr := errors.New(strings.Repeat("x", MaxRequestLogInternalErrorDetailBytes+100))

	detail := InternalErrorDetail(rawErr)

	if len(detail) <= MaxRequestLogInternalErrorDetailBytes {
		t.Fatalf("expected truncated detail marker to extend stored detail, got length %d", len(detail))
	}
	if !strings.HasSuffix(detail, "...[truncated]") {
		t.Fatalf("expected truncated marker, got %q", detail[len(detail)-20:])
	}
}

// TestInternalErrorDetailIncludesCauseChain 锁定:流式断连这类错误必须把底层根因
// (如 context canceled)拼进 detail,而非只留 failure 顶层归类文案,否则排查无从下手。
func TestInternalErrorDetailIncludesCauseChain(t *testing.T) {
	wrapped := failure.Wrap(
		failure.CodeAdapterReadStreamFailed,
		context.Canceled,
		failure.WithMessage("openai responses adapter read stream event"),
	)

	detail := InternalErrorDetail(wrapped)

	if !strings.Contains(detail, "openai responses adapter read stream event") {
		t.Fatalf("detail missing top message: %q", detail)
	}
	if !strings.Contains(detail, context.Canceled.Error()) {
		t.Fatalf("detail missing root cause %q: %q", context.Canceled.Error(), detail)
	}
}

func TestFailureCodeOrFallbackReturnsFallbackWhenNoFailureCode(t *testing.T) {
	if got := FailureCodeOrFallback(errors.New("plain error"), "fallback_code"); got != "fallback_code" {
		t.Fatalf("expected fallback_code, got %q", got)
	}
}

func TestBaseSafeRequestLogErrorMessageFallsBackByCategory(t *testing.T) {
	cases := []struct {
		code string
		want string
	}{
		{code: "client_canceled", want: "Request was canceled by the client."},
		{code: "adapter_some_error", want: "Upstream provider request failed."},
		{code: "routing_pick_failed", want: "Request routing failed."},
		{code: "ledger_oops", want: "Request billing failed."},
		{code: "billing_oops", want: "Request billing failed."},
		{code: "gateway_oops", want: "Gateway request failed."},
		{code: "totally_unknown", want: "Request failed."},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			if got := BaseSafeRequestLogErrorMessage(tc.code); got != tc.want {
				t.Fatalf("expected %q, got %q", tc.want, got)
			}
		})
	}
}
