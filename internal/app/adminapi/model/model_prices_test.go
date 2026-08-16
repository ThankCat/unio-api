package model

import (
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/service/admin/modelprice"
)

func TestToModelPriceDTOIncludesFastFacts(t *testing.T) {
	source := "https://developers.openai.com/api/docs/pricing?latest-pricing=fast"
	checkedAt := time.Date(2026, time.August, 15, 0, 0, 0, 0, time.UTC)
	cacheRead := "0.125"
	dto := toModelPriceDTO(modelprice.ModelPrice{
		ID:                 1,
		ModelID:            2,
		UncachedInputPrice: "0.15",
		OutputPrice:        "0.60",
		FastPriceStatus:    "configured",
		FastPrices: &modelprice.FastPrice{
			ServiceTierID:       3,
			UncachedInputPrice:  "0.25",
			CacheReadInputPrice: &cacheRead,
			OutputPrice:         "1.00",
			ReferenceSource:     &source,
			ReferenceCheckedAt:  &checkedAt,
		},
		FastPriceReference: &modelprice.FastPriceReference{
			Currency:           "USD",
			PricingUnit:        modelprice.PricingUnitPer1MTokens,
			UncachedInputPrice: "0.25",
			OutputPrice:        "1.00",
			Source:             source,
			CheckedAt:          checkedAt,
		},
		EffectiveFrom: checkedAt,
		CreatedAt:     checkedAt,
		UpdatedAt:     checkedAt,
	})

	if dto.FastPrices == nil || dto.FastPrices.ServiceTierID != 3 || dto.FastPrices.ReferenceCheckedAt == nil || *dto.FastPrices.ReferenceCheckedAt != "2026-08-15" {
		t.Fatalf("Fast prices missing from DTO: %+v", dto.FastPrices)
	}
	if dto.FastPriceReference == nil || dto.FastPriceReference.Source != source {
		t.Fatalf("Fast reference missing from DTO: %+v", dto.FastPriceReference)
	}
}
