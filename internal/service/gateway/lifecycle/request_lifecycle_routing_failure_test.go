package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/ThankCat/unio-gateway/internal/core/requestlog"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"go.uber.org/zap"
)

func TestShouldPersistRoutingFailure(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		code failure.Code
		want bool
	}{
		{"model not found", failure.CodeRoutingModelNotFound, false},
		{"model not available", failure.CodeRoutingModelNotAvailable, false},
		{"route not configured", failure.CodeRoutingRouteNotConfigured, false},
		{"protocol invalid", failure.CodeRoutingProtocolInvalid, false},
		{"store failed", failure.CodeRoutingStoreFailed, false},
		{"no available channel", failure.CodeRoutingNoAvailableChannel, true},
		{"unknown routing failure", failure.Code("routing_something_else"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := failure.New(tc.code, failure.WithMessage(string(tc.code)))
			if got := shouldPersistRoutingFailure(err); got != tc.want {
				t.Fatalf("shouldPersistRoutingFailure(%s)=%v, want %v", tc.code, got, tc.want)
			}
		})
	}
}

func TestRecordRoutingFailureSkipsPreSelectionErrors(t *testing.T) {
	t.Parallel()
	store := &fakeRoutingTraceStore{}
	recorder := NewRoutingTraceRecorder(store, zap.NewNop())
	lifecycle := &RequestLifecycle{}
	lifecycle.SetRoutingTraceRecorder(recorder)

	routeID := int64(9)
	request := requestlog.RequestRecord{
		ID: 1, RequestID: "req-unknown-model", RequestedModelID: "openai/does-not-exist",
		IngressProtocol: requestlog.ProtocolOpenAI, Endpoint: requestlog.EndpointChatCompletions,
	}

	lifecycle.RecordRoutingFailure(context.Background(), request, &routeID, failure.Wrap(
		failure.CodeRoutingModelNotFound,
		errors.New("model not found"),
		failure.WithMessage("model not found"),
	))
	if len(store.writes) != 0 {
		t.Fatalf("model_not_found must not write routing decision traces, got %d", len(store.writes))
	}

	lifecycle.RecordRoutingFailure(context.Background(), request, &routeID, failure.New(
		failure.CodeRoutingNoAvailableChannel,
		failure.WithMessage("no available channel"),
	))
	if len(store.writes) != 1 {
		t.Fatalf("no_available_channel must write a routing decision trace, got %d", len(store.writes))
	}
}
