package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/core/requestlog"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

func TestRoutingFailureCodeClassifiesRoutingErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "model not found",
			err:  routing.ErrModelNotFound,
			want: "model_not_found",
		},
		{
			name: "model not available",
			err:  routing.ErrModelNotAvailable,
			want: "model_not_available",
		},
		{
			name: "no available channel",
			err:  routing.ErrNoAvailableChannel,
			want: "no_available_channel",
		},
		{
			name: "route not configured",
			err:  routing.ErrRouteNotConfigured,
			want: "routing_route_not_configured",
		},
		{
			name: "unknown routing error",
			err:  errors.New("routing database failed"),
			want: "routing_error",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RoutingFailureCode(tc.err); got != tc.want {
				t.Fatalf("expected code %q, got %q", tc.want, got)
			}
		})
	}
}

func TestInternalErrorDetailIsTruncated(t *testing.T) {
	rawErr := errors.New(strings.Repeat("x", MaxRequestLogInternalErrorDetailBytes+100))

	detail := InternalErrorDetail(rawErr)

	if len(detail) <= MaxRequestLogInternalErrorDetailBytes {
		t.Fatalf("expected truncated detail marker to extend stored detail, got length %d", len(detail))
	}
	if !strings.HasSuffix(detail, "...[truncated]") {
		t.Fatalf("expected truncated marker, got %q", detail[len(detail)-20:])
	}
}

// TestInternalErrorDetailIncludesCauseChain 锁定:流式断连这类错误必须把底层根因
// (如 context canceled)拼进 detail,而非只留 failure 顶层归类文案,否则排查无从下手。
func TestInternalErrorDetailIncludesCauseChain(t *testing.T) {
	wrapped := failure.Wrap(
		failure.CodeAdapterReadStreamFailed,
		context.Canceled,
		failure.WithMessage("openai responses adapter read stream event"),
	)

	detail := InternalErrorDetail(wrapped)

	if !strings.Contains(detail, "openai responses adapter read stream event") {
		t.Fatalf("detail missing top message: %q", detail)
	}
	if !strings.Contains(detail, context.Canceled.Error()) {
		t.Fatalf("detail missing root cause %q: %q", context.Canceled.Error(), detail)
	}
}

func TestFailureCodeOrFallbackReturnsFallbackWhenNoFailureCode(t *testing.T) {
	if got := FailureCodeOrFallback(errors.New("plain error"), "fallback_code"); got != "fallback_code" {
		t.Fatalf("expected fallback_code, got %q", got)
	}
}

func TestRequestLogCancelFactsIgnoresAdapterWrappedFailureCode(t *testing.T) {
	err := adapter.NewUpstreamError(
		adapter.UpstreamErrorCanceled,
		adapter.UpstreamMetadata{},
		failure.Wrap(
			failure.CodeAdapterSendRequestFailed,
			context.Canceled,
			failure.WithMessage("openai adapter send stream chat completion request"),
		),
	)

	l := &RequestLifecycle{}
	code, msg, detail := l.requestLogCancelFacts(err)

	if code != "client_canceled" {
		t.Fatalf("expected client_canceled, got %q", code)
	}
	if msg != "Request was canceled by the client." {
		t.Fatalf("expected client cancel message, got %q", msg)
	}
	if FailureCodeOrFallback(err, "client_canceled") != string(failure.CodeAdapterSendRequestFailed) {
		t.Fatal("precondition: wrapped cancel must carry adapter failure code")
	}
	if !strings.Contains(detail, context.Canceled.Error()) {
		t.Fatalf("detail missing root cause %q: %q", context.Canceled.Error(), detail)
	}
}

func TestMarkAttemptFailedPersistsUpstreamMetadata(t *testing.T) {
	store := &captureAttemptFailedLog{}
	l := &RequestLifecycle{requestLog: store}
	err := adapter.NewUpstreamError(
		adapter.UpstreamErrorServer,
		adapter.UpstreamMetadata{StatusCode: 503, RequestID: "req-upstream-503"},
		failure.New(
			failure.CodeAdapterUpstreamStatus,
			failure.WithMessage("openai responses adapter upstream stream status 503"),
		),
	)

	l.MarkAttemptFailed(context.Background(), requestlog.AttemptRecord{ID: 42}, "stream_adapter_error", err)

	if store.params.ID != 42 {
		t.Fatalf("attempt id = %d, want 42", store.params.ID)
	}
	if store.params.UpstreamStatusCode == nil || *store.params.UpstreamStatusCode != 503 {
		t.Fatalf("upstream status = %v, want 503", store.params.UpstreamStatusCode)
	}
	if store.params.UpstreamRequestID == nil || *store.params.UpstreamRequestID != "req-upstream-503" {
		t.Fatalf("upstream request id = %v, want req-upstream-503", store.params.UpstreamRequestID)
	}
	if store.params.ErrorCode != string(failure.CodeAdapterUpstreamStatus) {
		t.Fatalf("error code = %q, want %q", store.params.ErrorCode, failure.CodeAdapterUpstreamStatus)
	}
}

