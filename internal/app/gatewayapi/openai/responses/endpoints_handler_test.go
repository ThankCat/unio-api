package responses

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResponsesCompactHandler_OK(t *testing.T) {
	svc := &fakeResponsesService{compact: &CompactHistoryResponse{
		Output: []ResponseOutputItem{{
			Type:    "message",
			Role:    "assistant",
			Status:  "completed",
			Content: []ResponseOutputContent{{Type: "output_text", Text: "summary"}},
		}},
	}}
	handler := NewResponsesCompactHandler(svc)

	rec := postJSON(t, handler, `{"model":"m","input":[{"type":"message","role":"user","content":"hi"}],"instructions":"compact"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	var resp CompactHistoryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode compact response: %v", err)
	}
	if len(resp.Output) != 1 || resp.Output[0].Content[0].Text != "summary" {
		t.Fatalf("unexpected compact output: %+v", resp.Output)
	}
	if svc.got.Model != "m" {
		t.Fatalf("compact request not forwarded to service: %+v", svc.got)
	}
}

func TestResponsesCompactHandler_ValidationError(t *testing.T) {
	handler := NewResponsesCompactHandler(&fakeResponsesService{})

	rec := postJSON(t, handler, `{"model":"","input":"hi"}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	errType, code, param := decodeErrorBody(t, rec)
	if errType != "invalid_request_error" || code != "invalid_request" || param != "model" {
		t.Fatalf("unexpected validation error: type=%q code=%q param=%q", errType, code, param)
	}
}

func TestResponsesInputTokensHandler_OK(t *testing.T) {
	svc := &fakeResponsesService{inputCount: &InputTokenCountResponse{InputTokens: 42, Object: "response.input_tokens"}}
	handler := NewResponsesInputTokensHandler(svc)

	rec := postJSON(t, handler, `{"model":"m","input":"hi"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	var resp InputTokenCountResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode input_tokens response: %v", err)
	}
	if resp.InputTokens != 42 || resp.Object != "response.input_tokens" {
		t.Fatalf("unexpected input_tokens response: %+v", resp)
	}
}

func TestResponsesHandler_BackgroundRejected(t *testing.T) {
	handler := NewResponsesHandler(&fakeResponsesService{})

	rec := postJSON(t, handler, `{"model":"m","input":"hi","background":true}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	errType, code, param := decodeErrorBody(t, rec)
	if errType != "invalid_request_error" || code != "unsupported_background" || param != "background" {
		t.Fatalf("unexpected background rejection: type=%q code=%q param=%q", errType, code, param)
	}
}

func TestResponsesStatelessUnsupportedHandler(t *testing.T) {
	handler := NewResponsesStatelessUnsupportedHandler()

	req := httptest.NewRequest(http.MethodGet, "/v1/responses/resp_123", strings.NewReader(""))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("expected 501, got %d body=%q", rec.Code, rec.Body.String())
	}
	_, code, _ := decodeErrorBody(t, rec)
	if code != "unsupported_endpoint_stateless" {
		t.Fatalf("unexpected stateless code: %q", code)
	}
}
