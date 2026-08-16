package modelprice

import (
	"testing"
	"time"
)

func TestParseFastPriceConfig(t *testing.T) {
	t.Run("missing is not configured", func(t *testing.T) {
		got, err := parseFastPriceConfig(nil)
		if err != nil {
			t.Fatalf("parseFastPriceConfig(nil) error = %v", err)
		}
		if got.configured {
			t.Fatal("missing Fast prices must not be configured")
		}
	})

	t.Run("preserves exact vector and reference", func(t *testing.T) {
		source := openAIFastPricingSource
		checkedAt := time.Date(2026, time.August, 15, 0, 0, 0, 0, time.UTC)
		cacheRead := "0.125"
		got, err := parseFastPriceConfig(&FastPriceInput{
			UncachedInputPrice:  "0.25",
			CacheReadInputPrice: &cacheRead,
			OutputPrice:         "1.00",
			ReferenceSource:     &source,
			ReferenceCheckedAt:  &checkedAt,
		})
		if err != nil {
			t.Fatalf("parseFastPriceConfig() error = %v", err)
		}
		if !got.configured || !got.uncachedInputPrice.Valid || !got.cacheReadInputPrice.Valid || !got.outputPrice.Valid {
			t.Fatalf("Fast vector was not fully parsed: %+v", got)
		}
		if got.referenceSource.String != source || !got.referenceCheckedAt.Valid || !got.referenceCheckedAt.Time.Equal(checkedAt) {
			t.Fatalf("Fast reference mismatch: %+v", got)
		}
	})

	t.Run("requires both primary prices", func(t *testing.T) {
		if _, err := parseFastPriceConfig(&FastPriceInput{OutputPrice: "1.00"}); err == nil {
			t.Fatal("missing Fast uncached input price must fail")
		}
		if _, err := parseFastPriceConfig(&FastPriceInput{UncachedInputPrice: "0.25"}); err == nil {
			t.Fatal("missing Fast output price must fail")
		}
	})

	t.Run("requires reference source and date together", func(t *testing.T) {
		source := openAIFastPricingSource
		if _, err := parseFastPriceConfig(&FastPriceInput{
			UncachedInputPrice: "0.25",
			OutputPrice:        "1.00",
			ReferenceSource:    &source,
		}); err == nil {
			t.Fatal("source without checked date must fail")
		}
	})
}

func TestOfficialFastPriceReference(t *testing.T) {
	expected := map[string]struct {
		input      string
		cacheRead  string
		cacheWrite string
		output     string
	}{
		"gpt-5.6-sol":       {input: "10.00", cacheRead: "1.00", cacheWrite: "12.50", output: "60.00"},
		"gpt-5.6-terra":     {input: "4.00", cacheRead: "0.40", cacheWrite: "5.00", output: "24.00"},
		"gpt-5.6-luna":      {input: "0.40", cacheRead: "0.04", cacheWrite: "0.50", output: "2.40"},
		"gpt-5.5":           {input: "12.50", cacheRead: "1.25", output: "75.00"},
		"gpt-5.4":           {input: "5.00", cacheRead: "0.50", output: "30.00"},
		"gpt-5.4-mini":      {input: "1.50", cacheRead: "0.15", output: "9.00"},
		"gpt-5.2":           {input: "3.50", cacheRead: "0.35", output: "28.00"},
		"gpt-5.1":           {input: "2.50", cacheRead: "0.25", output: "20.00"},
		"gpt-5":             {input: "2.50", cacheRead: "0.25", output: "20.00"},
		"gpt-5-mini":        {input: "0.45", cacheRead: "0.045", output: "3.60"},
		"gpt-4.1":           {input: "3.50", cacheRead: "0.875", output: "14.00"},
		"gpt-4.1-mini":      {input: "0.70", cacheRead: "0.175", output: "2.80"},
		"gpt-4.1-nano":      {input: "0.20", cacheRead: "0.05", output: "0.80"},
		"gpt-4o":            {input: "4.25", cacheRead: "2.125", output: "17.00"},
		"gpt-4o-mini":       {input: "0.25", cacheRead: "0.125", output: "1.00"},
		"o4-mini":           {input: "2.00", cacheRead: "0.50", output: "8.00"},
		"o3":                {input: "3.50", cacheRead: "0.875", output: "14.00"},
		"gpt-4o-2024-05-13": {input: "8.75", output: "26.25"},
	}

	if len(openAIFastPriceReferences) != len(expected) {
		t.Fatalf("Fast reference count = %d, want %d", len(openAIFastPriceReferences), len(expected))
	}
	for model, want := range expected {
		t.Run(model, func(t *testing.T) {
			got := officialFastPriceReference("openai/" + model)
			if got == nil {
				t.Fatalf("expected %s Fast reference", model)
			}
			if got.UncachedInputPrice != want.input || got.OutputPrice != want.output {
				t.Fatalf("Fast reference = %+v, want input=%s output=%s", got, want.input, want.output)
			}
			assertOptionalReferencePrice(t, "cache read", got.CacheReadInputPrice, want.cacheRead)
			assertOptionalReferencePrice(t, "cache write 30m", got.CacheWrite30mInputPrice, want.cacheWrite)
			if got.Source != openAIFastPricingSource || !got.CheckedAt.Equal(openAIFastPriceCheckedAt) {
				t.Fatalf("Fast reference audit facts = source %q checked %s", got.Source, got.CheckedAt)
			}
		})
	}
	if officialFastPriceReference("gpt-unknown") != nil {
		t.Fatal("unknown model must not receive a fabricated Fast reference")
	}
}

func assertOptionalReferencePrice(t *testing.T, field string, got *string, want string) {
	t.Helper()
	if want == "" {
		if got != nil {
			t.Fatalf("%s = %q, want nil", field, *got)
		}
		return
	}
	if got == nil || *got != want {
		t.Fatalf("%s = %v, want %q", field, got, want)
	}
}
