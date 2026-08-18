package transport

import (
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

func TestErrorWriterSeparatesPublicMessageFromInternalCause(t *testing.T) {
	core, logs := observer.New(zap.ErrorLevel)
	writer := NewErrorWriter(zap.New(core))
	recorder := httptest.NewRecorder()
	cause := errors.New("postgres connection refused")

	writer.Write(recorder, &consoleservice.Error{
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
