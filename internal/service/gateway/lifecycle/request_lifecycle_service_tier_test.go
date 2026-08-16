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
	return requestlog.AttemptRecord{ID: 1, RequestedServiceTier: params.RequestedServiceTier}, nil
}

func TestCreateAttemptCarriesRequestedServiceTier(t *testing.T) {
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
		UpstreamModel:          "gpt-test",
		Channel: channel.Runtime{
			ID:           16,
			Name:         "Fast test",
			ProviderSlug: "openai",
		},
	}

	attempt, err := lifecycle.CreateAttemptForEndpoint(
		context.Background(),
		request,
		0,
		0,
		candidate,
		requestlog.UpstreamEndpointResponses,
		"permit-1",
	)
	if err != nil {
		t.Fatalf("create attempt: %v", err)
	}
	if log.params.RequestedServiceTier != servicetier.TierFast {
		t.Fatalf("requested service tier = %q, want fast", log.params.RequestedServiceTier)
	}
	if attempt.RequestedServiceTier != servicetier.TierFast {
		t.Fatalf("attempt requested service tier = %q, want fast", attempt.RequestedServiceTier)
	}
}
