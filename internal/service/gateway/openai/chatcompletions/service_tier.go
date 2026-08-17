package chatcompletions

import (
	gatewayapi "github.com/ThankCat/unio-gateway/internal/app/gatewayapi/openai/chatcompletions"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/core/servicetier"
)

// requestForOpenAIChannel returns an attempt-local request with the wire tier resolved for one Channel.
func requestForOpenAIChannel(req gatewayapi.ChatCompletionRequest, requested servicetier.Tier, candidate routing.ChatRouteCandidate) gatewayapi.ChatCompletionRequest {
	forwarded := servicetier.ResolveOpenAIForwardRequest(requested, candidate.SupportsOpenAIFast)
	attemptReq := req
	attemptReq.ServiceTier = &forwarded.UpstreamRaw
	return attemptReq
}
