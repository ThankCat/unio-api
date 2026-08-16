package modelprice

import (
	"strings"
	"time"
)

const openAIFastPricingSource = "https://developers.openai.com/api/docs/pricing?latest-pricing=fast"

var openAIFastPriceCheckedAt = time.Date(2026, time.August, 16, 0, 0, 0, 0, time.UTC)

var openAIFastPriceReferences = map[string]FastPriceReference{
	"gpt-5.6-sol":       newOpenAIFastPriceReference("10.00", "1.00", "12.50", "60.00"),
	"gpt-5.6-terra":     newOpenAIFastPriceReference("4.00", "0.40", "5.00", "24.00"),
	"gpt-5.6-luna":      newOpenAIFastPriceReference("0.40", "0.04", "0.50", "2.40"),
	"gpt-5.5":           newOpenAIFastPriceReference("12.50", "1.25", "", "75.00"),
	"gpt-5.4":           newOpenAIFastPriceReference("5.00", "0.50", "", "30.00"),
	"gpt-5.4-mini":      newOpenAIFastPriceReference("1.50", "0.15", "", "9.00"),
	"gpt-5.2":           newOpenAIFastPriceReference("3.50", "0.35", "", "28.00"),
	"gpt-5.1":           newOpenAIFastPriceReference("2.50", "0.25", "", "20.00"),
	"gpt-5":             newOpenAIFastPriceReference("2.50", "0.25", "", "20.00"),
	"gpt-5-mini":        newOpenAIFastPriceReference("0.45", "0.045", "", "3.60"),
	"gpt-4.1":           newOpenAIFastPriceReference("3.50", "0.875", "", "14.00"),
	"gpt-4.1-mini":      newOpenAIFastPriceReference("0.70", "0.175", "", "2.80"),
	"gpt-4.1-nano":      newOpenAIFastPriceReference("0.20", "0.05", "", "0.80"),
	"gpt-4o":            newOpenAIFastPriceReference("4.25", "2.125", "", "17.00"),
	"gpt-4o-mini":       newOpenAIFastPriceReference("0.25", "0.125", "", "1.00"),
	"o4-mini":           newOpenAIFastPriceReference("2.00", "0.50", "", "8.00"),
	"o3":                newOpenAIFastPriceReference("3.50", "0.875", "", "14.00"),
	"gpt-4o-2024-05-13": newOpenAIFastPriceReference("8.75", "", "", "26.25"),
}

// newOpenAIFastPriceReference builds one short-context text-token row from the official
// Fast mode table. OpenAI's Cache writes column maps to the existing 30m cache-write field.
func newOpenAIFastPriceReference(uncachedInput, cacheRead, cacheWrite30m, output string) FastPriceReference {
	reference := FastPriceReference{
		Currency:           "USD",
		PricingUnit:        PricingUnitPer1MTokens,
		UncachedInputPrice: uncachedInput,
		OutputPrice:        output,
		Source:             openAIFastPricingSource,
		CheckedAt:          openAIFastPriceCheckedAt,
	}
	if cacheRead != "" {
		reference.CacheReadInputPrice = stringPointer(cacheRead)
	}
	if cacheWrite30m != "" {
		reference.CacheWrite30mInputPrice = stringPointer(cacheWrite30m)
	}
	return reference
}

func officialFastPriceReference(modelID string) *FastPriceReference {
	canonical := strings.TrimPrefix(strings.TrimSpace(modelID), "openai/")
	reference, ok := openAIFastPriceReferences[canonical]
	if !ok {
		return nil
	}
	copy := reference
	return &copy
}

func stringPointer(value string) *string {
	return &value
}
