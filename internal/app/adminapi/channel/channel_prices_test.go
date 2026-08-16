package channel

import (
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/service/admin/channelprice"
)

func TestToChannelPriceDTOIncludesFastCosts(t *testing.T) {
	now := time.Date(2026, time.August, 15, 0, 0, 0, 0, time.UTC)
	dto := toChannelPriceDTO(channelprice.ChannelPrice{
		ID:                1,
		ChannelID:         2,
		ModelID:           3,
		UncachedInputCost: "0.1",
		OutputCost:        "0.4",
		FastCostStatus:    "configured",
		FastCosts: &channelprice.FastCost{
			ServiceTierID:     4,
			UncachedInputCost: "0.2",
			OutputCost:        "0.8",
		},
		EffectiveFrom: now,
		CreatedAt:     now,
		UpdatedAt:     now,
	})

	if dto.FastCosts == nil || dto.FastCosts.ServiceTierID != 4 || dto.FastCosts.UncachedInputCost != "0.2" || dto.FastCosts.OutputCost != "0.8" {
		t.Fatalf("Fast costs missing from DTO: %+v", dto.FastCosts)
	}
}
