package chatcompletions

import (
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/core/servicetier"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/openai/chatcompletions/dto"
)

// requestForOpenAIChannel returns an attempt-local request with the wire tier resolved for one Channel.
func requestForOpenAIChannel(req dto.ChatCompletionRequest, requested servicetier.Tier, candidate routing.ChatRouteCandidate) dto.ChatCompletionRequest {
	forwarded := servicetier.ResolveOpenAIForwardRequest(requested, candidate.SupportsOpenAIFast)
	attemptReq := req
	attemptReq.ServiceTier = &forwarded.UpstreamRaw
	return attemptReq
}
