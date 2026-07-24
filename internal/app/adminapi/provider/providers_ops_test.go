package provider

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/service/admin/providerops"
)

func TestProviderOpsRowDTOIncludesOriginArray(t *testing.T) {
	row := providerops.Row{
		ID: 1, Slug: "starapi", Name: "StarAPI", Status: "enabled",
		CreatedAt: time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC),
		Origins: []providerops.OriginSummary{{
			ID: 11, Name: "primary", BaseURL: "https://api.example.com/v1", Status: "enabled",
		}},
	}
	dto := providerOpsRowDTOFrom(row)
	if len(dto.Origins) != 1 || dto.Origins[0].ID != 11 || dto.Origins[0].BaseURL != "https://api.example.com/v1" {
		t.Fatalf("unexpected origin DTOs: %+v", dto.Origins)
	}

	emptyJSON, err := json.Marshal(providerOpsRowDTOFrom(providerops.Row{}))
	if err != nil {
		t.Fatalf("marshal empty DTO: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(emptyJSON, &payload); err != nil {
		t.Fatalf("decode empty DTO: %v", err)
	}
	origins, ok := payload["origins"].([]any)
	if !ok || len(origins) != 0 {
		t.Fatalf("origins must serialize as [], got %s", emptyJSON)
	}
}
