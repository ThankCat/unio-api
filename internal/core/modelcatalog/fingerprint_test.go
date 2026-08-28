package modelcatalog

import (
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/core/capability"
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
			{Key: capability.Key("text.input"), SupportLevel: capability.SupportLevelFull},
			{Key: capability.Key("tools.function"), SupportLevel: capability.SupportLevelFull},
		},
	}
}

func TestEntryFingerprintStableAndOrderIndependent(t *testing.T) {
	a := baseModel()
	b := baseModel()
	// 能力顺序打乱不应改变指纹（实现内部排序）。
	b.CoarseCapabilities = []capability.Declaration{
		{Key: capability.Key("tools.function"), SupportLevel: capability.SupportLevelFull},
		{Key: capability.Key("text.input"), SupportLevel: capability.SupportLevelFull},
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
			m.CoarseCapabilities = append(m.CoarseCapabilities, capability.Declaration{Key: capability.Key("image.input"), SupportLevel: capability.SupportLevelFull})
		},
		"capability_level": func(m *CanonicalModel) {
			m.CoarseCapabilities[0].SupportLevel = capability.SupportLevelLimited
		},
		// 展示元数据也参与追更判定：上游改了简介/知识截止/缓存价等，采纳模型该收到提示。
		"description":          func(m *CanonicalModel) { m.Description = "updated blurb" },
		"knowledge_cutoff":     func(m *CanonicalModel) { m.KnowledgeCutoff = "2025-06" },
		"input_limit":          func(m *CanonicalModel) { v := int64(272000); m.InputLimitTokens = &v },
		"open_weights":         func(m *CanonicalModel) { v := true; m.OpenWeights = &v },
		"modalities_input":     func(m *CanonicalModel) { m.ModalitiesInput = []string{"text", "audio"} },
		"cache_read_price":     func(m *CanonicalModel) { v := "0.25"; m.CacheReadPrice = &v },
		"cache_creation_price": func(m *CanonicalModel) { v := "3.1"; m.CacheCreationPrice = &v },
		"last_updated":         func(m *CanonicalModel) { v := time.Date(2025, 2, 2, 0, 0, 0, 0, time.UTC); m.LastUpdated = &v },
		"capability_limits": func(m *CanonicalModel) {
			m.CoarseCapabilities[0].Limits = []byte(`{"effort":["low"]}`)
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
