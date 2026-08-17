package responses

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/lifecycle"
)

type fakeResponsesService struct {
	resp                *ResponsesResponse
	compact             *CompactHistoryResponse
	inputCount          *InputTokenCountResponse
	err                 error
	got                 ResponsesRequest
	deliveryCompleted   int
	deliveryInterrupted int
}

func (s *fakeResponsesService) CreateResponse(_ context.Context, req ResponsesRequest) (*lifecycle.NonStreamResult[*ResponsesResponse], error) {
	s.got = req
	if s.err != nil {
		return nil, s.err
	}
	return lifecycle.NewNonStreamResult(s.resp, s.deliveryFinalizer()), nil
}

func (s *fakeResponsesService) StreamResponse(_ context.Context, req ResponsesRequest, emit func(ResponsesStreamEvent) error) error {
	s.got = req
	if s.err != nil {
		return s.err
	}
	return emit(ResponsesStreamEvent{Type: EventResponseCreated})
}

func (s *fakeResponsesService) CompactHistory(_ context.Context, req ResponsesRequest) (*lifecycle.NonStreamResult[*CompactHistoryResponse], error) {
	s.got = req
	if s.err != nil {
		return nil, s.err
	}
	return lifecycle.NewNonStreamResult(s.compact, s.deliveryFinalizer()), nil
}

func (s *fakeResponsesService) deliveryFinalizer() lifecycle.DeliveryFinalizer {
	return lifecycle.NewDeliveryFinalizer(
		func() { s.deliveryCompleted++ },
		func() { s.deliveryInterrupted++ },
	)
}

func (s *fakeResponsesService) CountInputTokens(_ context.Context, req ResponsesRequest) (*InputTokenCountResponse, error) {
	s.got = req
	return s.inputCount, s.err
}

