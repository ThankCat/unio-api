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
	// ResolutionWireOutboundAuthoritative：该 wire 的响应档位不可信（Codex 订阅后端对 priority
	// 请求仍回 auto/default），结算档位以出站请求档位为权威（边界 15 的结算例外）。
	ResolutionWireOutboundAuthoritative Resolution = "wire_outbound_authoritative"
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

// ResolveOpenAIForwardRequest maps normalized customer intent to the value sent to one Channel.
// Fast is opt-in per Channel; unsupported Channels receive the OpenAI Standard wire value.
func ResolveOpenAIForwardRequest(requested Tier, supportsFast bool) Request {
	if requested == TierFast && supportsFast {
		return Request{Tier: TierFast, UpstreamRaw: "priority"}
	}
	return Request{Tier: TierStandard, UpstreamRaw: "default"}
}

// ResolveWireOutboundAuthoritative 按出站请求档位定档（响应档位不可信的 wire 专用，如 Codex 订阅后端）。
// outboundRaw 是发给上游的请求体 service_tier 原始值（nil=未带，按 Standard）；
// responseRaw 是上游响应回显的原始值，仅保留进 UpstreamRaw 供审计对照，不参与定档。
func ResolveWireOutboundAuthoritative(outboundRaw *string, responseRaw string) Response {
	tier := TierStandard
	if outboundRaw != nil {
		switch *outboundRaw {
		case "priority", "fast":
			tier = TierFast
		}
	}
	return Response{
		Actual:      tier,
		Settled:     tier,
		UpstreamRaw: responseRaw,
		Resolution:  ResolutionWireOutboundAuthoritative,
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
	// 官方 2026-07-30 把 priority 改名 fast，两个值都接受：现有模型响应仍回 priority，
	// gpt-5.6 之后发布的模型改回 fast。两值必须视为同一档，否则新模型上线时
	// Fast 会被误判成 Standard 并按低档结算（存量修正，不限号池）。
	case "priority", "fast":
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
