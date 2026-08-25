package lifecycle

import (
	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/core/usage"
)

func usageFactsOf(facts *adapter.ResponseFacts) usage.Facts {
	if facts == nil {
		return usage.Facts{}
	}
	return facts.Usage
}
