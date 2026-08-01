package lifecycle

import (
	"context"
	"errors"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/core/requestlog"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
)

func TestLogSettlementResultDistinguishesRecoverySchedulingOutcome(t *testing.T) {
	tests := []struct {
		name            string
		err             error
		wantScheduled   int
		wantScheduleErr int
	}{
		{
			name:          "recovery job persisted",
			err:           ChatSettlementRecoveryScheduledError(10, errors.New("settlement commit failed")),
			wantScheduled: 1,
		},
		{
			name:            "recovery job not persisted",
			err:             errors.New("insert recovery job failed"),
			wantScheduleErr: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			core, observed := observer.New(zapcore.DebugLevel)
			lifecycle := &RequestLifecycle{logger: zap.New(core)}
			lifecycle.LogSettlementResult(
				context.Background(),
				requestlog.RequestRecord{ID: 10, RequestID: "req_test"},
				requestlog.AttemptRecord{ID: 20},
				routing.ChatRouteCandidate{},
				ChatAuthorization{},
				adapter.ResponseFacts{},
				false,
				tc.err,
			)

			if got := observed.FilterMessage("settlement recovery scheduled").Len(); got != tc.wantScheduled {
				t.Fatalf("scheduled logs = %d, want %d", got, tc.wantScheduled)
			}
			if got := observed.FilterMessage("settlement recovery scheduling failed").Len(); got != tc.wantScheduleErr {
				t.Fatalf("scheduling failed logs = %d, want %d", got, tc.wantScheduleErr)
			}
		})
	}
}
