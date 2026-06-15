package modelcatalog

import (
	"testing"
	"time"

	"github.com/ThankCat/unio-api/internal/core/capability"
)

func baseModel() CanonicalModel {
	ctx := int64(128000)
	out := int64(16384)
	in := "2.5"
	op := "10"
	rel := time.Date(2024, 5, 13, 0, 0, 0, 0, time.UTC)
	return CanonicalModel{
		CanonicalID:     "openai/gpt-4o",
		Lab:             "openai",
		DisplayName:     "GPT-4o",
		ContextTokens:   &ctx,
		MaxOutputTokens: &out,
		InputPrice:      &in,
		OutputPrice:     &op,
		ReleaseDate:     &rel,
		CoarseCapabilities: []capability.Declaration{
			{Key: capability.KeyTextInput, SupportLevel: capability.SupportLevelFull},
			{Key: capability.KeyToolsFunction, SupportLevel: capability.SupportLevelFull},
		},
	}
}

func TestEntryFingerprintStableAndOrderIndependent(t *testing.T) {
	a := baseModel()
	b := baseModel()
	// 能力顺序打乱不应改变指纹（实现内部排序）。
	b.CoarseCapabilities = []capability.Declaration{
		{Key: capability.KeyToolsFunction, SupportLevel: capability.SupportLevelFull},
		{Key: capability.KeyTextInput, SupportLevel: capability.SupportLevelFull},
	}
	if entryFingerprint(a) != entryFingerprint(b) {
		t.Fatal("fingerprint must be stable regardless of capability order")
	}
}

func TestEntryFingerprintSensitiveToChanges(t *testing.T) {
	base := entryFingerprint(baseModel())

	cases := map[string]func(*CanonicalModel){
		"display_name": func(m *CanonicalModel) { m.DisplayName = "GPT-4o v2" },
		"context":      func(m *CanonicalModel) { v := int64(200000); m.ContextTokens = &v },
		"max_output":   func(m *CanonicalModel) { v := int64(4096); m.MaxOutputTokens = &v },
		"input_price":  func(m *CanonicalModel) { v := "3.0"; m.InputPrice = &v },
		"release_date": func(m *CanonicalModel) { v := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC); m.ReleaseDate = &v },
		"add_capability": func(m *CanonicalModel) {
			m.CoarseCapabilities = append(m.CoarseCapabilities, capability.Declaration{Key: capability.KeyImageInput, SupportLevel: capability.SupportLevelFull})
		},
		"capability_level": func(m *CanonicalModel) {
			m.CoarseCapabilities[0].SupportLevel = capability.SupportLevelLimited
		},
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			m := baseModel()
			mutate(&m)
			if entryFingerprint(m) == base {
				t.Fatalf("fingerprint must change when %s changes", name)
			}
		})
	}
}
