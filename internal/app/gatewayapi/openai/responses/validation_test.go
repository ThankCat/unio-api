package responses

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ThankCat/unio-api/internal/platform/httpx"
)

func mustUnmarshal(t *testing.T, raw string) ResponsesRequest {
	t.Helper()
	var req ResponsesRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	return req
}

func TestValidateResponsesRequestValid(t *testing.T) {
	req := mustUnmarshal(t, `{
		"model": "deepseek-v4-flash",
		"input": [
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "hi"}]},
			{"type": "function_call", "call_id": "c1", "name": "exec_command", "arguments": "{}"},
			{"type": "function_call_output", "call_id": "c1", "output": "ok"}
		],
		"tools": [
			{"type": "function", "name": "exec_command"},
			{"type": "namespace", "name": "mcp__x__", "tools": [{"type": "function", "name": "js"}]}
		],
		"temperature": 1.0
	}`)

	if err := validateResponsesRequest(req); err != nil {
		t.Fatalf("expected valid request, got %+v", err)
	}
}

func TestValidateResponsesRequestErrors(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		wantParam string
	}{
		{"missing model", `{"input": "hi"}`, "model"},
		{"missing input", `{"model": "m"}`, "input"},
		{"empty string input", `{"model": "m", "input": "  "}`, "input"},
		{"object input", `{"model": "m", "input": {"foo": 1}}`, "input"},
		{"message missing role", `{"model": "m", "input": [{"type": "message", "content": "hi"}]}`, "input.0.role"},
		{"message bad role", `{"model": "m", "input": [{"type": "message", "role": "tool", "content": "hi"}]}`, "input.0.role"},
		{"message empty content", `{"model": "m", "input": [{"type": "message", "role": "user", "content": ""}]}`, "input.0.content"},
		{"bad content part type", `{"model": "m", "input": [{"type": "message", "role": "user", "content": [{"type": "weird"}]}]}`, "input.0.content.0.type"},
		{"function_call missing call_id", `{"model": "m", "input": [{"type": "function_call", "name": "f"}]}`, "input.0.call_id"},
		{"function_call missing name", `{"model": "m", "input": [{"type": "function_call", "call_id": "c1"}]}`, "input.0.name"},
		{"function_call_output missing output", `{"model": "m", "input": [{"type": "function_call_output", "call_id": "c1"}]}`, "input.0.output"},
		{"item_reference missing id", `{"model": "m", "input": [{"type": "item_reference"}]}`, "input.0.id"},
		{"tool missing type", `{"model": "m", "input": "hi", "tools": [{"name": "f"}]}`, "tools.0.type"},
		{"function tool missing name", `{"model": "m", "input": "hi", "tools": [{"type": "function"}]}`, "tools.0.name"},
		{"namespace nested missing name", `{"model": "m", "input": "hi", "tools": [{"type": "namespace", "name": "n", "tools": [{"type": "function"}]}]}`, "tools.0.tools.0.name"},
		{"temperature out of range", `{"model": "m", "input": "hi", "temperature": 3}`, "temperature"},
		{"top_p out of range", `{"model": "m", "input": "hi", "top_p": 2}`, "top_p"},
		{"max_output_tokens zero", `{"model": "m", "input": "hi", "max_output_tokens": 0}`, "max_output_tokens"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := mustUnmarshal(t, tc.raw)
			err := validateResponsesRequest(req)
			if err == nil {
				t.Fatalf("expected validation error for %s", tc.name)
			}
			if err.param != tc.wantParam {
				t.Fatalf("expected param %q, got %q (message=%q)", tc.wantParam, err.param, err.message)
			}
		})
	}
}

// TestValidateUnknownItemTypePassesThrough 验证未知 item 类型放行（DEC-012），交给 translation。
func TestValidateUnknownItemTypePassesThrough(t *testing.T) {
	req := mustUnmarshal(t, `{"model": "m", "input": [{"type": "future_item_type", "id": "x"}]}`)
	if err := validateResponsesRequest(req); err != nil {
		t.Fatalf("expected unknown item type to pass, got %+v", err)
	}
}

func TestWriteResponsesValidationError(t *testing.T) {
	rec := httptest.NewRecorder()
	writeResponsesValidationError(rec, &responsesValidationError{param: "input", message: "input is required"})

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rec.Code)
	}

	var body httpx.ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if body.Error.Type != errorTypeInvalidRequest {
		t.Fatalf("error type = %q", body.Error.Type)
	}
	if body.Error.Param == nil || *body.Error.Param != "input" {
		t.Fatalf("error param = %#v", body.Error.Param)
	}
	if body.Error.Message != "input is required" {
		t.Fatalf("error message = %q", body.Error.Message)
	}
}

func TestWriteResponsesDecodeError(t *testing.T) {
	rec := httptest.NewRecorder()
	writeResponsesDecodeError(rec, httpx.ErrEmptyJSONBody)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rec.Code)
	}

	var body httpx.ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if body.Error.Type != errorTypeInvalidRequest || body.Error.Message != "request body is required" {
		t.Fatalf("error body = %#v", body.Error)
	}
}
