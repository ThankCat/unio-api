package channelmodelinventory

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

func TestBuildInventoryMatchCases(t *testing.T) {
	exact := sqlc.ListChannelModelInventoryMatchesRow{
		ExactModelID:          pgtype.Int8{Int64: 11, Valid: true},
		ExactModelExternalID:  pgtype.Text{String: "model-a", Valid: true},
		ExactModelDisplayName: pgtype.Text{String: "Model A", Valid: true},
		ExactModelStatus:      pgtype.Text{String: "enabled", Valid: true},
	}
	catalog := func(canonical string, adoptedID int64) sqlc.ListChannelModelInventoryMatchesRow {
		row := sqlc.ListChannelModelInventoryMatchesRow{
			CatalogCanonicalID: pgtype.Text{String: canonical, Valid: true},
			CatalogLab:         pgtype.Text{String: "lab", Valid: true},
			CatalogDisplayName: pgtype.Text{String: canonical, Valid: true},
		}
		if adoptedID > 0 {
			row.AdoptedModelID = pgtype.Int8{Int64: adoptedID, Valid: true}
			row.AdoptedModelExternalID = pgtype.Text{String: "adopted", Valid: true}
			row.AdoptedModelDisplayName = pgtype.Text{String: "Adopted", Valid: true}
			row.AdoptedModelStatus = pgtype.Text{String: "disabled", Valid: true}
		}
		return row
	}

	tests := []struct {
		name     string
		bindings []InventoryBinding
		rows     []sqlc.ListChannelModelInventoryMatchesRow
		want     string
	}{
		{name: "bound takes precedence", bindings: []InventoryBinding{{ID: 1}}, rows: []sqlc.ListChannelModelInventoryMatchesRow{exact}, want: "bound"},
		{name: "local exact model", rows: []sqlc.ListChannelModelInventoryMatchesRow{exact}, want: "local_model"},
		{name: "single adopted model", rows: []sqlc.ListChannelModelInventoryMatchesRow{catalog("lab/model-a", 12)}, want: "adopted_model"},
		{name: "catalog requires adoption", rows: []sqlc.ListChannelModelInventoryMatchesRow{catalog("lab/model-a", 0)}, want: "catalog"},
		{name: "ambiguous catalog", rows: []sqlc.ListChannelModelInventoryMatchesRow{catalog("lab-a/model-a", 0), catalog("lab-b/model-a", 0)}, want: "ambiguous_catalog"},
		{name: "no match", want: "none"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildInventoryMatch(tt.bindings, tt.rows)
			if got.Kind != tt.want {
				t.Fatalf("kind=%q want=%q", got.Kind, tt.want)
			}
		})
	}
}

func TestInventoryItemPendingKeepsStatusesIndependent(t *testing.T) {
	success := &InventoryVerification{Status: "succeeded", Current: true}
	tests := []struct {
		name string
		item InventoryItem
		want bool
	}{
		{
			name: "discovered enabled verified",
			item: InventoryItem{DiscoveryState: "discovered", Bindings: []InventoryBinding{
				{Status: "enabled", Verification: success},
			}},
			want: false,
		},
		{
			name: "binding disabled remains pending",
			item: InventoryItem{DiscoveryState: "discovered", Bindings: []InventoryBinding{
				{Status: "disabled", Verification: success},
			}},
			want: true,
		},
		{
			name: "verification stale",
			item: InventoryItem{DiscoveryState: "discovered", Bindings: []InventoryBinding{
				{Status: "enabled", Verification: &InventoryVerification{Status: "succeeded"}},
			}},
			want: true,
		},
		{
			name: "binding not seen upstream",
			item: InventoryItem{DiscoveryState: "not_seen", Bindings: []InventoryBinding{
				{Status: "enabled", Verification: success},
			}},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := inventoryItemPending(tt.item); got != tt.want {
				t.Fatalf("pending=%v want=%v", got, tt.want)
			}
		})
	}
}
