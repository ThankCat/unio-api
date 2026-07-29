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

// actualTotalTokens returns cache-inclusive reliable usage used only to settle Redis TPM.
// Billing continues to price every category independently from the same Facts value.
func actualTotalTokens(f usage.Facts) (int64, bool) {
	if !f.Valid() {
		return 0, false
	}
	return f.ActualTotalTokens()
}