func postJSON(t *testing.T, handler http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func decodeErrorBody(t *testing.T, rec *httptest.ResponseRecorder) (errType, code, param string) {
	t.Helper()
	var body struct {
		Error struct {
			Type  string  `json:"type"`
			Code  string  `json:"code"`
			Param *string `json:"param"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body %q: %v", rec.Body.String(), err)
	}
	p := ""
	if body.Error.Param != nil {
		p = *body.Error.Param
	}
	return body.Error.Type, body.Error.Code, p
}

func TestResponsesHandler_HappyPath(t *testing.T) {
	svc := &fakeResponsesService{resp: &ResponsesResponse{ID: "resp_1", Object: "response", Model: "m", Status: "completed"}}
	handler := NewResponsesHandler(svc)

	rec := postJSON(t, handler, `{"model":"m","input":"hi"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	if svc.got.Model != "m" || svc.got.Input.Text == nil || *svc.got.Input.Text != "hi" {
		t.Fatalf("service received unexpected request: %+v", svc.got)
	}
	var resp ResponsesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Object != "response" || resp.ID != "resp_1" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestResponsesHandler_StreamSSE(t *testing.T) {
	svc := &fakeResponsesService{}
	handler := NewResponsesHandler(svc)

	rec := postJSON(t, handler, `{"model":"m","input":"hi","stream":true}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("expected SSE content-type, got %q", ct)
	}
	if !svc.got.StreamEnabled() || svc.got.Model != "m" {
		t.Fatalf("stream request not forwarded to service: model=%q stream=%v", svc.got.Model, svc.got.StreamEnabled())
	}
	if body := rec.Body.String(); !strings.Contains(body, EventResponseCreated) {
		t.Fatalf("expected SSE body to carry %q event, got %q", EventResponseCreated, body)
	}
}

func TestResponsesHandler_ValidationError(t *testing.T) {
	handler := NewResponsesHandler(&fakeResponsesService{})

	rec := postJSON(t, handler, `{"model":"","input":"hi"}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	errType, code, param := decodeErrorBody(t, rec)
	if errType != "invalid_request_error" || code != "invalid_request" || param != "model" {
		t.Fatalf("unexpected validation error: type=%q code=%q param=%q", errType, code, param)
	}
}

func TestResponsesHandler_DecodeErrorWrongContentType(t *testing.T) {
	handler := NewResponsesHandler(&fakeResponsesService{})

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"m","input":"hi"}`))
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("expected 415, got %d body=%q", rec.Code, rec.Body.String())
	}
}

func TestResponsesHandler_InsufficientQuota(t *testing.T) {
	svc := &fakeResponsesService{err: failure.New(failure.CodeLedgerInsufficientBalance, failure.WithMessage("insufficient balance"))}
	handler := NewResponsesHandler(svc)

	rec := postJSON(t, handler, `{"model":"m","input":"hi"}`)

	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("expected 402, got %d", rec.Code)
	}
	errType, code, _ := decodeErrorBody(t, rec)
	if errType != "insufficient_quota" || code != "insufficient_quota" {
		t.Fatalf("unexpected quota error: type=%q code=%q", errType, code)
	}
}

func TestResponsesHandler_TemporarilyReservedBalance(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "non-stream", body: `{"model":"m","input":"hi"}`},
		{name: "stream before first event", body: `{"model":"m","input":"hi","stream":true}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &fakeResponsesService{err: failure.New(failure.CodeLedgerBalanceTemporarilyReserved)}
			handler := NewResponsesHandler(svc)

			rec := postJSON(t, handler, tt.body)

			if rec.Code != http.StatusTooManyRequests {
				t.Fatalf("status=%d, want 429 body=%s", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Retry-After"); got != "1" {
				t.Fatalf("Retry-After=%q, want 1", got)
			}
			if strings.Contains(rec.Body.String(), "event:") {
				t.Fatalf("pre-first-event failure must use JSON body, got %q", rec.Body.String())
			}
			errType, code, _ := decodeErrorBody(t, rec)
			if errType != "rate_limit_error" || code != "rate_limit_exceeded" {
				t.Fatalf("error type=%q code=%q", errType, code)
			}
		})
	}
}

// TestUpstreamErrorResponseKeepsSanitizedUpstreamIdentity 冻结：流式首字前失败时内联 response.failed
// 事件会被丢弃（首字前不向客户暴露失败渠道事件），因此上游真实 code/message 必须由最终错误响应带出，
// 否则客户只能看到一句通用文案、SDK 也拿不到上游原本的 error.code。
//
// HTTP 状态与 error type 仍由分类决定：客户端据此判断能否重试，不能被上游 code 影响。
func TestUpstreamErrorResponseKeepsSanitizedUpstreamIdentity(t *testing.T) {
	err := adapter.NewUpstreamError(
		adapter.UpstreamErrorBadRequest,
		adapter.UpstreamMetadata{
			StatusCode:   http.StatusBadRequest,
			ErrorCode:    "context_length_exceeded",
			ErrorMessage: "This model's maximum context length is 128000 tokens.",
		},
		failure.New(failure.CodeAdapterUpstreamStatus),
	)

	got := mapResponsesServiceError(ResponsesRequest{Model: "gpt-5"}, err, "fallback_code", "fallback message")

	if got.status != http.StatusBadRequest || got.errorType != "invalid_request_error" {
		t.Fatalf("status/type must stay category-driven: status=%d type=%q", got.status, got.errorType)
	}
	if got.code != "context_length_exceeded" {
		t.Fatalf("code = %q, want the upstream code so SDKs can branch on it", got.code)
	}
	if got.message != "This model's maximum context length is 128000 tokens." {
		t.Fatalf("message = %q, want the upstream reason", got.message)
	}
}

// TestUpstreamErrorResponseFallsBackWhenUpstreamIdentityAbsent 保证没有上游标识时仍是通用脱敏文案。
func TestUpstreamErrorResponseFallsBackWhenUpstreamIdentityAbsent(t *testing.T) {
	err := adapter.NewUpstreamError(
		adapter.UpstreamErrorBadRequest,
		adapter.UpstreamMetadata{StatusCode: http.StatusBadRequest},
		failure.New(failure.CodeAdapterUpstreamStatus),
	)

	got := mapResponsesServiceError(ResponsesRequest{Model: "gpt-5"}, err, "fallback_code", "fallback message")

	if got.code != "invalid_request" || got.message != "The upstream provider rejected the request." {
		t.Fatalf("expected the generic sanitized fallback, got code=%q message=%q", got.code, got.message)
	}
}
