package provider

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/service/admin/providerops"
)

func TestProviderOpsRowDTOIncludesEndpointArray(t *testing.T) {
	row := providerops.Row{
		ID: 1, Slug: "starapi", Name: "StarAPI", Status: "enabled",
		CreatedAt: time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC),
		Endpoints: []providerops.EndpointSummary{{
			ID: 11, Name: "primary", BaseURL: "https://api.example.com/v1", Status: "enabled",
		}},
	}
	dto := providerOpsRowDTOFrom(row)
	if len(dto.Endpoints) != 1 || dto.Endpoints[0].ID != 11 || dto.Endpoints[0].BaseURL != "https://api.example.com/v1" {
		t.Fatalf("unexpected endpoint DTOs: %+v", dto.Endpoints)
	}

	emptyJSON, err := json.Marshal(providerOpsRowDTOFrom(providerops.Row{}))
	if err != nil {
		t.Fatalf("marshal empty DTO: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(emptyJSON, &payload); err != nil {
		t.Fatalf("decode empty DTO: %v", err)
	}
	endpoints, ok := payload["endpoints"].([]any)
	if !ok || len(endpoints) != 0 {
		t.Fatalf("endpoints must serialize as [], got %s", emptyJSON)
	}
}
