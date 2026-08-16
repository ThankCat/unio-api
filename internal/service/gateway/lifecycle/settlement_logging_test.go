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
	"github.com/ThankCat/unio-gateway/internal/core/servicetier"
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

func TestLogSettlementResultRecordsServiceTierFacts(t *testing.T) {
	core, observed := observer.New(zapcore.DebugLevel)
	metrics := &routingMetricsSpy{}
	lifecycle := &RequestLifecycle{logger: zap.New(core), metrics: metrics}
	lifecycle.LogSettlementResult(
		context.Background(),
		requestlog.RequestRecord{
			ID:                   10,
			RequestID:            "req_fast_standard",
			RequestedServiceTier: servicetier.TierFast,
		},
		requestlog.AttemptRecord{ID: 20},
		routing.ChatRouteCandidate{},
		ChatAuthorization{},
		adapter.ResponseFacts{ServiceTier: servicetier.Response{
			Actual:      servicetier.TierStandard,
			Settled:     servicetier.TierStandard,
			UpstreamRaw: "default",
			Resolution:  servicetier.ResolutionUpstreamResponse,
		}},
		false,
		nil,
	)

	entries := observed.FilterMessage("billing settlement completed").All()
	if len(entries) != 1 {
		t.Fatalf("settlement completion logs = %d, want 1", len(entries))
	}
	fields := entries[0].ContextMap()
	for key, want := range map[string]any{
		"requested_service_tier":  "fast",
		"actual_service_tier":     "standard",
		"settled_service_tier":    "standard",
		"service_tier_resolution": "upstream_response",
		"upstream_service_tier":   "default",
		"service_tier_downgraded": true,
	} {
		if got := fields[key]; got != want {
			t.Errorf("%s = %#v, want %#v", key, got, want)
		}
	}
	if len(metrics.serviceTiers) != 1 || metrics.serviceTiers[0] != "fast/standard/standard/upstream_response" {
		t.Fatalf("service tier metrics = %#v", metrics.serviceTiers)
	}
}

func TestResolveServiceTierObservationUsesPinnedFastPriceFallback(t *testing.T) {
	request := requestlog.RequestRecord{RequestedServiceTier: servicetier.TierFast}
	facts := adapter.ResponseFacts{ServiceTier: servicetier.Response{
		Actual:      servicetier.TierFast,
		Settled:     servicetier.TierFast,
		UpstreamRaw: "priority",
		Resolution:  servicetier.ResolutionUpstreamResponse,
	}}

	missing := resolveServiceTierObservation(request, routing.ChatRouteCandidate{}, facts)
	if missing.settled != servicetier.TierStandard || missing.resolution != servicetier.ResolutionFastPriceMissing {
		t.Fatalf("missing Fast prices observation = %+v", missing)
	}
	if missing.downgraded {
		t.Fatal("Fast billing fallback must not be reported as an upstream downgrade")
	}

	configured := resolveServiceTierObservation(request, routing.ChatRouteCandidate{
		FastModelPriceServiceTierID: 11,
		CostBaseModelPriceID:        12,
		ChannelCostMultiplierID:     13,
	}, facts)
	if configured.settled != servicetier.TierFast || configured.resolution != servicetier.ResolutionUpstreamResponse {
		t.Fatalf("configured Fast prices observation = %+v", configured)
	}
}
