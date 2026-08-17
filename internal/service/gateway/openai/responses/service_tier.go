package responses

import (
	gatewayapi "github.com/ThankCat/unio-gateway/internal/app/gatewayapi/openai/responses"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/core/servicetier"
)

// requestForOpenAIChannel returns an attempt-local request with the wire tier resolved for one Channel.
func requestForOpenAIChannel(req gatewayapi.ResponsesRequest, requested servicetier.Tier, candidate routing.ChatRouteCandidate) gatewayapi.ResponsesRequest {
	forwarded := servicetier.ResolveOpenAIForwardRequest(requested, candidate.SupportsOpenAIFast)
	attemptReq := req
	attemptReq.ServiceTier = &forwarded.UpstreamRaw
	return attemptReq
}
