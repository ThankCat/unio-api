package console

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestRequestUnavailableKeepsDependencyDetailsInternal(t *testing.T) {
	cause := errors.New("redis connection refused")
	err := RequestUnavailable("create session", cause)

	if err.Code != CodeRequestUnavailable || err.Status != 503 {
		t.Fatalf("unexpected public error: %+v", err)
	}
	if err.Message != "The request could not be completed. Please try again later." {
		t.Fatalf("unexpected public message %q", err.Message)
	}
	if strings.Contains(strings.ToLower(err.Message), "redis") || strings.Contains(strings.ToLower(err.Message), "service") {
		t.Fatalf("public message exposes an internal dependency: %q", err.Message)
	}
	if !errors.Is(err, cause) {
		t.Fatal("expected the internal cause to remain available to server logs")
	}
}

func TestRequestUnavailableMapsClientCancelToClosedRequest(t *testing.T) {
	cause := fmt.Errorf("read user usd wallet: %w", context.Canceled)
	err := RequestUnavailable("read user usd wallet", cause)

	if err.Code != CodeRequestCanceled || err.Status != StatusClientClosedRequest {
		t.Fatalf("unexpected public error: %+v", err)
	}
	if err.Message != "The request was canceled." {
		t.Fatalf("unexpected public message %q", err.Message)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatal("expected context.Canceled to remain available to server logs")
	}
}

func TestRequestUnavailableKeepsDeadlineAsUnavailable(t *testing.T) {
	err := RequestUnavailable("read user usd wallet", context.DeadlineExceeded)
	if err.Code != CodeRequestUnavailable || err.Status != 503 {
		t.Fatalf("deadline should stay unavailable, got %+v", err)
	}
}

func TestAsClientCanceledRewritesUnavailableWhenRequestCanceled(t *testing.T) {
	err := &Error{
		Code:    CodeRequestUnavailable,
		Message: "The request could not be completed. Please try again later.",
		Status:  503,
		Cause:   errors.New("postgres connection refused"),
	}
	got := AsClientCanceled(err, context.Canceled)
	if got.Code != CodeRequestCanceled || got.Status != StatusClientClosedRequest {
		t.Fatalf("unexpected rewritten error: %+v", got)
	}
}

func TestAsClientCanceledLeavesClientErrorsAlone(t *testing.T) {
	err := InvalidArgument("sort", "sort must be created_at, model, reasoning, or stream.")
	if got := AsClientCanceled(err, context.Canceled); got != err {
		t.Fatalf("client errors must stay unchanged, got %+v", got)
	}
}

func TestInvalidArgumentIsPublicClientError(t *testing.T) {
	err := InvalidArgument("sort", "sort must be created_at, model, reasoning, or stream.")
	if err.Code != CodeInvalidArgument || err.Status != 400 || err.Param != "sort" {
		t.Fatalf("unexpected public error: %+v", err)
	}
}
