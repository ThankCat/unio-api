package anthropic

import (
	"encoding/json"
	"testing"
)

func TestNewErrorResponseShape(t *testing.T) {
	raw, err := json.Marshal(NewErrorResponse("invalid_request_error", "max_tokens is required", "req-123"))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got map[string]json.RawMessage
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if string(got["type"]) != `"error"` {
		t.Fatalf("type = %s, want \"error\"", got["type"])
	}
	if string(got["request_id"]) != `"req-123"` {
		t.Fatalf("request_id = %s", got["request_id"])
	}

	var body map[string]string
	if err := json.Unmarshal(got["error"], &body); err != nil {
		t.Fatalf("unmarshal error body: %v", err)
	}
	if body["type"] != "invalid_request_error" || body["message"] != "max_tokens is required" {
		t.Fatalf("error body = %#v", body)
	}
}

func TestNewErrorResponseOmitsEmptyRequestID(t *testing.T) {
	raw, err := json.Marshal(NewErrorResponse("api_error", "upstream failed", ""))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got map[string]json.RawMessage
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := got["request_id"]; ok {
		t.Fatal("expected empty request_id to be omitted")
	}
}
