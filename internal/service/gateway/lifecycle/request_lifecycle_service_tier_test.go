package lifecycle

import (
	"context"
	"testing"

	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/core/channel"
	"github.com/ThankCat/unio-gateway/internal/core/requestlog"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/core/servicetier"
)

type captureCreateAttemptLog struct {
	captureAttemptFailedLog
	params requestlog.CreateAttemptParams
}

func (s *captureCreateAttemptLog) CreateAttempt(
	_ context.Context,
	params requestlog.CreateAttemptParams,
) (requestlog.AttemptRecord, error) {
	s.params = params
	return requestlog.AttemptRecord{
		ID:                   1,
		RequestedServiceTier: params.RequestedServiceTier,
		ForwardedServiceTier: params.ForwardedServiceTier,
	}, nil
}

func TestCreateAttemptCarriesRequestedAndForwardedServiceTier(t *testing.T) {
	tests := []struct {
		name         string
		supportsFast bool
		wantForward  servicetier.Tier
	}{
		{name: "unsupported channel", wantForward: servicetier.TierStandard},
		{name: "supported channel", supportsFast: true, wantForward: servicetier.TierFast},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log := &captureCreateAttemptLog{}
			lifecycle := &RequestLifecycle{requestLog: log, logger: zap.NewNop()}
			request := requestlog.RequestRecord{ID: 42, RequestedServiceTier: servicetier.TierFast}
			candidate := routing.ChatRouteCandidate{
				ModelDBID:              11,
				ProviderID:             12,
				OriginRevision:         13,
				ProviderStatusRevision: 14,
				ChannelConfigRevision:  15,
				AdapterKey:             "openai",
				Protocol:               routing.ProtocolOpenAI,
				SupportsOpenAIFast:     tt.supportsFast,
				UpstreamModel:          "gpt-test",
				Channel: channel.Runtime{
					ID:           16,
					Name:         "Fast test",
					ProviderSlug: "openai",
				},
			}

			attempt, err := lifecycle.CreateAttemptForEndpoint(
				context.Background(), request, 0, 0, candidate,
				requestlog.UpstreamEndpointResponses, "permit-1",
			)
			if err != nil {
				t.Fatalf("create attempt: %v", err)
			}
			if log.params.RequestedServiceTier != servicetier.TierFast || attempt.RequestedServiceTier != servicetier.TierFast {
				t.Fatalf("requested service tier params/attempt = %q/%q, want fast", log.params.RequestedServiceTier, attempt.RequestedServiceTier)
			}
			if log.params.ForwardedServiceTier != tt.wantForward || attempt.ForwardedServiceTier != tt.wantForward {
				t.Fatalf("forwarded service tier params/attempt = %q/%q, want %q", log.params.ForwardedServiceTier, attempt.ForwardedServiceTier, tt.wantForward)
			}
		})
	}
}
