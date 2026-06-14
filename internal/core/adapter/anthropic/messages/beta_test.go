package messages

import (
	"testing"
)

func TestFilterSupportedBetasKeepsWhitelistInOrder(t *testing.T) {
	got := filterSupportedBetas([]string{
		"made-up-beta",
		"prompt-caching-2024-07-31",
		"fine-grained-tool-streaming-2025-05-14",
		"prompt-caching-2024-07-31",
	})
	want := []string{
		"prompt-caching-2024-07-31",
		"fine-grained-tool-streaming-2025-05-14",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestFilterSupportedBetasReturnsNilWhenNoneSupported(t *testing.T) {
	if got := filterSupportedBetas([]string{"made-up-beta"}); got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}

func TestDroppedBetas(t *testing.T) {
	got := droppedBetas([]string{
		"prompt-caching-2024-07-31",
		"made-up-beta",
		"another-unknown",
	})
	want := []string{"made-up-beta", "another-unknown"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
