package channelprice

import "testing"

func TestParseFastCostConfig(t *testing.T) {
	t.Run("missing is not configured", func(t *testing.T) {
		got, err := parseFastCostConfig(nil)
		if err != nil {
			t.Fatalf("parseFastCostConfig(nil) error = %v", err)
		}
		if got.configured {
			t.Fatal("missing Fast costs must not be configured")
		}
	})

	t.Run("preserves exact vector", func(t *testing.T) {
		cacheRead := "0.08"
		got, err := parseFastCostConfig(&FastCostInput{
			UncachedInputCost:  "0.2",
			CacheReadInputCost: &cacheRead,
			OutputCost:         "0.8",
		})
		if err != nil {
			t.Fatalf("parseFastCostConfig() error = %v", err)
		}
		if !got.configured || !got.uncachedInputCost.Valid || !got.cacheReadInputCost.Valid || !got.outputCost.Valid {
			t.Fatalf("Fast cost vector was not fully parsed: %+v", got)
		}
	})

	t.Run("requires both primary costs", func(t *testing.T) {
		if _, err := parseFastCostConfig(&FastCostInput{OutputCost: "0.8"}); err == nil {
			t.Fatal("missing Fast uncached input cost must fail")
		}
		if _, err := parseFastCostConfig(&FastCostInput{UncachedInputCost: "0.2"}); err == nil {
			t.Fatal("missing Fast output cost must fail")
		}
	})
}
