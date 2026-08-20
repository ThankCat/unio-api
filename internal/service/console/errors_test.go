package console

import (
	"errors"
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

func TestInvalidArgumentIsPublicClientError(t *testing.T) {
	err := InvalidArgument("sort", "sort must be created_at, model, reasoning, or stream.")
	if err.Code != CodeInvalidArgument || err.Status != 400 || err.Param != "sort" {
		t.Fatalf("unexpected public error: %+v", err)
	}
}
