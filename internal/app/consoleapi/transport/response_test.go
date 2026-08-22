package transport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
	consoleservice "github.com/ThankCat/unio-gateway/internal/service/console"
)

func testRequest() *http.Request {
	return httptest.NewRequest(http.MethodGet, "/v1/auth/me", nil)
}

func TestErrorWriterSeparatesPublicMessageFromInternalCause(t *testing.T) {
	core, logs := observer.New(zap.ErrorLevel)
	writer := NewErrorWriter(zap.New(core))
	recorder := httptest.NewRecorder()
	cause := errors.New("postgres connection refused")

	writer.Write(recorder, testRequest(), &consoleservice.Error{
		Code:    consoleservice.CodeRequestUnavailable,
		Message: "The request could not be completed. Please try again later.",
		Status:  http.StatusServiceUnavailable,
		Cause:   cause,
	})

	var response httpx.ConsoleErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Error.Message != "The request could not be completed. Please try again later." {
		t.Fatalf("unexpected public message %q", response.Error.Message)
	}
	if strings.Contains(strings.ToLower(response.Error.Message), "postgres") {
		t.Fatalf("public response exposes the internal cause: %q", response.Error.Message)
	}
	entries := logs.All()
	if len(entries) != 1 || !strings.Contains(entries[0].ContextMap()["error"].(string), cause.Error()) {
		t.Fatalf("expected internal cause in server log, got %+v", entries)
	}
}

func TestErrorWriterDoesNotLogClientCancelAsServerFailure(t *testing.T) {
	core, logs := observer.New(zap.ErrorLevel)
	writer := NewErrorWriter(zap.New(core))
	recorder := httptest.NewRecorder()

	writer.Write(recorder, testRequest(), consoleservice.RequestUnavailable("read user usd wallet", context.Canceled))

	if recorder.Code != consoleservice.StatusClientClosedRequest {
		t.Fatalf("status %d, want %d", recorder.Code, consoleservice.StatusClientClosedRequest)
	}
	if logs.Len() != 0 {
		t.Fatalf("client cancel must not log ERROR, got %+v", logs.All())
	}
}

func TestErrorWriterRemapsCanceledRequestContextForAnyUnavailableError(t *testing.T) {
	core, logs := observer.New(zap.ErrorLevel)
	writer := NewErrorWriter(zap.New(core))
	recorder := httptest.NewRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/v1/requests/summary", nil).WithContext(ctx)

	writer.Write(recorder, req, &consoleservice.Error{
		Code:    consoleservice.CodeRequestUnavailable,
		Message: "The request could not be completed. Please try again later.",
		Status:  http.StatusServiceUnavailable,
		Cause:   errors.New("postgres connection refused"),
	})

	if recorder.Code != consoleservice.StatusClientClosedRequest {
		t.Fatalf("status %d, want %d", recorder.Code, consoleservice.StatusClientClosedRequest)
	}
	if logs.Len() != 0 {
		t.Fatalf("client cancel must not log ERROR, got %+v", logs.All())
	}
}

func TestErrorWriterKeepsClientErrorsWhenRequestIsCanceled(t *testing.T) {
	writer := NewErrorWriter(zap.NewNop())
	recorder := httptest.NewRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/v1/auth/me", nil).WithContext(ctx)

	writer.Write(recorder, req, &consoleservice.Error{
		Code:    "auth_session_invalid",
		Message: "The current session is invalid.",
		Status:  http.StatusUnauthorized,
	})

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
}

func TestErrorWriterIncludesRemainingVerificationAttempts(t *testing.T) {
	writer := NewErrorWriter(zap.NewNop())
	recorder := httptest.NewRecorder()
	remaining := 4

	writer.Write(recorder, testRequest(), &consoleservice.Error{
		Code:              "auth_verification_code_invalid",
		Message:           "The verification code is incorrect.",
		Param:             "code",
		Status:            http.StatusUnprocessableEntity,
		RemainingAttempts: &remaining,
	})

	var response httpx.ConsoleErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Error.RemainingAttempts == nil || *response.Error.RemainingAttempts != remaining {
		t.Fatalf("unexpected remaining attempts: %+v", response.Error)
	}
}
