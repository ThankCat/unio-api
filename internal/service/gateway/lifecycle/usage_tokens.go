package lifecycle

import (
	"github.com/ThankCat/unio-gateway/internal/core/auth"
	"github.com/ThankCat/unio-gateway/internal/core/usage"
)

func routeIDOf(p *auth.APIKeyPrincipal) *int64 {
	if p == nil {
		return nil
	}
	return p.RouteID
}

// billableTPMTokens uses the same cache-aware token accounting for request admission,
// attempt permits and settlement publication. Cache reads do not consume upstream TPM.
func billableTPMTokens(f usage.Facts) int64 {
	total := int64(0)
	for _, c := range []usage.TokenCount{
		f.UncachedInputTokens,
		f.CacheWrite5mInputTokens,
		f.CacheWrite1hInputTokens,
		f.CacheWrite30mInputTokens,
		f.OutputTokensTotal,
	} {
		if v, ok := c.BillableValue(); ok && v > 0 {
			total += v
		}
	}
	return total
}