func TestBaseSafeRequestLogErrorMessageFallsBackByCategory(t *testing.T) {
	cases := []struct {
		code string
		want string
	}{
		{code: "client_canceled", want: "Request was canceled by the client."},
		{code: "adapter_some_error", want: "Upstream provider request failed."},
		{code: "routing_pick_failed", want: "Request routing failed."},
		{code: "ledger_oops", want: "Request billing failed."},
		{code: "billing_oops", want: "Request billing failed."},
		{code: "gateway_oops", want: "Gateway request failed."},
		{code: "totally_unknown", want: "Request failed."},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			if got := BaseSafeRequestLogErrorMessage(tc.code); got != tc.want {
				t.Fatalf("expected %q, got %q", tc.want, got)
			}
		})
	}
}

type captureAttemptFailedLog struct {
	params requestlog.MarkAttemptFailedParams
}

func (s *captureAttemptFailedLog) CreateRequest(context.Context, requestlog.CreateRequestParams) (requestlog.RequestRecord, error) {
	return requestlog.RequestRecord{}, fmt.Errorf("unexpected CreateRequest")
}

func (s *captureAttemptFailedLog) MarkRequestRunning(context.Context, int64) (requestlog.RequestRecord, error) {
	return requestlog.RequestRecord{}, fmt.Errorf("unexpected MarkRequestRunning")
}

func (s *captureAttemptFailedLog) MarkRequestResponseStarted(context.Context, requestlog.MarkResponseStartedParams) (requestlog.RequestRecord, error) {
	return requestlog.RequestRecord{}, fmt.Errorf("unexpected MarkRequestResponseStarted")
}

func (s *captureAttemptFailedLog) MarkRequestSucceeded(context.Context, requestlog.MarkRequestSucceededParams) (requestlog.RequestRecord, error) {
	return requestlog.RequestRecord{}, fmt.Errorf("unexpected MarkRequestSucceeded")
}

func (s *captureAttemptFailedLog) MarkSettledRequestFailed(context.Context, requestlog.MarkSettledRequestFailedParams) (requestlog.RequestRecord, error) {
	return requestlog.RequestRecord{}, fmt.Errorf("unexpected MarkSettledRequestFailed")
}

func (s *captureAttemptFailedLog) MarkSettledRequestCanceled(context.Context, requestlog.MarkSettledRequestCanceledParams) (requestlog.RequestRecord, error) {
	return requestlog.RequestRecord{}, fmt.Errorf("unexpected MarkSettledRequestCanceled")
}

func (s *captureAttemptFailedLog) MarkRequestFailed(context.Context, requestlog.MarkRequestFailedParams) (requestlog.RequestRecord, error) {
	return requestlog.RequestRecord{}, fmt.Errorf("unexpected MarkRequestFailed")
}

func (s *captureAttemptFailedLog) MarkRequestCanceled(context.Context, requestlog.MarkRequestCanceledParams) (requestlog.RequestRecord, error) {
	return requestlog.RequestRecord{}, fmt.Errorf("unexpected MarkRequestCanceled")
}

func (s *captureAttemptFailedLog) CreateAttempt(context.Context, requestlog.CreateAttemptParams) (requestlog.AttemptRecord, error) {
	return requestlog.AttemptRecord{}, fmt.Errorf("unexpected CreateAttempt")
}

func (s *captureAttemptFailedLog) MarkAttemptResponseStarted(context.Context, requestlog.MarkAttemptResponseStartedParams) (requestlog.AttemptRecord, error) {
	return requestlog.AttemptRecord{}, fmt.Errorf("unexpected MarkAttemptResponseStarted")
}

func (s *captureAttemptFailedLog) MarkAttemptSucceeded(context.Context, requestlog.MarkAttemptSucceededParams) (requestlog.AttemptRecord, error) {
	return requestlog.AttemptRecord{}, fmt.Errorf("unexpected MarkAttemptSucceeded")
}

func (s *captureAttemptFailedLog) MarkSettledAttemptFailed(context.Context, requestlog.MarkSettledAttemptFailedParams) (requestlog.AttemptRecord, error) {
	return requestlog.AttemptRecord{}, fmt.Errorf("unexpected MarkSettledAttemptFailed")
}

func (s *captureAttemptFailedLog) MarkSettledAttemptCanceled(context.Context, requestlog.MarkSettledAttemptCanceledParams) (requestlog.AttemptRecord, error) {
	return requestlog.AttemptRecord{}, fmt.Errorf("unexpected MarkSettledAttemptCanceled")
}

func (s *captureAttemptFailedLog) MarkAttemptFailed(_ context.Context, params requestlog.MarkAttemptFailedParams) (requestlog.AttemptRecord, error) {
	s.params = params
	completedAt := time.Now()
	return requestlog.AttemptRecord{ID: params.ID, CompletedAt: &completedAt}, nil
}

func (s *captureAttemptFailedLog) MarkAttemptCanceled(context.Context, requestlog.MarkAttemptCanceledParams) (requestlog.AttemptRecord, error) {
	return requestlog.AttemptRecord{}, fmt.Errorf("unexpected MarkAttemptCanceled")
}
