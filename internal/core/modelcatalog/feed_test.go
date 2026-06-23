package modelcatalog

import (
	"reflect"
	"sort"
	"testing"

	"github.com/ThankCat/unio-api/internal/core/capability"
)

const sampleModelsJSON = `{
  "deepseek/deepseek-v4-pro": {
    "id": "deepseek/deepseek-v4-pro",
    "name": "DeepSeek V4 Pro",
    "family": "deepseek-thinking",
    "attachment": false,
    "reasoning": true,
    "tool_call": true,
    "structured_output": true,
    "release_date": "2026-01-15",
    "modalities": { "input": ["text", "image"], "output": ["text"] },
    "limit": { "context": 131072, "output": 8192 }
  },
  "acme/acme-mini": {
    "id": "acme/acme-mini",
    "name": "Acme Mini",
    "attachment": false,
    "reasoning": false,
    "tool_call": false,
    "structured_output": false,
    "release_date": "",
    "modalities": { "input": ["text"], "output": ["text"] },
    "limit": { "context": 0, "output": null }
  }
}`

const sampleAPIJSON = `{
  "deepseek": {
    "id": "deepseek",
    "models": {
      "deepseek-v4-pro": { "cost": { "input": 0.435, "output": 0.87 } }
    }
  }
}`

func TestParseFeedMergesMetadataAndPrice(t *testing.T) {
	feed, err := ParseFeed([]byte(sampleModelsJSON), []byte(sampleAPIJSON))
	if err != nil {
		t.Fatalf("ParseFeed: %v", err)
	}
	if len(feed.Models) != 2 {
		t.Fatalf("want 2 models, got %d", len(feed.Models))
	}
	if feed.Fingerprint == "" {
		t.Fatal("want non-empty fingerprint")
	}

	// 按 canonical_id 升序：acme/acme-mini 在前。
	if feed.Models[0].CanonicalID != "acme/acme-mini" || feed.Models[1].CanonicalID != "deepseek/deepseek-v4-pro" {
		t.Fatalf("models not sorted by canonical_id: %s, %s", feed.Models[0].CanonicalID, feed.Models[1].CanonicalID)
	}

	deepseek := feed.Models[1]
	if deepseek.Lab != "deepseek" {
		t.Fatalf("lab = %q, want deepseek", deepseek.Lab)
	}
	if deepseek.DisplayName != "DeepSeek V4 Pro" {
		t.Fatalf("display = %q", deepseek.DisplayName)
	}
	if deepseek.ContextTokens == nil || *deepseek.ContextTokens != 131072 {
		t.Fatalf("context = %v, want 131072", deepseek.ContextTokens)
	}
	if deepseek.MaxOutputTokens == nil || *deepseek.MaxOutputTokens != 8192 {
		t.Fatalf("max output = %v, want 8192", deepseek.MaxOutputTokens)
	}
	if deepseek.ReleaseDate == nil || deepseek.ReleaseDate.Format("2006-01-02") != "2026-01-15" {
		t.Fatalf("release date = %v", deepseek.ReleaseDate)
	}
	if deepseek.InputPrice == nil || *deepseek.InputPrice != "0.435" {
		t.Fatalf("input price = %v, want 0.435", deepseek.InputPrice)
	}
	if deepseek.OutputPrice == nil || *deepseek.OutputPrice != "0.87" {
		t.Fatalf("output price = %v, want 0.87", deepseek.OutputPrice)
	}

	wantCaps := sortedKeyStrings([]capability.Key{
		capability.Key("text.input"), capability.Key("text.output"),
		capability.Key("tools.function"), capability.Key("reasoning.effort"),
		capability.Key("response_format.json_schema"), capability.Key("image.input"),
	})
	if got := declKeySet(deepseek.CoarseCapabilities); !reflect.DeepEqual(got, wantCaps) {
		t.Fatalf("deepseek coarse caps = %v, want %v", got, wantCaps)
	}

	acme := feed.Models[0]
	if acme.ContextTokens != nil {
		t.Fatalf("acme context should be nil for 0 value, got %v", *acme.ContextTokens)
	}
	if acme.MaxOutputTokens != nil {
		t.Fatalf("acme max output should be nil, got %v", *acme.MaxOutputTokens)
	}
	if acme.ReleaseDate != nil {
		t.Fatalf("acme release date should be nil")
	}
	if acme.InputPrice != nil || acme.OutputPrice != nil {
		t.Fatalf("acme has no provider price, want nil prices")
	}
	wantAcmeCaps := sortedKeyStrings([]capability.Key{capability.Key("text.input"), capability.Key("text.output")})
	if got := declKeySet(acme.CoarseCapabilities); !reflect.DeepEqual(got, wantAcmeCaps) {
		t.Fatalf("acme coarse caps = %v, want text only", got)
	}
}

func TestParseFeedToleratesMissingAPIJSON(t *testing.T) {
	feed, err := ParseFeed([]byte(sampleModelsJSON), nil)
	if err != nil {
		t.Fatalf("ParseFeed: %v", err)
	}
	for _, model := range feed.Models {
		if model.InputPrice != nil || model.OutputPrice != nil {
			t.Fatalf("model %s should have no price without api.json", model.CanonicalID)
		}
	}
}

func TestParseFeedRejectsInvalidModelsJSON(t *testing.T) {
	if _, err := ParseFeed([]byte("not json"), nil); err == nil {
		t.Fatal("want error for invalid models.json")
	}
}

func TestParseFeedToleratesInvalidAPIJSON(t *testing.T) {
	feed, err := ParseFeed([]byte(sampleModelsJSON), []byte("not json"))
	if err != nil {
		t.Fatalf("ParseFeed should tolerate bad api.json: %v", err)
	}
	for _, model := range feed.Models {
		if model.InputPrice != nil {
			t.Fatalf("bad api.json must yield nil prices, model %s", model.CanonicalID)
		}
	}
}

func declKeySet(decls []capability.Declaration) []string {
	keys := make([]capability.Key, 0, len(decls))
	for _, decl := range decls {
		keys = append(keys, decl.Key)
	}
	return sortedKeyStrings(keys)
}

func sortedKeyStrings(keys []capability.Key) []string {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, string(k))
	}
	sort.Strings(out)
	return out
}
