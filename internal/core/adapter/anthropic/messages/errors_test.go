package messages

import (
	"context"
	"errors"
	"testing"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

func TestStreamSendFirstTokenTimeoutBeforeHeadersPreservesTimeoutCause(t *testing.T) {
	err := newUpstreamSendErrorWithContextCause(
		context.Canceled,
		adapter.ErrFirstTokenTimeout,
		"send stream messages request",
	)

	category, ok := adapter.UpstreamCategoryOf(err)
	if !ok || category != adapter.UpstreamErrorTimeout {
		t.Fatalf("category = %q ok=%v, want timeout", category, ok)
	}
	if !errors.Is(err, adapter.ErrFirstTokenTimeout) {
		t.Fatalf("expected first-token timeout in error chain, got %v", err)
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("server first-token timeout must not look like client cancel: %v", err)
	}
	if failure.CodeOf(err) != failure.CodeAdapterSendRequestFailed {
		t.Fatalf("failure code = %q, want %q", failure.CodeOf(err), failure.CodeAdapterSendRequestFailed)
	}
}
