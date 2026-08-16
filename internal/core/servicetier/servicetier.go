// Package servicetier defines protocol-independent request, response, and settlement tiers.
package servicetier

import "errors"

// Tier is the normalized Gateway service tier used by routing-independent billing logic.
type Tier string

const (
	TierStandard Tier = "standard"
	TierFast     Tier = "fast"
)

// Resolution records how the settled tier was determined.
type Resolution string

const (
	ResolutionUpstreamResponse        Resolution = "upstream_response"
	ResolutionStandardFallbackMissing Resolution = "standard_fallback_missing"
	ResolutionStandardFallbackUnknown Resolution = "standard_fallback_unknown"
	ResolutionFastPriceMissing        Resolution = "standard_fallback_fast_price_missing"
)

// ErrInvalidOpenAIRequestTier means the public request value is outside the supported contract.
var ErrInvalidOpenAIRequestTier = errors.New("invalid OpenAI service_tier")

// Request describes the normalized customer intent and the value sent to OpenAI.
type Request struct {
	Tier        Tier
	UpstreamRaw string
}

// Response describes the service tier returned by the same upstream response.
// Actual is empty when the value is missing or unknown. Settled always has a billing fallback.
type Response struct {
	Actual      Tier
	Settled     Tier
	UpstreamRaw string
	Resolution  Resolution
}

// NormalizeOpenAIRequest maps the first-phase public contract to a stable Gateway tier.
func NormalizeOpenAIRequest(raw *string) (Request, error) {
	if raw == nil {
		return Request{Tier: TierStandard, UpstreamRaw: "default"}, nil
	}

	switch *raw {
	case "auto", "default":
		return Request{Tier: TierStandard, UpstreamRaw: "default"}, nil
	case "fast", "priority":
		return Request{Tier: TierFast, UpstreamRaw: "priority"}, nil
	default:
		return Request{}, ErrInvalidOpenAIRequestTier
	}
}

// ResolveOpenAIResponse maps the upstream response value without inferring it from the request.
func ResolveOpenAIResponse(raw *string) Response {
	if raw == nil || *raw == "" {
		return Response{
			Settled:    TierStandard,
			Resolution: ResolutionStandardFallbackMissing,
		}
	}

	switch *raw {
	case "default":
		return Response{
			Actual:      TierStandard,
			Settled:     TierStandard,
			UpstreamRaw: *raw,
			Resolution:  ResolutionUpstreamResponse,
		}
	case "priority":
		return Response{
			Actual:      TierFast,
			Settled:     TierFast,
			UpstreamRaw: *raw,
			Resolution:  ResolutionUpstreamResponse,
		}
	default:
		return Response{
			Settled:     TierStandard,
			UpstreamRaw: *raw,
			Resolution:  ResolutionStandardFallbackUnknown,
		}
	}
}
