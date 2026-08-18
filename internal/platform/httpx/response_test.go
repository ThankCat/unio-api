package httpx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWriteConsoleErrorUsesConsoleEnvelope(t *testing.T) {
	recorder := httptest.NewRecorder()
	param := "email"
	if err := WriteConsoleError(
		recorder,
		http.StatusUnprocessableEntity,
		"auth_invalid_email",
		"The email address is invalid.",
		&param,
	); err != nil {
		t.Fatal(err)
	}
	var response ConsoleErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Error.Type != "request_error" || response.Error.Message != "The email address is invalid." {
		t.Fatalf("unexpected Console error response: %+v", response.Error)
	}
}
