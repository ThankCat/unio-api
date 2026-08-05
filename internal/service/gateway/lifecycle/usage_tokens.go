package lifecycle

import (
	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/core/auth"
	"github.com/ThankCat/unio-gateway/internal/core/usage"
)

func routeIDOf(p *auth.APIKeyPrincipal) *int64 {
	if p == nil {
		return nil
	}
	return p.RouteID
}

func usageFactsOf(facts *adapter.ResponseFacts) usage.Facts {
	if facts == nil {
		return usage.Facts{}
	}
	return facts.Usage
}
