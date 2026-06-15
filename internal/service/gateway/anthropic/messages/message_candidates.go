package messages

import (
	"context"

	gatewayapi "github.com/ThankCat/unio-api/internal/app/gatewayapi/anthropic/messages"
	messagesadapter "github.com/ThankCat/unio-api/internal/core/adapter/anthropic/messages"
	"github.com/ThankCat/unio-api/internal/core/routing"
	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/service/gateway/lifecycle"
)

func (s *MessagesService) prepareMessageCandidates(ctx context.Context, req gatewayapi.MessageRequest, candidates []routing.ChatRouteCandidate, mode string, stream bool) (lifecycle.CandidatePlan, error) {
	capabilities := []lifecycle.AdapterCapability{
		lifecycle.AdapterCapabilityInputTokenizer,
	}
	if stream {
		capabilities = append(capabilities, lifecycle.AdapterCapabilityStream)
	} else {
		capabilities = append(capabilities, lifecycle.AdapterCapabilityNonStream)
	}

	return s.candidates.PrepareCandidates(ctx, lifecycle.PrepareCandidatesParams{
		Protocol:            routing.ProtocolAnthropic,
		Candidates:          candidates,
		Capabilities:        capabilities,
		Available:           s.candidateAvailable,
		EstimateInputTokens: s.messagesInputTokenEstimator(req),
		Mode:                mode,
		ChannelHealthScore:  s.channelHealthScore,
	})
}

func (s *MessagesService) messagesInputTokenEstimator(req gatewayapi.MessageRequest) lifecycle.CandidateInputTokenEstimator {
	return func(_ context.Context, candidate routing.ChatRouteCandidate) (int64, error) {
		tokenizer, ok := s.registry.MessagesInputTokenizer(candidate.AdapterKey)
		if !ok {
			return 0, failure.New(
				failure.CodeGatewayAdapterNotRegistered,
				failure.WithMessage("anthropic messages input tokenizer is not registered"),
				failure.WithField("protocol", routing.ProtocolAnthropic),
				failure.WithField("adapter_key", candidate.AdapterKey),
			)
		}

		adapterReq := mapGatewayRequestToAdapter(req, candidate.UpstreamModel)
		inputTokens, err := tokenizer.CountMessagesInputTokens(messagesadapter.MessagesInputTokenizeRequest{
			Model:    adapterReq.Model,
			System:   adapterReq.System,
			Messages: adapterReq.Messages,
			Tools:    adapterReq.Tools,
		})
		if err != nil {
			return 0, failure.Wrap(
				failure.CodeAdapterTokenizeFailed,
				err,
				failure.WithMessage("count anthropic messages input tokens"),
				failure.WithField("protocol", routing.ProtocolAnthropic),
				failure.WithField("adapter_key", candidate.AdapterKey),
				failure.WithField("upstream_model", candidate.UpstreamModel),
			)
		}

		return inputTokens, nil
	}
}

func estimateMaxOutputTokens(req gatewayapi.MessageRequest) int64 {
	if req.MaxTokens != nil && *req.MaxTokens > 0 {
		return int64(*req.MaxTokens)
	}
	return lifecycle.DefaultAuthorizationMaxCompletionTokens
}
